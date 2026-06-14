package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cellmodules/percellpg"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/migration"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/capability"
	"github.com/ghbvf/gocell/framework/runtime/composition"
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

// provisionPostgres opens the assembly's single serving pool from the per-cell
// DSN agreed by cellmodules/percellpg.Resolve. percellpg owns the per-cell DSN
// decision (dedup-by-DSN + fail-closed gates: empty cell set, missing DSN, >1
// distinct DSN); this site owns the actual adapter construction — pool, schema
// verification, the journaling outbox writer, and the capability.PGProvider —
// because the banned shared-infra constructors (NewPool / NewTxManager /
// NewJournalingOutboxWriter) must stay in the single sanctioned cmd/ site that
// CAPABILITY-PROVIDER-FUNNEL-01 and PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-
// DERIVED-01 cover.
//
// The topology gate runs FIRST — before reading any per-cell PG env — so memory
// mode is never blocked by an unused / malformed GOCELL_<CELL>_DATABASE_* knob.
// In memory topology shared.PG stays nil and cell modules take their in-memory path.
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

	agreed, ok, err := percellpg.Resolve(shared.Topology, percellpg.Config{
		Cells:                 cells,
		RequireRestrictedRole: true,
	})
	if err != nil {
		return err
	}
	if !ok {
		// Defensive: topology was already gated above, so postgres topology always
		// yields ok=true here. Belt-and-suspenders against a future gate drift.
		return nil
	}

	pool, err := adapterpg.NewPool(ctx, agreed)
	if err != nil {
		return fmt.Errorf("corebundle: open assembly PG pool: %w", err)
	}
	if vErr := verifyPGPreconditions(ctx, pool); vErr != nil {
		_ = pool.Close(ctx) // no leak on verify error
		return vErr
	}

	// Surface the wired topic-set size so operators can distinguish "journal inactive
	// because no projections are declared" (count 0, expected today) from a wiring
	// regression — the durable journal otherwise gives no startup signal (#1504 PR-02).
	// The decorator's topic argument MUST stay the direct generatedProjectionSourceTopics()
	// call (PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01 rejects a threaded
	// variable, which could hide a hand-typed list).
	slog.InfoContext(ctx, "corebundle: journaling outbox writer wired",
		slog.Int("projection_source_topic_count", len(generatedProjectionSourceTopics())))
	writer := adapterpg.NewJournalingOutboxWriter(adapterpg.NewOutboxWriter(shared.Clock), generatedProjectionSourceTopics())

	shared.PG = capability.NewPGProvider(adapterpg.NewTxManager(pool), writer, pool.DB())
	locals.poolMR = pool
	return nil
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
