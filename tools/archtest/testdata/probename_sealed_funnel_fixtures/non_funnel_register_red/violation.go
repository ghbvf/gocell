// Package non_funnel_register_red is a synthetic violation fixture for
// PROBENAME-SEALED-FUNNEL-01/A3.
//
// It calls healthz.Aggregator.Register directly from a file that is NOT in
// the sanctioned allowlist (runtime/bootstrap, kernel/cell, or the healthztest
// conformance helper). The archtest scanner must detect this as an A3 violation.
//
// DO NOT use this package in production code.
package non_funnel_register_red

import (
	"context"

	"github.com/ghbvf/gocell/kernel/healthz"
)

// fakeAggregator is a minimal healthz.Aggregator implementation used to
// provide a type-checkable receiver for the A3 violation call.
type fakeAggregator struct{}

func (f *fakeAggregator) Register(p healthz.Probe) error              { return nil }
func (f *fakeAggregator) Deregister(name healthz.ProbeName)           {}
func (f *fakeAggregator) Evaluate(_ context.Context) healthz.Snapshot { return healthz.Snapshot{} }

// registerDirectly demonstrates the A3 violation: calling
// healthz.Aggregator.Register from a file that is not in the sanctioned
// caller allowlist.
func registerDirectly(agg healthz.Aggregator) {
	// VIOLATION A3: Aggregator.Register called from non-allowlisted file.
	// The correct pattern is to use reg.RegisterReadiness(name, prober).
	_ = agg.Register(healthz.NewProbe(healthz.ProbeName("rogue_probe"), func(_ context.Context) error { return nil }))
}
