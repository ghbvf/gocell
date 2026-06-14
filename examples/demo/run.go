// run.go is the hand-written runtime half behind the generated assembly
// entrypoint for the demo example. The generated main.go owns the assembly ID
// and cell order; this file owns the bootstrap wiring.
//
// The demo is the smallest runnable GoCell assembly: a single L1 cell (one L0
// pure-compute slice) serving GET /api/v1/hello with no external dependencies.
// There is no database, no event bus, and no saga — just two loopback listeners
// (API + health).
//
// Usage (from the repo root):
//
//	go run ./examples/demo
//	curl 127.0.0.1:8086/api/v1/hello   # {"data":{"message":"hello, gocell"}}
//	curl 127.0.0.1:9096/healthz        # {"data":{"status":"healthy"}}
package main

import (
	"context"
	"fmt"
	"log/slog"

	democell "github.com/ghbvf/gocell/examples/demo/cells/democell"
	"github.com/ghbvf/gocell/framework/kernel/assembly"
	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/http/router"
)

// listenerAddrs bundles the demo's two loopback listener addresses.
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

// demoListenerOpts wires the two demo listeners (primary API + health) by fixed
// loopback address with no authentication. The boot smoke test substitutes its
// own listener options (WithListenerNet-injected, pre-bound ephemeral sockets)
// so it can issue real HTTP requests against the bound ports.
func demoListenerOpts(addrs listenerAddrs) []bootstrap.Option {
	noAuth := []kauth.ListenerAuth{kauth.AuthNone{}}
	return []bootstrap.Option{
		// Primary listener: the hello API. Loopback only + AuthNone — the demo is
		// intentionally unauthenticated and must not be exposed to untrusted nets.
		bootstrap.WithListener(cell.PrimaryListener, addrs.primary, noAuth),
		// Health listener: /healthz, /readyz. /readyz aggregates over zero probes
		// (the demo registers none) and reports ready.
		bootstrap.WithListener(cell.HealthListener, addrs.health, noAuth),
	}
}

// runDemo is the hand-written runtime helper called by the generated main.go.
// It is called after main.go's run() seals the redacting slog default.
func runDemo(ctx context.Context, assemblyID string, assemblyCellIDs []string) error {
	addrs := defaultDemoListenerAddrs()
	app, err := buildDemoBootstrap(assemblyID, assemblyCellIDs, demoListenerOpts(addrs)...)
	if err != nil {
		return err
	}
	slog.Default().Info("demo: starting (loopback only, unauthenticated hello)",
		slog.String("primary_addr", addrs.primary),
		slog.String("health_addr", addrs.health))
	return app.Run(ctx)
}

// buildDemoBootstrap validates the assembly↔modules_gen.go drift guard, assembles
// the demo cell, and wires the bootstrap with the caller-supplied listener
// options. Taking the listeners as options lets the boot smoke test drive the
// real wiring (including the module drift guard) on pre-bound ephemeral sockets.
func buildDemoBootstrap(assemblyID string, assemblyCellIDs []string, listeners ...bootstrap.Option) (*bootstrap.Bootstrap, error) {
	if _, err := runDemoModules(assemblyID, assemblyCellIDs); err != nil {
		return nil, err
	}
	clk := clock.Real()

	asm := assembly.New(clk, assembly.Config{ID: assemblyID, DurabilityMode: outbox.DurabilityDemo})
	if err := asm.Register(democell.NewDemoCell()); err != nil {
		return nil, fmt.Errorf("register democell: %w", err)
	}

	opts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		// The primary listener is intentionally loopback-only + AuthNone with a
		// public hello route. Suppress the production-oriented "public routes on a
		// listener with no JWT verifier" WARN so the demo's first run is clean —
		// this is a hello-world, not an auth example.
		bootstrap.WithRouterOptions(router.WithSuppressNoAuthVerifierWarn()),
		bootstrap.WithHealthRoutes(bootstrap.WithReadyzVerboseDisabled()),
	}
	opts = append(opts, listeners...)
	return bootstrap.New(clk, opts...), nil
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
// the generated module list. A mismatch means the assembly entrypoint was not
// regenerated after an assembly.yaml change.
func assertModuleIDsMatch(assemblyID string, cellIDs []string, mods []CellModule) error {
	hint := fmt.Sprintf("run `go run ./cmd/gocell generate assembly --id=%s` from the repo root", assemblyID)
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
