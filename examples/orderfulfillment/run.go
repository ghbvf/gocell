// run.go is the hand-written runtime half behind the generated assembly
// entrypoint for orderfulfillment. The generated main.go owns the assembly ID
// and cell order; this file owns environment loading and runtime option wiring.
//
// Topology-gated operation:
//   - demo/memory (default): in-memory stores + MemJournal via sagaprojectiondeps.Resolve.
//   - postgres (GOCELL_CELL_ADAPTER_MODE=postgres): PG pool + PGJournal + PG
//     checkpoint store + PG order-status read model via sagaprojectiondeps.Resolve.
//
// The saga Coordinator is wired directly and started via bootstrap.WithLifecycle.
// The bootstrap auto-builds a Tailer for the orderstatus saga-journal projection.
//
// Usage:
//
//	go run ./examples/orderfulfillment                                   # demo mode
//	GOCELL_CELL_ADAPTER_MODE=postgres DATABASE_URL=... go run ./...     # postgres mode
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cellmodules/sagaprojectiondeps"
	ordercell "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell"
	ofmigrations "github.com/ghbvf/gocell/examples/orderfulfillment/migrations"
	of "github.com/ghbvf/gocell/generated/contracts/saga/orderfulfillment/v1"
	"github.com/ghbvf/gocell/kernel/assembly"
	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/migration"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
	rtsaga "github.com/ghbvf/gocell/runtime/saga"
)

// ofDatabaseURLEnv is the env var supplying the PostgreSQL DSN for postgres
// topology. Required when GOCELL_CELL_ADAPTER_MODE=postgres.
const ofDatabaseURLEnv = "DATABASE_URL"

// runOrderfulfillment is the hand-written runtime helper for the orderfulfillment
// assembly. It is called by the generated main.go and owns environment loading +
// bootstrap wiring. Topology is derived from environment (TopologyFromEnv).
func runOrderfulfillment(ctx context.Context, assemblyID string, assemblyCellIDs []string) error {
	mods, err := runOrderfulfillmentModules(assemblyID, assemblyCellIDs)
	if err != nil {
		return err
	}
	_ = mods // cell construction is done directly below; mods only validates drift

	// The redacting slog default is sealed by the generated main.go's run()
	// (SLOG-HANDLER-SEALED-FUNNEL-01 A3 generated segment) before this function
	// runs, so logger (and every slog.Default() call) is already scrubbed.
	logger := slog.Default()

	clk := clock.Real()

	topo, err := bootstrap.TopologyFromEnv()
	if err != nil {
		return fmt.Errorf("orderfulfillment: resolve topology: %w", err)
	}

	coord, oc, deps, bopts, err := buildSagaComponents(ctx, clk, topo, assemblyID, logger)
	if err != nil {
		return err
	}

	durability := outbox.DurabilityDemo
	if topo.StorageBackend() == bootstrap.StorageBackendPostgres {
		durability = outbox.DurabilityDurable
	}

	// Build the assembly and register the cell.
	asm := assembly.New(clk, assembly.Config{ID: assemblyID, DurabilityMode: durability})
	if err := asm.Register(oc); err != nil {
		return fmt.Errorf("register orderfulfillmentcell: %w", err)
	}

	// Capture lifecycle.Append errors so they bubble to runOrderfulfillment.
	// WithLifecycle callback has no error return (thorough fix = error-first API;
	// tracked in gh #1392). Closure variable is read before app.Run.
	var lifecycleAppendErr error
	opts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		// Primary listener: no JWT required (demo/example binary).
		// Bound to loopback only — must not expose to untrusted networks.
		bootstrap.WithListener(cell.PrimaryListener, "127.0.0.1:8083",
			[]kauth.ListenerAuth{kauth.AuthNone{}}),
		// Health listener: /healthz, /readyz.
		bootstrap.WithListener(cell.HealthListener, "127.0.0.1:9093",
			[]kauth.ListenerAuth{kauth.AuthNone{}}),
		bootstrap.WithHealthRoutes(demoHealthOpts()...),
		bootstrap.WithLifecycle(buildSagaLifecycle(coord, assemblyID, logger, &lifecycleAppendErr)),
		// Saga-journal projection bootstrap options: the bootstrap auto-builds a
		// Tailer for the orderstatus saga-journal projection (registered in cell_gen.go).
		bootstrap.WithSagaJournalReader(deps.Reader),
		bootstrap.WithSagaProjectionOwnerCheckpointStore(deps.OwnerStore),
		bootstrap.WithSagaProjectionLocker(deps.Locker),
		bootstrap.WithProjectionTxRunner(deps.TxRunner),
	}
	opts = append(opts, bopts...)

	app := bootstrap.New(clk, opts...)
	if lifecycleAppendErr != nil {
		return fmt.Errorf("orderfulfillment: register saga-coordinator lifecycle hook: %w", lifecycleAppendErr)
	}

	if topo.StorageBackend() == bootstrap.StorageBackendPostgres {
		// postgres mode is a single-pod-only test/demo durable-replay
		// demonstration — NOT production. Multi-pod postgres is fail-closed by
		// sagaprojectiondeps.Resolve (run.go injects no RedisClient, so a
		// RequiresDistributedReplay topology errors at startup rather than
		// silently granting every pod the projection leader lock).
		logger.Warn("orderfulfillment: postgres mode — single-pod loopback test/demo durable-replay " +
			"demonstration, NOT production: one unrestricted PG pool (migrations+serving), " +
			"NoopEmitter (no outbox consumers — the saga journal is the durable read source), " +
			"no leader-election (single-pod), unauthenticated loopback listener. " +
			"Production would require: durable broker emitter, split admin/serving DSN + restricted RLS role, " +
			"Redis distlock + leader-elect, JWT/PDP auth")
		logger.Info("orderfulfillment: starting on 127.0.0.1:8083 (postgres mode, single-pod loopback)")
	} else {
		logger.Warn("orderfulfillment: demo mode — unauthenticated + in-memory journal + discarded outbox events + discarded saga metrics")
		logger.Info("orderfulfillment: starting on 127.0.0.1:8083 (demo mode, loopback only)")
	}
	return app.Run(ctx)
}

// pgInfra bundles the postgres-topology infrastructure resolved by buildPostgresInfra.
type pgInfra struct {
	pool *adapterpg.Pool
	deps sagaprojectiondeps.Deps
	rm   *ordercell.PGOrderStatusReadModel
}

// buildPostgresInfra opens the PG pool, applies platform + orderfulfillment
// migrations, resolves sagaprojectiondeps, and builds the PG read model.
// topo is the already-resolved postgres topology (StorageBackend == "postgres").
// On success the caller owns pool.Close (registered via bootstrap.WithManagedResource).
// On failure the pool is closed before returning.
func buildPostgresInfra(ctx context.Context, clk clock.Clock, topo bootstrap.Topology) (pgInfra, error) {
	dsn := os.Getenv(ofDatabaseURLEnv)
	if dsn == "" {
		return pgInfra{}, fmt.Errorf("orderfulfillment: %s must be set for postgres topology", ofDatabaseURLEnv)
	}
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: dsn})
	if err != nil {
		return pgInfra{}, fmt.Errorf("orderfulfillment: open PG pool: %w", err)
	}
	if err := runOrderfulfillmentMigrations(ctx, pool); err != nil {
		_ = pool.Close(ctx)
		return pgInfra{}, err
	}
	// postgres mode is single-pod-only: no RedisClient is passed, so a multi-pod
	// (RequiresDistributedReplay) topology is fail-closed here by the resolver —
	// it refuses to hand every pod an in-process projection leader lock.
	deps, err := sagaprojectiondeps.Resolve(ctx, clk, topo, sagaprojectiondeps.Config{Pool: pool})
	if err != nil {
		_ = pool.Close(ctx)
		return pgInfra{}, fmt.Errorf("orderfulfillment: resolve saga projection deps (postgres): %w", err)
	}
	rm, err := ordercell.NewPGOrderStatusReadModel(pool)
	if err != nil {
		_ = pool.Close(ctx)
		return pgInfra{}, fmt.Errorf("orderfulfillment: build PG read model: %w", err)
	}
	return pgInfra{pool: pool, deps: deps, rm: rm}, nil
}

// runOrderfulfillmentMigrations applies platform migrations then orderfulfillment
// migrations to pool. Extracted to keep buildPostgresInfra ≤ gocognit 15.
func runOrderfulfillmentMigrations(ctx context.Context, pool *adapterpg.Pool) error {
	platformFS, err := adapterpg.MigrationsFS()
	if err != nil {
		return fmt.Errorf("orderfulfillment: get platform migrations FS: %w", err)
	}
	platMigrator, err := adapterpg.NewMigrator(pool, platformFS, migration.PlatformNamespace)
	if err != nil {
		return fmt.Errorf("orderfulfillment: create platform migrator: %w", err)
	}
	if err := platMigrator.Up(ctx); err != nil {
		return fmt.Errorf("orderfulfillment: run platform migrations: %w", err)
	}
	ofMigrator, err := adapterpg.NewMigrator(pool, ofmigrations.FS, ofmigrations.Namespace)
	if err != nil {
		return fmt.Errorf("orderfulfillment: create orderfulfillment migrator: %w", err)
	}
	if err := ofMigrator.Up(ctx); err != nil {
		return fmt.Errorf("orderfulfillment: run orderfulfillment migrations: %w", err)
	}
	return nil
}

// buildSagaComponents constructs saga-projection deps, saga coordinator, and
// orderfulfillment cell. topo determines the backend (demo/memory vs postgres).
// Returns extra bootstrap options (e.g. WithManagedResource for PG pool).
func buildSagaComponents(
	ctx context.Context,
	clk clock.Clock,
	topo bootstrap.Topology,
	assemblyID string,
	logger *slog.Logger,
) (*rtsaga.Coordinator, *ordercell.OrderCell, sagaprojectiondeps.Deps, []bootstrap.Option, error) {
	// Build in-memory stores. Single DemoStores instance is shared across the
	// cell (placeorder service) and the saga coordinator (Impl), so orders
	// created via HTTP are visible to saga steps. In postgres mode the journal
	// and projection stores are PG-backed, but order/inventory/payment/shipment
	// remain in-memory (this is a demo binary, not a production service).
	stores := ordercell.NewDemoStores(map[string]int{
		"widget": 100,
		"gadget": 100,
	})

	var deps sagaprojectiondeps.Deps
	var cellOpts []ordercell.Option
	var extraOpts []bootstrap.Option

	if topo.StorageBackend() == bootstrap.StorageBackendPostgres {
		pg, err := buildPostgresInfra(ctx, clk, topo)
		if err != nil {
			return nil, nil, sagaprojectiondeps.Deps{}, nil, err
		}
		deps = pg.deps
		cellOpts = append(cellOpts, ordercell.WithOrderStatusReadModel(pg.rm))
		// Register the pool for LIFO teardown via bootstrap (pool closes last).
		extraOpts = append(extraOpts, bootstrap.WithManagedResource(pg.pool))
	} else {
		// demo/memory path.
		memDeps, err := sagaprojectiondeps.Resolve(ctx, clk, topo, sagaprojectiondeps.Config{})
		if err != nil {
			return nil, nil, sagaprojectiondeps.Deps{}, nil, fmt.Errorf("resolve saga projection deps: %w", err)
		}
		deps = memDeps
	}

	coord, err := buildCoordinator(clk, stores, deps, logger)
	if err != nil {
		return nil, nil, sagaprojectiondeps.Deps{}, nil, err
	}

	// Build the cell, injecting the shared journal, order repository, and coordinator.
	// In postgres mode WithOrderStatusReadModel is in cellOpts; demo mode defaults
	// to in-memory MemReadModel inside initInternal.
	oc := ordercell.NewOrderCell(
		clk,
		append([]ordercell.Option{
			ordercell.WithJournal(deps.Journal),
			ordercell.WithOrderRepo(stores.OrderRepository()),
			ordercell.WithLogger(logger),
			ordercell.WithCoordinator(coord),
		}, cellOpts...)...,
	)
	_ = assemblyID // reserved for future per-assembly labeling
	return coord, oc, deps, extraOpts, nil
}

// buildCoordinator constructs the saga Coordinator from deps. Extracted to keep
// buildSagaComponents ≤ gocognit 15.
func buildCoordinator(
	clk clock.Clock,
	stores ordercell.DemoStores,
	deps sagaprojectiondeps.Deps,
	logger *slog.Logger,
) (*rtsaga.Coordinator, error) {
	sagaImpl, err := ordercell.NewSagaImpl(stores)
	if err != nil {
		return nil, fmt.Errorf("build saga impl: %w", err)
	}
	reg, err := of.Register(sagaImpl)
	if err != nil {
		return nil, fmt.Errorf("register saga: %w", err)
	}
	sagaObs, err := obmetrics.NewSagaCollector(kernelmetrics.NopProvider{}, "orderfulfillmentcell")
	if err != nil {
		return nil, fmt.Errorf("create saga collector: %w", err)
	}
	coord, err := rtsaga.NewCoordinator(
		deps.Journal,
		deps.TxRunner,
		outbox.NewNoopEmitter(),
		reg,
		clk,
		rtsaga.WithLogger(logger),
		rtsaga.WithObserver(sagaObs),
	)
	if err != nil {
		return nil, fmt.Errorf("create saga coordinator: %w", err)
	}
	return coord, nil
}

// demoHealthOpts returns health route options for demo mode: verbose readyz
// is disabled unless GOCELL_READYZ_VERBOSE_TOKEN is set in the environment.
func demoHealthOpts() []bootstrap.HealthRouteGroupOption {
	if tok := os.Getenv("GOCELL_READYZ_VERBOSE_TOKEN"); tok != "" {
		return []bootstrap.HealthRouteGroupOption{bootstrap.WithReadyzVerboseToken(tok)}
	}
	return []bootstrap.HealthRouteGroupOption{bootstrap.WithReadyzVerboseDisabled()}
}

// buildSagaLifecycle returns a WithLifecycle callback that wires the saga
// Coordinator into the bootstrap lifecycle. Errors from lc.Append are written
// to *appendErr, which the caller checks after bootstrap.New returns.
func buildSagaLifecycle(
	coord *rtsaga.Coordinator,
	assemblyID string,
	logger *slog.Logger,
	appendErr *error,
) func(bootstrap.Lifecycle) {
	return func(lc bootstrap.Lifecycle) {
		*appendErr = lc.Append(bootstrap.Hook{
			Name: "saga-coordinator",
			OnStart: func(ownerCtx context.Context) error {
				go func() {
					if err := coord.Start(ownerCtx); err != nil {
						logger.Error("saga coordinator stopped with error",
							slog.Any("error", err),
							slog.String("assembly_id", assemblyID),
							slog.String("cell_id", "orderfulfillmentcell"))
					}
				}()
				// Block until the coordinator's first tick completes (ready to
				// claim and drive saga instances), so HTTP can accept orders
				// only after the coordinator is ticking.
				select {
				case <-coord.Ready():
				case <-ownerCtx.Done():
				}
				return nil
			},
			OnStop: func(stopCtx context.Context) error {
				return coord.Stop(stopCtx)
			},
		})
	}
}

// runOrderfulfillmentModules validates that assembly.yaml cells (assemblyCellIDs)
// match the generated module list in modules_gen.go.
func runOrderfulfillmentModules(assemblyID string, cellIDs []string) ([]CellModule, error) {
	mods := generatedCellModules()
	if err := assertModuleIDsMatch(assemblyID, cellIDs, mods); err != nil {
		return nil, err
	}
	return mods, nil
}

// assertModuleIDsMatch fails-fast when assembly.yaml.cells (cellIDs) drifts
// from the generated module list.
func assertModuleIDsMatch(assemblyID string, cellIDs []string, mods []CellModule) error {
	hint := fmt.Sprintf("run `gocell generate assembly --id=%s`", assemblyID)
	if len(cellIDs) != len(mods) {
		return fmt.Errorf(
			"%s: assembly.yaml cells (%d) ↔ modules_gen.go (%d) length mismatch; %s",
			assemblyID, len(cellIDs), len(mods), hint,
		)
	}
	for i, want := range cellIDs {
		if got := mods[i].ID(); got != want {
			return fmt.Errorf(
				"%s: assembly.yaml cells[%d]=%q ↔ modules_gen.go=%q drift; %s",
				assemblyID, i, want, got, hint,
			)
		}
	}
	return nil
}

// CellModule is the modules_gen.go interface contract.
type CellModule interface {
	ID() string
}

// OrderCellModule is the stub module for orderfulfillmentcell.
type OrderCellModule struct{}

// ID returns the orderfulfillmentcell identifier.
func (OrderCellModule) ID() string { return "orderfulfillmentcell" }
