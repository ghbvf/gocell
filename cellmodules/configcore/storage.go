package configcore

import (
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	configcell "github.com/ghbvf/gocell/cells/configcore"
	configpg "github.com/ghbvf/gocell/cells/configcore/postgres"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/capability"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
	outboxruntime "github.com/ghbvf/gocell/runtime/outbox"
)

// configCoreModuleConfig bundles inputs for buildConfigCoreOpts.
type configCoreModuleConfig struct {
	topology         bootstrap.Topology
	pg               capability.PGProvider
	publisher        outbox.Publisher
	metricsProvider  kernelmetrics.Provider
	valueTransformer kcrypto.ValueTransformer
	onStaleCipher    func(key, storedKeyID, currentKeyID string)
}

// configCoreModuleResult bundles outputs from buildConfigCoreOpts.
type configCoreModuleResult struct {
	cellOptions   []configcell.Option
	bootstrapOpts []bootstrap.Option
}

// buildConfigCoreOpts selects storage-adapter options based on topology.
func buildConfigCoreOpts(clk clock.Clock, cfg configCoreModuleConfig) (configCoreModuleResult, error) {
	clock.MustHaveClock(clk, "cellmodules/configcore.buildConfigCoreOpts")
	switch cfg.topology.StorageBackend() {
	case "postgres":
		return buildConfigCorePostgresOpts(clk, cfg)

	case "memory":
		slog.Info("configcore: using in-memory storage", slog.String("cell_adapter_mode", cfg.topology.StorageBackend()))
		return configCoreModuleResult{
			cellOptions: []configcell.Option{
				configcell.WithInMemoryDefaults(),
				configcell.WithOutboxDeps(outbox.WrapPublisherForCell(cfg.publisher), nil),
			},
		}, nil

	default:
		return configCoreModuleResult{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"buildConfigCoreOpts: unexpected StorageBackend (topology validation bypass)",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("backend=%q", cfg.topology.StorageBackend()))))
	}
}

// buildConfigCorePostgresOpts builds the configcore module result for postgres.
func buildConfigCorePostgresOpts(clk clock.Clock, cfg configCoreModuleConfig) (configCoreModuleResult, error) {
	if cfg.pg == nil {
		return configCoreModuleResult{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"configcore postgres mode requires the postgres capability provider "+
				"(provisionCapabilities must run before Build)")
	}
	db, err := cellsecrets.PgxPoolFromProvider(cfg.pg)
	if err != nil {
		return configCoreModuleResult{}, fmt.Errorf("configcore: %w", err)
	}
	txMgr := cfg.pg.TxManager()
	outboxWriter := cfg.pg.OutboxWriter()

	relayWorker, rwErr := buildConfigCorePGRelay(clk, db, cfg)
	if rwErr != nil {
		return configCoreModuleResult{}, rwErr
	}

	storageOpt, storageErr := buildConfigCorePGStorage(clk, db, cfg)
	if storageErr != nil {
		return configCoreModuleResult{}, storageErr
	}
	slog.Info("configcore: using PostgreSQL storage", slog.String("cell_adapter_mode", cfg.topology.StorageBackend()))
	cellOpts := []configcell.Option{
		storageOpt,
		configcell.WithOutboxDeps(outbox.WrapPublisherForCell(cfg.publisher), outbox.WrapWriterForCell(outboxWriter)),
		configcell.WithTxManager(persistence.WrapForCell(txMgr)),
	}
	return configCoreModuleResult{
		cellOptions:   cellOpts,
		bootstrapOpts: []bootstrap.Option{bootstrap.WithRelay(relayWorker)},
	}, nil
}

// buildConfigCoreResult wires the provisional resources, relay opts, and
// key-provider managed resource.
func buildConfigCoreResult(
	c *configcell.ConfigCore,
	kp kcrypto.KeyProvider,
	modResult configCoreModuleResult,
) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource) {
	var opts []bootstrap.Option
	var provisional []kernellifecycle.ManagedResource

	opts = append(opts, modResult.bootstrapOpts...)

	if kpRes, ok := kp.(kernellifecycle.ManagedResource); ok {
		opts = append(opts, bootstrap.WithManagedResource(kpRes))
		provisional = append(provisional, kpRes)
	}
	return c, opts, provisional
}

// buildConfigCorePGRelay constructs the configcore PG relay.
func buildConfigCorePGRelay(clk clock.Clock, db *pgxpool.Pool, cfg configCoreModuleConfig) (*outboxruntime.Relay, error) {
	relayCfg := outboxruntime.DefaultRelayConfig()
	relayMetrics, rmErr := outbox.NewProviderRelayCollector(cfg.metricsProvider, "configcore")
	if rmErr != nil {
		return nil, fmt.Errorf("configcore outbox relay metrics: %w", rmErr)
	}
	relayCfg.Metrics = relayMetrics

	pendingDepth, pdErr := obmetrics.NewOutboxPendingDepthCollector(cfg.metricsProvider, "configcore")
	if pdErr != nil {
		return nil, fmt.Errorf("configcore pending-depth collector: %w", pdErr)
	}

	pgStore := adapterpg.NewOutboxStore(db, clk)
	relayWorker := outboxruntime.NewRelay(clk, pgStore, cfg.publisher, relayCfg)
	relayWorker.WithPendingDepthObserver(pendingDepth)
	return relayWorker, nil
}

func buildConfigCorePGStorage(
	clk clock.Clock, db *pgxpool.Pool, cfg configCoreModuleConfig,
) (configcell.Option, error) {
	storageOpt, err := configpg.WithPool(db, clk,
		configpg.WithValueTransformer(cfg.valueTransformer),
		configpg.WithOnStaleCipher(cfg.onStaleCipher),
	)
	if err != nil {
		return nil, fmt.Errorf("configcore PG repository wiring: %w", err)
	}
	return storageOpt, nil
}
