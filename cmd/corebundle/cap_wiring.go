package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/capability"
)

// pgxPoolFromProvider is the single type-assertion site for the capability.PGProvider's
// any-typed DB() seam (runtime/capability stays adapter-free; the assertion lives here
// in the cmd/* consumer per the ADR). Cell modules call this to obtain the raw
// *pgxpool.Pool their per-cell stores need, rather than asserting themselves.
func pgxPoolFromProvider(pg capability.PGProvider) (*pgxpool.Pool, error) {
	pool, ok := pg.DB().(*pgxpool.Pool)
	if !ok {
		return nil, fmt.Errorf("corebundle: PG provider DB() is not *pgxpool.Pool (got %T)", pg.DB())
	}
	return pool, nil
}

// provisionCapabilities is the assembly's single shared-infrastructure
// provisioning site. It is the sole sanctioned caller of the banned adapter
// constructors (adapterpg.NewPool / NewTxManager / NewOutboxWriter,
// adapterredis.NewClient via the factory) — CAPABILITY-PROVIDER-FUNNEL-01
// downstream allowlist locks construction to this file.
//
// It iterates the codegen-declared generatedCapabilities() (single source:
// the derived union of the assembly cells' cell.yaml `requires`, computed in
// kernel/assembly.GenerateModulesGen), and for each declared
// capability provisions the shared resource exactly once and wraps it into the
// sealed runtime/capability provider stored on shared. Consuming cell modules receive
// the injected provider (shared.PG / shared.Redis) and never construct adapter
// primitives themselves.
//
// Called from runCorebundle after LoadSharedDepsFromEnv and before BuildApp so
// the providers are present (or fail-fast) before any module.Provide runs —
// mirroring fx.New()'s resolve-before-start ordering (ref: uber-go/fx app.go).
func provisionCapabilities(ctx context.Context, shared *SharedDeps) error {
	// INVARIANT: generatedCapabilities() exists because the corebundle assembly's
	// cells declare a non-empty `requires` union (auditcore/configcore require
	// postgres). The codegen template emits generatedCapabilities() iff that union
	// is non-empty — so this unconditional call is only safe while corebundle keeps
	// provisioning ≥1 capability. A hypothetical zero-capability corebundle would
	// not need a provisioner at all; this function would then be removed alongside.
	for _, c := range generatedCapabilities() {
		switch c {
		case capability.Postgres:
			if err := provisionPostgres(ctx, shared); err != nil {
				return err
			}
		case capability.Redis:
			provisionRedis(shared)
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
				errcode.WithInternal(fmt.Sprintf("capability=%q", string(c))))
		}
	}
	return nil
}

// provisionPostgres opens the assembly's single postgres pool (postgres
// StorageBackend only), runs the schema/shape/index fail-fast checks, and wraps
// the pool-bound TxManager + OutboxWriter + raw *pgxpool.Pool handle into the
// sealed capability.PGProvider. The pool is recorded as shared.poolMR so bundle_options
// registers it as the first ManagedResource (LIFO: closed last, after every PG
// consumer — relay, cell workers, tx).
//
// In memory mode the pool is not opened and shared.PG stays nil; cell modules
// fall through to their in-memory storage path.
func provisionPostgres(ctx context.Context, shared *SharedDeps) error {
	if shared.Topology.StorageBackend != "postgres" {
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
	writer := adapterpg.NewOutboxWriter(shared.Clock)
	shared.PG = capability.NewPGProvider(txMgr, writer, pool.DB())
	shared.poolMR = pool
	return nil
}

// provisionRedis wraps the shared redis client (constructed in
// LoadSharedDepsFromEnv via buildSharedReplayDeps and held on shared.redisClient)
// into the sealed capability.RedisProvider. The client construction itself
// (adapterredis.NewClient via the redisClientFactory) lives in redis.go; this
// step only wraps it. Nil (no-op) in modes without redis.
func provisionRedis(shared *SharedDeps) {
	if shared.redisClient != nil {
		shared.Redis = capability.NewRedisProvider(shared.redisClient)
	}
}
