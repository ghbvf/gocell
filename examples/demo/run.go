// run.go is the hand-written runtime half behind the generated assembly
// entrypoint for the demo example. The generated main.go owns the assembly ID
// and cell order; this file owns the bootstrap wiring.
//
// The demo is the smallest runnable GoCell assembly: a single L1 cell (one L0
// pure-compute slice) serving GET /api/v1/hello with no external dependencies.
// There is no database, no event bus, and no saga — just two loopback listeners
// (API + health).
//
// Usage:
//
//	go run ./examples/demo
//	curl 127.0.0.1:8086/api/v1/hello   # {"data":{"message":"hello, gocell"}}
//	curl 127.0.0.1:9096/healthz        # ok
package main

import (
	"context"
	"fmt"
	"log/slog"

	democell "github.com/ghbvf/gocell/examples/demo/cells/democell"
	"github.com/ghbvf/gocell/kernel/assembly"
	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// listenerAddrs bundles the demo's two loopback listener addresses so the boot
// smoke test (run_smoke_test.go) can override them with ephemeral ports.
type listenerAddrs struct {
	primary string
	health  string
}

// defaultDemoListenerAddrs returns the fixed loopback addresses used by
// `go run ./examples/demo`. Loopback only — the demo is unauthenticated and
// must never bind a non-loopback interface.
func defaultDemoListenerAddrs() listenerAddrs {
	return listenerAddrs{primary: "127.0.0.1:8086", health: "127.0.0.1:9096"}
}

// runDemo is the hand-written runtime helper called by the generated main.go.
// It is called after main.go's run() seals the redacting slog default.
func runDemo(ctx context.Context, assemblyID string, assemblyCellIDs []string) error {
	if _, err := runDemoModules(assemblyID, assemblyCellIDs); err != nil {
		return err
	}
	app, err := buildDemoBootstrap(assemblyID, assemblyCellIDs, defaultDemoListenerAddrs())
	if err != nil {
		return err
	}
	slog.Default().Info("demo: starting on 127.0.0.1:8086 (loopback only, unauthenticated hello)")
	return app.Run(ctx)
}

// buildDemoBootstrap assembles the demo cell and wires the bootstrap with two
// loopback listeners (API + health). Extracted so the boot smoke test can drive
// the real wiring on ephemeral ports.
func buildDemoBootstrap(assemblyID string, _ []string, addrs listenerAddrs) (*bootstrap.Bootstrap, error) {
	clk := clock.Real()

	asm := assembly.New(clk, assembly.Config{ID: assemblyID, DurabilityMode: outbox.DurabilityDemo})
	if err := asm.Register(democell.NewDemoCell(democell.WithLogger(slog.Default()))); err != nil {
		return nil, fmt.Errorf("register democell: %w", err)
	}

	app := bootstrap.New(
		clk,
		bootstrap.WithAssembly(asm),
		// Primary listener: the hello API. Loopback only + AuthNone — the demo is
		// intentionally unauthenticated and must not be exposed to untrusted
		// networks.
		bootstrap.WithListener(cell.PrimaryListener, addrs.primary,
			[]kauth.ListenerAuth{kauth.AuthNone{}}),
		// Health listener: /healthz, /readyz. /readyz aggregates over zero probes
		// (the demo registers none) and reports ready.
		bootstrap.WithListener(cell.HealthListener, addrs.health,
			[]kauth.ListenerAuth{kauth.AuthNone{}}),
		bootstrap.WithHealthRoutes(bootstrap.WithReadyzVerboseDisabled()),
	)
	return app, nil
}

// runDemoModules validates that assembly.yaml cells (assemblyCellIDs) match the
// generated module list in modules_gen.go.
func runDemoModules(assemblyID string, cellIDs []string) ([]CellModule, error) {
	mods := generatedCellModules()
	if err := assertModuleIDsMatch(assemblyID, cellIDs, mods); err != nil {
		return nil, err
	}
	return mods, nil
}

// assertModuleIDsMatch fails-fast when assembly.yaml.cells (cellIDs) drifts from
// the generated module list. A mismatch means `gocell generate assembly
// --id=demo` was not re-run after an assembly.yaml change.
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

// CellModule is the modules_gen.go interface contract: each generated factory
// returns a value implementing ID(). The demo assembly uses a stub module value
// only for drift detection; the cell is constructed directly in
// buildDemoBootstrap.
type CellModule interface {
	ID() string
}

// DemoCellModule is the stub module for democell — returned by
// generatedCellModules() in modules_gen.go.
type DemoCellModule struct{}

// ID returns the democell identifier.
func (DemoCellModule) ID() string { return "democell" }
