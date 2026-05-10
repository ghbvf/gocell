package main

import (
	"context"
	"fmt"
	"log/slog"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	configcore "github.com/ghbvf/gocell/cells/configcore"
	configpg "github.com/ghbvf/gocell/cells/configcore/postgres"
	"github.com/ghbvf/gocell/kernel/clock"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	outboxruntime "github.com/ghbvf/gocell/runtime/outbox"
)

// ConfigCoreModuleConfig bundles the inputs for buildConfigCoreOpts so that
// callers are not affected by positional parameter ordering and new inputs can
// be added without breaking existing call sites.
//
// ref: Uber fx fx.Option — self-contained module config struct.
// ref: go-zero core/conf/config.go — validate once, pass through.
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/server.go CompletedOptions
// — sealed type threaded through Run.
type ConfigCoreModuleConfig struct {
	Topology         bootstrap.Topology
	PGConfig         adapterpg.Config
	Publisher        outbox.Publisher
	MetricsProvider  metrics.Provider
	ValueTransformer kcrypto.ValueTransformer
	OnStaleCipher    func(key, storedKeyID, currentKeyID string)
	Clock            clock.Clock
}

// ConfigCoreModuleResult bundles the outputs from buildConfigCoreOpts. Using a
// result struct rather than multiple return values allows named field access and
// makes nil-vs-empty semantics explicit at the call site.
//
// ref: Uber fx fx.Out result struct pattern.
type ConfigCoreModuleResult struct {
	// PGResource is the ManagedResource for the PG pool. Nil in memory mode.
	PGResource kernellifecycle.ManagedResource
	// PGPool is the raw PG pool opened in postgres mode. Nil in memory mode.
	// ConfigCoreModule writes this to SharedDeps.SharedPGPool after a successful
	// buildConfigCoreOpts call so AccessCoreModule + AuditCoreModule can read it.
	PGPool *adapterpg.Pool
	// CellOptions are the configcore.Option values to pass to NewConfigCore.
	CellOptions []configcore.Option
	// BootstrapOpts are bootstrap.Option values — in postgres mode carries
	// WithManagedResource(relay) so the relay worker is independently managed.
	BootstrapOpts []bootstrap.Option
}

// buildConfigCoreOpts selects storage-adapter options for configcore based on
// the already-resolved Topology. Returns a ConfigCoreModuleResult and an error.
//
// cfg.PGConfig must be built by the caller (via LoadPGConfig) and passed
// explicitly; buildConfigCoreOpts no longer reads environment variables directly.
// In postgres mode, cfg.PGConfig.DSN must be non-empty; an empty DSN causes a
// fail-fast error with a message naming GOCELL_CONFIGCORE_DATABASE_URL.
//
// Relay worker is registered via BootstrapOpts independently of PGResource:
// PGResource owns the pool health probe and pool shutdown (no background worker);
// the relay is a separate ManagedResource with its own Worker/Close/Checkers.
//
// ref: Kratos wire — adapter selected at assembly init time, not run time.
// ref: uber-go/fx lifecycle — external resources hook via ManagedResource.
func buildConfigCoreOpts(ctx context.Context, cfg ConfigCoreModuleConfig) (ConfigCoreModuleResult, error) {
	switch cfg.Topology.StorageBackend {
	case "postgres":
		if cfg.PGConfig.DSN == "" {
			return ConfigCoreModuleResult{}, fmt.Errorf("configcore postgres mode requires GOCELL_CONFIGCORE_DATABASE_URL")
		}
		pool, err := adapterpg.NewPool(ctx, cfg.PGConfig)
		if err != nil {
			return ConfigCoreModuleResult{}, fmt.Errorf("configcore PG pool: %w", err)
		}
		// A12: fail-fast on schema version mismatch.
		if schemaErr := verifyConfigCorePGSchema(ctx, pool); schemaErr != nil {
			_ = pool.Close(ctx)
			return ConfigCoreModuleResult{}, schemaErr
		}
		// S3+S5: fail-fast on schema shape (column existence post-migration).
		// Catches partial migrations where the version table reports N but
		// the migration's DDL never reached the column (e.g. sessions.jti
		// missing while sessions.access_token still present).
		if shapeErr := adapterpg.VerifyExpectedShape(ctx, pool); shapeErr != nil {
			_ = pool.Close(ctx)
			return ConfigCoreModuleResult{}, fmt.Errorf("configcore PG schema shape: %w", shapeErr)
		}
		// B2-X-03: fail-fast on INVALID indexes (replaces prior warn-continue).
		// Operators must DROP INDEX manually before the binary will start —
		// silent continue can hide INSERT-time failures in tests / staging.
		if idxErr := adapterpg.VerifyNoInvalidIndexes(ctx, pool); idxErr != nil {
			_ = pool.Close(ctx)
			return ConfigCoreModuleResult{}, fmt.Errorf("configcore PG invalid indexes: %w", idxErr)
		}

		outboxWriter := adapterpg.NewOutboxWriter(cfg.Clock)
		txMgr := adapterpg.NewTxManager(pool)

		relayCfg := outboxruntime.DefaultRelayConfig()
		relayMetrics, rmErr := outbox.NewProviderRelayCollector(cfg.MetricsProvider, "configcore")
		if rmErr != nil {
			_ = pool.Close(ctx)
			return ConfigCoreModuleResult{}, fmt.Errorf("configcore outbox relay metrics: %w", rmErr)
		}
		relayCfg.Metrics = relayMetrics
		relayCfg.Clock = cfg.Clock
		pgStore := adapterpg.NewOutboxStore(pool.DB(), cfg.Clock)
		relayWorker := outboxruntime.NewRelay(pgStore, cfg.Publisher, relayCfg)

		pgRes, storageOpt, storageErr := buildConfigCorePGStorage(pool, cfg)
		if storageErr != nil {
			_ = pool.Close(ctx)
			return ConfigCoreModuleResult{}, storageErr
		}
		slog.Info("configcore: using PostgreSQL storage", slog.String("cell_adapter_mode", cfg.Topology.StorageBackend))
		cellOpts := []configcore.Option{
			storageOpt,
			// PG adapter path: publisher + real outbox.Writer compose a
			// WriterEmitter at Cell boundary; L2 transactional atomicity applies.
			configcore.WithOutboxDeps(cfg.Publisher, outboxWriter),
			configcore.WithTxManager(txMgr),
		}
		// Relay is registered independently via bootstrap so its Worker()/Close()
		// lifecycle is managed separately from the pool (PGResource.Worker() == nil).
		return ConfigCoreModuleResult{
			PGResource:    pgRes,
			PGPool:        pool,
			CellOptions:   cellOpts,
			BootstrapOpts: []bootstrap.Option{bootstrap.WithManagedResource(relayWorker)},
		}, nil

	case "memory":
		slog.Info("configcore: using in-memory storage", slog.String("cell_adapter_mode", cfg.Topology.StorageBackend))
		return ConfigCoreModuleResult{
			CellOptions: []configcore.Option{
				configcore.WithInMemoryDefaults(),
				// Memory adapter path: publisher only, writer=nil → DirectEmitter.
				configcore.WithOutboxDeps(cfg.Publisher, nil),
			},
		}, nil

	default:
		// Unreachable: TopologyFromEnv validation already rejects unknown
		// StorageBackend values. Keep as defense-in-depth only.
		return ConfigCoreModuleResult{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"buildConfigCoreOpts: unexpected StorageBackend (topology validation bypass)",
			errcode.WithInternal(fmt.Sprintf("backend=%q", cfg.Topology.StorageBackend)))
	}
}

func verifyConfigCorePGSchema(ctx context.Context, pool *adapterpg.Pool) error {
	migrationsFS, err := adapterpg.MigrationsFS()
	if err != nil {
		return fmt.Errorf("configcore PG migrations fs: %w", err)
	}
	if err := adapterpg.VerifyExpectedVersion(ctx, pool, migrationsFS); err != nil {
		return fmt.Errorf("configcore PG schema guard: %w", err)
	}
	// N8: lease invariant is enforced by the
	// `outbox_claiming_requires_lease` CHECK constraint on outbox_entries
	// (migration 015). The startup probe was removed — DB CHECK is the
	// single source of truth.
	return nil
}

func buildConfigCorePGStorage(
	pool *adapterpg.Pool, cfg ConfigCoreModuleConfig,
) (kernellifecycle.ManagedResource, configcore.Option, error) {
	pgRes, err := adapterpg.NewPGResource(pool)
	if err != nil {
		return nil, nil, fmt.Errorf("configcore PG resource: %w", err)
	}
	storageOpt, err := configpg.WithPool(pool.DB(), cfg.Clock,
		configpg.WithValueTransformer(cfg.ValueTransformer),
		configpg.WithOnStaleCipher(cfg.OnStaleCipher),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("configcore PG repository wiring: %w", err)
	}
	return pgRes, storageOpt, nil
}
