package main

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cellmodules/percellpg"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/migration"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/capability"
	"github.com/ghbvf/gocell/framework/runtime/composition"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
	outboxruntime "github.com/ghbvf/gocell/framework/runtime/outbox"
)

// provisionCapabilities is the assembly's single shared-infrastructure
// provisioning site. It iterates the codegen-declared generatedCapabilities()
// (single source: the derived union of the assembly cells' cell.yaml `requires`,
// computed in kernel/assembly.GenerateModulesGen), and for each declared
// capability provisions the shared resource exactly once and wraps it into the
// sealed runtime/capability provider stored on shared. Consuming cell modules receive
// the injected provider (shared.PG / shared.Redis) and never construct adapter
// primitives themselves.
//
// Postgres per-cell DSN resolution (dedup-by-DSN + fail-closed gates) is decided
// by cellmodules/percellpg.Resolve, but the adapter construction (pool, TxManager,
// journaling outbox writer, capability.PGProvider) stays HERE — this file remains
// the single sanctioned shared-infra provisioning site in cmd/
// (CAPABILITY-PROVIDER-FUNNEL-01 + PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01).
//
// Called from runCorebundle after LoadSharedDepsFromEnv and before composition.Builder.Build
// so the providers are present (or fail-fast) before any module.Provide runs —
// mirroring fx.New()'s resolve-before-start ordering (ref: uber-go/fx app.go).
func provisionCapabilities(ctx context.Context, shared *composition.SharedDeps, locals *cmdLocals) error {
	// INVARIANT: generatedCapabilities() exists because the corebundle assembly's
	// cells declare a non-empty `requires` union (auditcore/configcore require
	// postgres). The codegen template emits generatedCapabilities() iff that union
	// is non-empty — so this unconditional call is only safe while corebundle keeps
	// provisioning ≥1 capability. A hypothetical zero-capability corebundle would
	// not need a provisioner at all; this function would then be removed alongside.
	for _, c := range generatedCapabilities() {
		switch c {
		case capability.Postgres:
			if err := provisionPostgres(ctx, shared, locals); err != nil {
				return err
			}
		case capability.Redis:
			provisionRedis(shared, locals)
		default:
			// Reached when a cell requires a capability that is a recognized enum
			// member but has no provisioning path here — today only rabbitmq (it is
			// in CapabilityEnum + capabilityConstNames, so it passes FMT-36 + codegen,
			// but provisionCapabilities has no rabbitmq case). By-design fail-fast:
			// a real provider must land its provisioning atomically. Truly unknown
			// values are already rejected upstream by FMT-36 + the codegen guard.
			// See runtime/capability.RabbitMQ.
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"corebundle: declared capability has no provisioning path",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("capability=%q", string(c)))))
		}
	}
	return nil
}

// provisionPostgres opens the assembly's N serving pools from the per-instance
// DSN plan resolved by cellmodules/percellpg.Resolve (#2341). percellpg owns the
// per-cell DSN decision (dedup-by-DSN + fail-closed gates: empty cell set, missing
// DSN) and the per-instance keying (colocated → 1 pool keyed DefaultInstanceKey;
// split → N pools keyed by representative cell); this site owns the actual adapter
// construction — pools, per-pool schema verification, journaling outbox writers,
// the capability.PGProvider/PGSet, AND the per-pool outbox relay — because the
// banned shared-infra constructors (NewPool / NewTxManager / NewJournalingOutboxWriter)
// and the relay (NewOutboxStore / NewRelay) must stay in the single sanctioned cmd/
// site that CAPABILITY-PROVIDER-FUNNEL-01, PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-
// DERIVED-01, and RELAY-CONSTRUCTION-CELLMODULE-BAN-01 cover. The relay is per-POOL
// infrastructure (one relay drains one pool's outbox table) — it lives here, beside
// the pool it drains, NOT in a cell module.
//
// The topology gate runs FIRST — before reading any per-cell PG env — so memory
// mode is never blocked by an unused / malformed GOCELL_<CELL>_DATABASE_* knob.
// In memory topology shared.PG stays nil and cell modules take their in-memory path.
//
// On a mid-loop failure the pools opened so far are already appended to
// locals.poolMRs, so runCorebundle's startup-abort defer (releaseUnhandedResources)
// closes them in LIFO order; the pool whose verify/relay just failed is closed
// inline (it is not yet in poolMRs).
func provisionPostgres(ctx context.Context, shared *composition.SharedDeps, locals *cmdLocals) error {
	if shared.Topology.StorageBackend() != bootstrap.StorageBackendPostgres {
		return nil
	}

	cells := make(map[string]adapterpg.Config, len(generatedPostgresCells()))
	for _, cellID := range generatedPostgresCells() {
		pgCfg, err := LoadPGConfig(strings.ToUpper(cellID))
		if err != nil {
			return fmt.Errorf("corebundle: load PG config for cell %s: %w", cellID, err)
		}
		cells[cellID] = pgCfg
	}

	// Topology is gated above, so percellpg.Resolve always returns ok=true here
	// (ok=false is the memory-topology signal only). The retained fail-closed gates
	// (empty cell set / missing DSN) surface as err.
	res, _, err := percellpg.Resolve(shared.Topology, percellpg.Config{
		Cells:                 cells,
		RequireRestrictedRole: true,
	})
	if err != nil {
		return err
	}

	pgInstances := make([]capability.PGInstance, 0, len(res.Instances))
	for _, inst := range orderedPGInstances(res) {
		provider, provErr := provisionPGInstance(ctx, shared, locals, inst)
		if provErr != nil {
			return provErr
		}
		pgInstances = append(pgInstances, capability.PGInstance{Provider: provider, Cells: inst.cells})
	}

	pgSet, err := capability.NewPGSet(pgInstances)
	if err != nil {
		return fmt.Errorf("corebundle: build per-cell PG set: %w", err)
	}
	shared.PG = pgSet
	return nil
}

// pgPoolInstance is one resolved pool: its identity key, the config to open it
// from, the (sorted) cells it serves, and its representative cell (the
// alphabetically-first served cell — the relay's metric/probe label).
type pgPoolInstance struct {
	key     bootstrap.InfraInstanceKey
	cfg     adapterpg.Config
	cells   []string
	repCell string
}

// orderedPGInstances inverts the Resolution into pool instances ordered
// deterministically by representative cell, so pool-open + LIFO teardown order is
// stable across runs.
func orderedPGInstances(res percellpg.Resolution) []pgPoolInstance {
	cellsByKey := make(map[bootstrap.InfraInstanceKey][]string, len(res.Instances))
	for cellID, key := range res.CellToInstance {
		cellsByKey[key] = append(cellsByKey[key], cellID)
	}
	out := make([]pgPoolInstance, 0, len(res.Instances))
	for key, cfg := range res.Instances {
		served := cellsByKey[key]
		sort.Strings(served)
		out = append(out, pgPoolInstance{key: key, cfg: cfg, cells: served, repCell: served[0]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].repCell < out[j].repCell })
	return out
}

// provisionPGInstance opens one pool, verifies its schema, wraps it into a
// capability.PGProvider, builds its per-pool outbox relay, and records the pool
// (ManagedResource) + relay option on locals. On verify/relay failure it closes
// the pool inline and returns the error (earlier pools are closed by the caller's
// startup-abort defer).
func provisionPGInstance(
	ctx context.Context, shared *composition.SharedDeps, locals *cmdLocals, inst pgPoolInstance,
) (capability.PGProvider, error) {
	pool, err := adapterpg.NewPool(ctx, inst.cfg)
	if err != nil {
		return nil, fmt.Errorf("corebundle: open PG pool for instance %q: %w", inst.repCell, err)
	}
	if vErr := verifyPGPreconditions(ctx, pool); vErr != nil {
		_ = pool.Close(ctx) // no leak on verify error
		return nil, vErr
	}

	// Per-pool journaling outbox writer. The topic-set argument MUST stay the direct
	// generatedProjectionSourceTopics() call (PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-
	// DERIVED-01 rejects a threaded variable, which could hide a hand-typed list).
	writer := adapterpg.NewJournalingOutboxWriter(adapterpg.NewOutboxWriter(shared.Clock), generatedProjectionSourceTopics())
	provider := capability.NewPGProvider(adapterpg.NewTxManager(pool), writer, pool.DB())

	relay, rErr := buildAssemblyRelay(shared, pool, inst.repCell)
	if rErr != nil {
		_ = pool.Close(ctx)
		return nil, rErr
	}

	locals.poolMRs = append(locals.poolMRs, pool)
	locals.relayOpts = append(locals.relayOpts, bootstrap.WithRelay(inst.key, relay))

	slog.InfoContext(ctx, "corebundle: postgres pool + relay wired",
		slog.String("instance_rep_cell", inst.repCell),
		slog.Int("served_cells", len(inst.cells)),
		slog.Int("projection_source_topic_count", len(generatedProjectionSourceTopics())))
	return provider, nil
}

// buildAssemblyRelay constructs the per-pool outbox relay that drains pool's outbox
// table and publishes via the assembly's shared Publisher. The relay is per-POOL
// assembly infrastructure (one relay per outbox table), keyed at registration by the
// pool's InfraInstanceKey. Its metric/probe label is the pool's representative cell
// (repCell) — a closed-set assembly cell id. Moved here from cellmodules/configcore
// (#2341): configcore historically built the single shared-table relay, but the relay
// belongs with the pool it drains, not a cell.
func buildAssemblyRelay(shared *composition.SharedDeps, pool *adapterpg.Pool, repCell string) (*outboxruntime.Relay, error) {
	relayCfg := outboxruntime.DefaultRelayConfig()
	relayMetrics, rmErr := outbox.NewProviderRelayCollector(shared.MetricsProvider, repCell)
	if rmErr != nil {
		return nil, fmt.Errorf("corebundle outbox relay metrics (%s): %w", repCell, rmErr)
	}
	relayCfg.Metrics = relayMetrics
	pendingDepth, pdErr := obmetrics.NewOutboxPendingDepthCollector(shared.MetricsProvider, repCell)
	if pdErr != nil {
		return nil, fmt.Errorf("corebundle pending-depth collector (%s): %w", repCell, pdErr)
	}
	store := adapterpg.NewOutboxStore(pool.DB(), shared.Clock)
	relay := outboxruntime.NewRelay(shared.Clock, store, shared.Publisher, relayCfg)
	relay.WithPendingDepthObserver(pendingDepth)
	return relay, nil
}

// verifyPGPreconditions runs the three assembly-wide PG fail-fast checks in order.
// Caller owns pool lifecycle; on error the caller must close the pool.
func verifyPGPreconditions(ctx context.Context, pool *adapterpg.Pool) error {
	if err := verifyConfigCorePGSchema(ctx, pool); err != nil {
		return err
	}
	// S3+S5: column-existence fail-fast catches partial migrations.
	if err := adapterpg.VerifyExpectedShape(ctx, pool); err != nil {
		return fmt.Errorf("corebundle PG schema shape: %w", err)
	}
	// B2-X-03: operators must DROP INVALID indexes manually before start.
	if err := adapterpg.VerifyNoInvalidIndexes(ctx, pool); err != nil {
		return fmt.Errorf("corebundle PG invalid indexes: %w", err)
	}
	return nil
}

func verifyConfigCorePGSchema(ctx context.Context, pool *adapterpg.Pool) error {
	migrationsFS, err := adapterpg.MigrationsFS()
	if err != nil {
		return fmt.Errorf("corebundle PG migrations fs: %w", err)
	}
	if err := adapterpg.VerifyExpectedVersion(ctx, pool, migrationsFS, migration.PlatformNamespace); err != nil {
		return fmt.Errorf("corebundle PG schema guard: %w", err)
	}
	return nil
}

// provisionRedis wraps the shared redis client (constructed in
// LoadSharedDepsFromEnv via buildSharedReplayDeps and held on locals.redisClient)
// into the sealed capability.RedisProvider. Nil (no-op) in modes without redis.
func provisionRedis(shared *composition.SharedDeps, locals *cmdLocals) {
	if locals.redisClient != nil {
		shared.Redis = capability.NewRedisProvider(locals.redisClient)
	}
}
