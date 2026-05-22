package auditcore

import (
	"github.com/ghbvf/gocell/runtime/observability/healthz/healthztest"
)

// newTestAgg returns a shared fake healthz.Aggregator for use in cell unit tests.
// The implementation lives in healthztest to eliminate per-cell duplication.
// The concrete *healthztest.FakeAggregator is returned so callers can access
// the Probe and HasProbe methods to inspect registered probe functions by name.
func newTestAgg() *healthztest.FakeAggregator {
	return healthztest.NewFakeAggregator()
}
