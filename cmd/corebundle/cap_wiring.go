package main

import (
	"context"
	"fmt"
	"log/slog"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/migration"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/capability"
	"github.com/ghbvf/gocell/runtime/composition"
)

// provisionCapabilities is the assembly's single shared-infrastructure
// provisioning site. It is the sole sanctioned caller of the banned adapter
// constructors (adapterpg.NewPool / NewTxManager / NewOutboxWriter /
// NewJournalingOutboxWriter, adapterredis.NewClient via the factory) —
// CAPABILITY-PROVIDER-FUNNEL-01 downstream allowlist locks construction to this file.
//
// It iterates the codegen-declared generatedCapabilities() (single source:
// the derived union of the assembly cells' cell.yaml `requires`, computed in
// kernel/assembly.GenerateModulesGen), and for each declared
// capability provisions the shared resource exactly once and wraps it into the
// sealed runtime/capability provider stored on shared. Consuming cell modules receive
// the injected provider (shared.PG / shared.Redis) and never construct adapter
// primitives themselves.
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

// provisionPostgres opens the assembly's single postgres pool (postgres
// StorageBackend only), runs the schema/shape/index fail-fast checks, and wraps
// the pool-bound TxManager + OutboxWriter + raw *pgxpool.Pool handle into the
// sealed capability.PGProvider. The pool is recorded as locals.poolMR so bundle_options
// registers it as the first ManagedResource (LIFO: closed last, after every PG
// consumer — relay, cell workers, tx).
//
// In memory mode the pool is not opened and shared.PG stays nil; cell modules
// fall through to their in-memory storage path.
func provisionPostgres(ctx context.Context, shared *composition.SharedDeps, locals *cmdLocals) error {
	if shared.Topology.StorageBackend() != bootstrap.StorageBackendPostgres {
		return nil
	}
	pgCfg, err := LoadPGConfig("CONFIGCORE")
	if err != nil {
		return fmt.Errorf("assembly pg config: %w", err)
	}
	if pgCfg.DSN == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"corebundle postgres mode requires GOCELL_CONFIGCORE_DATABASE_URL")
	}
	// This is the assembly's SERVING pool. Its schema (migrations 052/053) places
	// the six tenant tables under FORCE ROW LEVEL SECURITY, which is only enforced
	// at runtime when the connecting role is neither a superuser nor BYPASSRLS.
	// Opt into the postgres_app_role_restricted_ready precondition probe so a
	// superuser-served deployment reports /readyz 503 instead of silently leaking
	// across tenants (#1676 [F-B11]). Migrations are applied by a separate admin
	// pool (tools/pg-migrate), which does not set this flag.
	pgCfg.RequireRestrictedRole = true
	pool, err := adapterpg.NewPool(ctx, pgCfg)
	if err != nil {
		return fmt.Errorf("assembly PG pool: %w", err)
	}
	// Assembly-wide schema/shape/index fail-fast before any wiring. The guard is
	// not configcore-specific: it checks the single migration set
	// (adapterpg.MigrationsFS) that owns config + session + audit tables.
	if vErr := verifyPGPreconditions(ctx, pool); vErr != nil {
		_ = pool.Close(ctx)
		return vErr
	}
	txMgr := adapterpg.NewTxManager(pool)
	// Wrap the base outbox writer so projection-source events are journaled to the
	// durable projection_events table inside the producer's transaction (EPIC #1504
	// D4). The topic set is cellgen-derived (generatedProjectionSourceTopics, I5); it
	// is empty today (corebundle declares no outbox projections), so the decorator
	// forwards writes unchanged until a projection is added — at which point its topic
	// auto-enrolls on the next `gocell generate assembly`. Wiring the durable source as
	// the projection read side lands in PR-03.
	// Surface the wired topic-set size so operators can distinguish "journal inactive
	// because no projections are declared" (count 0, expected today) from a wiring
	// regression — the durable journal otherwise gives no startup signal (#1504 PR-02).
	// The decorator's topic argument MUST stay the direct generatedProjectionSourceTopics()
	// call (PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01 rejects a threaded
	// variable, which could hide a hand-typed list); the accessor is a cheap generated
	// slice literal, so calling it again for the count is free.
	slog.InfoContext(ctx, "corebundle: journaling outbox writer wired",
		slog.Int("projection_source_topic_count", len(generatedProjectionSourceTopics())))
	writer := adapterpg.NewJournalingOutboxWriter(adapterpg.NewOutboxWriter(shared.Clock), generatedProjectionSourceTopics())
	shared.PG = capability.NewPGProvider(txMgr, writer, pool.DB())
	locals.poolMR = pool
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

// verifyPGPreconditions runs the three configcore PG fail-fast checks in
// order. Caller owns pool lifecycle; on error caller must close the pool.
func verifyPGPreconditions(ctx context.Context, pool *adapterpg.Pool) error {
	if schemaErr := verifyConfigCorePGSchema(ctx, pool); schemaErr != nil {
		return schemaErr
	}
	// S3+S5: column-existence fail-fast catches partial migrations.
	if shapeErr := adapterpg.VerifyExpectedShape(ctx, pool); shapeErr != nil {
		return fmt.Errorf("configcore PG schema shape: %w", shapeErr)
	}
	// B2-X-03: operators must DROP INVALID indexes manually before start —
	// silent continue can hide INSERT-time failures in tests / staging.
	if idxErr := adapterpg.VerifyNoInvalidIndexes(ctx, pool); idxErr != nil {
		return fmt.Errorf("configcore PG invalid indexes: %w", idxErr)
	}
	return nil
}

func verifyConfigCorePGSchema(ctx context.Context, pool *adapterpg.Pool) error {
	migrationsFS, err := adapterpg.MigrationsFS()
	if err != nil {
		return fmt.Errorf("configcore PG migrations fs: %w", err)
	}
	if err := adapterpg.VerifyExpectedVersion(ctx, pool, migrationsFS, migration.PlatformNamespace); err != nil {
		return fmt.Errorf("configcore PG schema guard: %w", err)
	}
	return nil
}
