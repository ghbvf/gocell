package configcore

import (
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	configcell "github.com/ghbvf/gocell/corecells/configcore"
	configpg "github.com/ghbvf/gocell/corecells/configcore/postgres"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	kcrypto "github.com/ghbvf/gocell/framework/kernel/crypto"
	kernellifecycle "github.com/ghbvf/gocell/framework/kernel/lifecycle"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/capability"
)

// configCoreModuleConfig bundles inputs for buildConfigCoreOpts.
type configCoreModuleConfig struct {
	topology         bootstrap.Topology
	pg               capability.PGProvider
	publisher        outbox.Publisher
	valueTransformer kcrypto.ValueTransformer
	onStaleCipher    func(key, storedKeyID, currentKeyID string)
}

// configCoreModuleResult bundles outputs from buildConfigCoreOpts.
type configCoreModuleResult struct {
	cellOptions []configcell.Option
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
//
// The outbox relay is NOT built here (#2341): it is per-POOL assembly
// infrastructure owned by the composition root (cmd/corebundle/cap_wiring.go),
// which drives one relay per pool keyed by the pool's InfraInstanceKey. configcore
// only wires its cell-level storage + outbox deps; it contributes no bootstrap opts
// (RELAY-CONSTRUCTION-CELLMODULE-BAN-01).
func buildConfigCorePostgresOpts(clk clock.Clock, cfg configCoreModuleConfig) (configCoreModuleResult, error) {
	if cfg.pg == nil {
		return configCoreModuleResult{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"configcore postgres mode requires the postgres capability provider "+
				"(the composition root must provision the postgres capability on SharedDeps before composition.Build)")
	}
	db, err := cellsecrets.PgxPoolFromProvider(cfg.pg)
	if err != nil {
		return configCoreModuleResult{}, fmt.Errorf("configcore: %w", err)
	}
	txMgr := cfg.pg.TxManager()
	outboxWriter := cfg.pg.OutboxWriter()

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
	return configCoreModuleResult{cellOptions: cellOpts}, nil
}

// buildConfigCoreResult assembles the configcore module result: the cell, the
// non-resource bootstrap opts (always nil — relay moved to composition root,
// #2341, RELAY-CONSTRUCTION-CELLMODULE-BAN-01), and the single-source
// ManagedResource list. When the KeyProvider is itself a ManagedResource
// (vault-transit) it is returned ONLY in the resources slice — Builder.Build
// derives both the steady-state bootstrap.WithManagedResource registration and the
// pre-Run rollback from it. This function must NOT call
// bootstrap.WithManagedResource (banned in cellmodules/ by
// WITHMANAGEDRESOURCE-CELLMODULE-FUNNEL-01).
//
//nolint:unparam // R2-approved: opts always nil post-#2341; signature kept for call-site symmetry
func buildConfigCoreResult(
	c *configcell.ConfigCore,
	kp kcrypto.KeyProvider,
	_ configCoreModuleResult,
) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource) {
	var resources []kernellifecycle.ManagedResource
	if kpRes, ok := kp.(kernellifecycle.ManagedResource); ok {
		resources = append(resources, kpRes)
	}
	return c, nil, resources
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
