package accesscore

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/observability/healthz/healthztest"
)

// newTestAgg returns a shared fake healthz.Aggregator for use in cell unit tests.
// The implementation lives in healthztest to eliminate per-cell duplication.
// The concrete *healthztest.FakeAggregator is returned so callers can access
// the Probe method to inspect registered probe functions by name.
func newTestAgg() *healthztest.FakeAggregator {
	return healthztest.NewFakeAggregator()
}

// drainProbeSnapshot mirrors the bootstrap layer: it drains the probes a cell
// accumulated during Init (RegistrySnapshot.Probes) into a FakeAggregator so
// tests can assert probe registration via HasProbe / Probe. Call it after Init.
func drainProbeSnapshot(t *testing.T, rec *cell.RegistryRecorder) *healthztest.FakeAggregator {
	t.Helper()
	agg := newTestAgg()
	for _, p := range rec.Snapshot().Probes {
		require.NoError(t, agg.Register(p))
	}
	return agg
}
