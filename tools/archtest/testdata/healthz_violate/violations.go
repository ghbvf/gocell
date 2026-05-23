// Package healthz_violate is a synthetic violation fixture for the
// HEALTHZ-WRITE-01 (A1/A2/A3) and HEALTHZ-TYPED-REGISTER-01 archtest rules.
// Each violation form is exercised exactly once so the reverse self-test
// (TestHealthzInvariants_ReverseFixture) can assert each rule fires.
//
// DO NOT use this package in production code.
package healthz_violate

import (
	"context"
	"net/http"

	"github.com/ghbvf/gocell/kernel/healthz"
)

// ── A1 violation: direct http.HandleFunc registration of "/healthz" ──────────
// scanHealthzA1 must detect this call.

func registerHealthzDirectly() {
	// VIOLATION A1: registers /healthz directly instead of through
	// runtime/http/health.Handler. This must trigger HEALTHZ-WRITE-01/A1.
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// ── A2 violation: healthz.Aggregator.Register called from non-allowlisted file ─
// fakeAggregator is a minimal healthz.Aggregator implementation used only
// to provide a type-checkable receiver for the A2 violation call.

type fakeAggregator struct{}

func (f *fakeAggregator) Register(p healthz.Probe) error              { return nil }
func (f *fakeAggregator) Deregister(name string)                      {}
func (f *fakeAggregator) Evaluate(_ context.Context) healthz.Snapshot { return healthz.Snapshot{} }

// registerProbeDirectly demonstrates the A2 violation: calling
// healthz.Aggregator.Register from a file that is not in the sanctioned
// caller allowlist (i.e. not runtime/bootstrap/* or healthz_gen.go).
func registerProbeDirectly(agg healthz.Aggregator) {
	// VIOLATION A2: Register called from violations.go (not in allowlist).
	_ = agg.Register(healthz.NewProbe("bad_probe", func(_ context.Context) error { return nil }))
}

// ── A3 violation: struct holding healthz.Aggregator field ─────────────────────
// rogueAggregatorHolder must be detected by scanHealthzA3 because it is not in
// the sanctioned holder allowlist.

// rogueAggregatorHolder is a struct that illegally holds a healthz.Aggregator
// field outside the sanctioned holder allowlist. This must trigger
// HEALTHZ-WRITE-01/A3.
type rogueAggregatorHolder struct {
	agg healthz.Aggregator // VIOLATION A3: this field triggers the rule
}
