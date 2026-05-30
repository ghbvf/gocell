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
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/runtime/bootstrap"
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
		return fmt.Errorf("create journal: %w", err)
	}

	// Build and register the saga definition.
	reg, err := of.Register(ordercell.NewSagaImpl(stores))
	if err != nil {
		return fmt.Errorf("register saga: %w", err)
	}

	// Build the saga Coordinator.
	// outbox.DemoTxRunner{} is a pass-through TxRunner (no real DB).
	// outbox.NewNoopEmitter() discards outbox events (demo mode).
	coord, err := rtsaga.NewCoordinator(
		jrnl,
		outbox.DemoTxRunner{},
		outbox.NewNoopEmitter(),
		reg,
		clk,
		rtsaga.WithLogger(logger),
	)
	if err != nil {
		return fmt.Errorf("create saga coordinator: %w", err)
	}

	// Build the cell, injecting the shared journal and order repository.
	oc := ordercell.NewOrderCell(
		ordercell.WithJournal(jrnl),
		ordercell.WithOrderRepo(stores.OrderRepository()),
		ordercell.WithLogger(logger),
	)

	// Build the assembly and register the cell.
	asm := assembly.New(clk, assembly.Config{ID: assemblyID, DurabilityMode: outbox.DurabilityDemo})
	if err := asm.Register(oc); err != nil {
		return fmt.Errorf("register orderfulfillmentcell: %w", err)
	}

	// Health routes configuration: verbose disabled for demo (no token configured).
	healthOpts := []bootstrap.HealthRouteGroupOption{
		bootstrap.WithReadyzVerboseDisabled(),
	}
	if tok := os.Getenv("GOCELL_READYZ_VERBOSE_TOKEN"); tok != "" {
		healthOpts = []bootstrap.HealthRouteGroupOption{
			bootstrap.WithReadyzVerboseToken(tok),
		}
	}

	app := bootstrap.New(
		clk,
		bootstrap.WithAssembly(asm),
		// Primary listener: demo mode, no JWT required.
		bootstrap.WithListener(cell.PrimaryListener, ":8083",
			[]kauth.ListenerAuth{kauth.AuthNone{}}),
		// Internal listener: demo loopback.
		bootstrap.WithListener(cell.InternalListener, "127.0.0.1:9083",
			[]kauth.ListenerAuth{kauth.AuthNone{}}),
		// Health listener: /healthz, /readyz, /metrics.
		bootstrap.WithListener(cell.HealthListener, "127.0.0.1:9093",
			[]kauth.ListenerAuth{kauth.AuthNone{}}),
		bootstrap.WithHealthRoutes(healthOpts...),
		// Wire the saga Coordinator lifecycle via WithLifecycle.
		// Coordinator.Start blocks, so spawn in a background goroutine and
		// return immediately from OnStart.
		bootstrap.WithLifecycle(func(lc bootstrap.Lifecycle) {
			_ = lc.Append(bootstrap.Hook{
				Name: "saga-coordinator",
				OnStart: func(ownerCtx context.Context) error {
					go func() {
						if err := coord.Start(ownerCtx); err != nil {
							logger.Error("saga coordinator stopped with error",
								slog.Any("error", err))
						}
					}()
					return nil
				},
				OnStop: func(stopCtx context.Context) error {
					return coord.Stop(stopCtx)
				},
			})
		}),
	)

	logger.Info("orderfulfillment: starting on :8083 (demo mode, no auth required)")
	return app.Run(ctx)
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
			assemblyID, len(cellIDs), len(mods), hint)
	}
	for i, want := range cellIDs {
		if got := mods[i].ID(); got != want {
			return fmt.Errorf(
				"%s: assembly.yaml cells[%d]=%q ↔ modules_gen.go=%q drift; %s",
				assemblyID, i, want, got, hint)
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
