package ordercell

import (
	"github.com/ghbvf/gocell/runtime/observability/healthz/healthztest"
)

// newTestAgg returns a shared fake healthz.Aggregator for use in cell unit tests.
// The implementation lives in healthztest to eliminate per-cell duplication.
func newTestAgg() *healthztest.FakeAggregator {
	return healthztest.NewFakeAggregator()
}
