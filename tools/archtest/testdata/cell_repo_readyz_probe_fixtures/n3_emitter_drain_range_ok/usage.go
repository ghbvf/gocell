// Package n3_emitter_drain_range_ok exercises the N2 GREEN reverse fixture for
// CELL-REPO-READYZ-PROBE-01: the emitter-drain pattern reg.Health(k, v) where
// k and v are range-loop variables (not a *ast.BasicLit string). The rule must
// NOT flag this — it is the legitimate emitter health drain.
package n3_emitter_drain_range_ok

import (
	"context"

	"github.com/ghbvf/gocell/kernel/cell"
)

// drainEmitter demonstrates the legitimate emitter health drain pattern:
// iterating Probes() and forwarding each entry to reg.Health. The first
// argument (k) is an *ast.Ident (range variable), not a *ast.BasicLit.
// N2 must NOT flag this.
func drainEmitter(reg cell.Registry, hc cell.HealthProber) {
	for k, v := range hc.Probes() {
		reg.Health(k, v)
	}
}

// also verify that a type-assertion to cell.HealthProber (named interface)
// does NOT trigger N1.
func checkHealthProber(v any) {
	if _, ok := v.(cell.HealthProber); ok {
		// named interface assertion — must not be flagged
	}
}

// dummyProber is a trivial HealthProber so the package compiles.
type dummyProber struct{}

func (d *dummyProber) Probes() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"probe_ready": func(_ context.Context) error { return nil },
	}
}
