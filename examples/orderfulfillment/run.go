// run.go is the hand-written runtime half behind the generated assembly
// entrypoint for orderfulfillment. The generated main.go owns the assembly ID
// and cell order; this file owns environment loading and runtime option wiring.
//
// Demo mode uses in-memory stores and a MemJournal. The saga Coordinator is
// wired directly and started via bootstrap.WithLifecycle.
//
// Usage:
//
//	go run ./examples/orderfulfillment
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	ordercell "github.com/ghbvf/gocell/examples/orderfulfillment/cells/orderfulfillmentcell"
	of "github.com/ghbvf/gocell/generated/contracts/saga/orderfulfillment/v1"
	"github.com/ghbvf/gocell/kernel/assembly"
	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
	rtsaga "github.com/ghbvf/gocell/runtime/saga"
)

// runOrderfulfillment is the hand-written runtime helper for the orderfulfillment
// assembly. It is called by the generated main.go and owns environment loading +
// bootstrap wiring.
func runOrderfulfillment(ctx context.Context, assemblyID string, assemblyCellIDs []string) error {
	mods, err := runOrderfulfillmentModules(assemblyID, assemblyCellIDs)
	if err != nil {
		return err
	}
	_ = mods // cell construction is done directly below; mods only validates drift

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	clk := clock.Real()

	coord, oc, err := buildSagaComponents(clk, assemblyID, logger)
	if err != nil {
		return err
	}

	// Build the assembly and register the cell.
	asm := assembly.New(clk, assembly.Config{ID: assemblyID, DurabilityMode: outbox.DurabilityDemo})
	if err := asm.Register(oc); err != nil {
		return fmt.Errorf("register orderfulfillmentcell: %w", err)
	}

	// Capture lifecycle.Append errors so they bubble to runOrderfulfillment.
	// WithLifecycle callback has no error return (thorough fix = error-first API;
	// tracked in gh #1392). Closure variable is read before app.Run.
	var lifecycleAppendErr error
	app := bootstrap.New(
		clk,
		bootstrap.WithAssembly(asm),
		// Primary listener: demo mode, no JWT required.
		// Bound to loopback only — demo mode must not expose to untrusted networks.
		bootstrap.WithListener(cell.PrimaryListener, "127.0.0.1:8083",
			[]kauth.ListenerAuth{kauth.AuthNone{}}),
		// Health listener: /healthz, /readyz. Demo mode wires no /metrics
		// handler (saga metrics use NopProvider), so the framework /metrics
		// route is not registered — see WithHealthRoutes(demoHealthOpts()) below
		// (no bootstrap.WithMetricsHandler).
		bootstrap.WithListener(cell.HealthListener, "127.0.0.1:9093",
			[]kauth.ListenerAuth{kauth.AuthNone{}}),
		bootstrap.WithHealthRoutes(demoHealthOpts()...),
		bootstrap.WithLifecycle(buildSagaLifecycle(coord, assemblyID, logger, &lifecycleAppendErr)),
	)
	if lifecycleAppendErr != nil {
		return fmt.Errorf("orderfulfillment: register saga-coordinator lifecycle hook: %w", lifecycleAppendErr)
	}

	logger.Warn("orderfulfillment: demo mode — unauthenticated + in-memory journal + discarded outbox events + discarded saga metrics")
	logger.Info("orderfulfillment: starting on 127.0.0.1:8083 (demo mode, loopback only)")
	return app.Run(ctx)
}

// buildSagaComponents constructs the in-memory stores, MemJournal, saga
// coordinator, and orderfulfillment cell for demo mode.
func buildSagaComponents(clk clock.Clock, assemblyID string, logger *slog.Logger) (*rtsaga.Coordinator, *ordercell.OrderCell, error) {
	// Build in-memory stores. Single DemoStores instance is shared across the
	// cell (placeorder service) and the saga coordinator (Impl), so orders
	// created via HTTP are visible to saga steps.
	stores := ordercell.NewDemoStores(map[string]int{
		"widget": 100,
		"gadget": 100,
	})

	// Shared MemJournal — injected into both the placeorder service (for
	// enrollment) and the Coordinator (for execution).
	jrnl, err := journal.NewMemJournal(clk)
	if err != nil {
		return nil, nil, fmt.Errorf("create journal: %w", err)
	}

	// Build and register the saga definition.
	sagaImpl, err := ordercell.NewSagaImpl(stores)
	if err != nil {
		return nil, nil, fmt.Errorf("build saga impl: %w", err)
	}
	reg, err := of.Register(sagaImpl)
	if err != nil {
		return nil, nil, fmt.Errorf("register saga: %w", err)
	}

	// Build the saga step metrics observer.
	// Demo mode uses NopProvider (discards metrics) — production wiring would
	// pass a real Prometheus provider from bootstrap.MetricsProvider().
	sagaObs, err := obmetrics.NewSagaStepCollector(kernelmetrics.NopProvider{}, "orderfulfillmentcell")
	if err != nil {
		return nil, nil, fmt.Errorf("create saga step collector: %w", err)
	}

	// Build the saga Coordinator.
	// Production: replace DemoTxRunner with a real persistence.TxRunner and NewNoopEmitter with a real outbox.Emitter (wired via bootstrap).
	// outbox.DemoTxRunner{} is a pass-through TxRunner (no real DB).
	// outbox.NewNoopEmitter() discards outbox events (demo mode).
	coord, err := rtsaga.NewCoordinator(
		jrnl,
		outbox.DemoTxRunner{},
		outbox.NewNoopEmitter(),
		reg,
		clk,
		rtsaga.WithLogger(logger),
		rtsaga.WithObserver(sagaObs),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create saga coordinator: %w", err)
	}

	// Build the cell, injecting the shared journal, order repository, and coordinator
	// for /readyz health reporting.
	oc := ordercell.NewOrderCell(
		clk,
		ordercell.WithJournal(jrnl),
		ordercell.WithOrderRepo(stores.OrderRepository()),
		ordercell.WithLogger(logger),
		ordercell.WithCoordinator(coord),
	)
	_ = assemblyID // reserved for future per-assembly labeling
	return coord, oc, nil
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
