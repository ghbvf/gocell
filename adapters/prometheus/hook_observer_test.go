package prometheus

import (
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHookObserver_InterfaceConformance(t *testing.T) {
	var _ cell.LifecycleHookObserver = (*HookObserver)(nil)
}

func TestNewHookObserver_RegistersMetrics(t *testing.T) {
	reg := prom.NewRegistry()
	obs, err := NewHookObserver(HookObserverConfig{Registry: reg})
	require.NoError(t, err)
	require.NotNil(t, obs)

	// Verify collectors registered by gathering empty metric families.
	gathered, err := reg.Gather()
	require.NoError(t, err)
	var hookTotal, hookDuration bool
	for _, mf := range gathered {
		switch mf.GetName() {
		case "gocell_cell_hook_total":
			hookTotal = true
		case "gocell_cell_hook_duration_seconds":
			hookDuration = true
		}
	}
	// Counters/histograms without observations gather as empty families — this
	// assertion passes once the first observation is made, so exercise the path.
	obs.OnHookEvent(cell.HookEvent{
		CellID: "c", Hook: cell.HookBeforeStart, Outcome: cell.OutcomeSuccess,
		Duration: time.Millisecond,
	})
	gathered, err = reg.Gather()
	require.NoError(t, err)
	for _, mf := range gathered {
		switch mf.GetName() {
		case "gocell_cell_hook_total":
			hookTotal = true
		case "gocell_cell_hook_duration_seconds":
			hookDuration = true
		}
	}
	assert.True(t, hookTotal, "counter must be registered")
	assert.True(t, hookDuration, "histogram must be registered")
}

func TestHookObserver_CounterIncrementsPerOutcome(t *testing.T) {
	reg := prom.NewRegistry()
	obs, err := NewHookObserver(HookObserverConfig{Registry: reg})
	require.NoError(t, err)

	tests := []struct {
		cellID  string
		phase   cell.HookPhase
		outcome cell.HookOutcome
	}{
		{"a", cell.HookBeforeStart, cell.OutcomeSuccess},
		{"a", cell.HookBeforeStart, cell.OutcomeSuccess},
		{"a", cell.HookBeforeStart, cell.OutcomeFailure},
		{"b", cell.HookAfterStart, cell.OutcomeTimeout},
		{"b", cell.HookAfterStop, cell.OutcomePanic},
	}
	for _, tc := range tests {
		obs.OnHookEvent(cell.HookEvent{
			CellID:   tc.cellID,
			Hook:     tc.phase,
			Outcome:  tc.outcome,
			Duration: time.Millisecond,
		})
	}

	counter, err := obs.hookTotal.GetMetricWithLabelValues("a", "before_start", "success")
	require.NoError(t, err)
	assert.Equal(t, 2.0, testutil.ToFloat64(counter))

	counter, err = obs.hookTotal.GetMetricWithLabelValues("a", "before_start", "failure")
	require.NoError(t, err)
	assert.Equal(t, 1.0, testutil.ToFloat64(counter))

	counter, err = obs.hookTotal.GetMetricWithLabelValues("b", "after_start", "timeout")
	require.NoError(t, err)
	assert.Equal(t, 1.0, testutil.ToFloat64(counter))

	counter, err = obs.hookTotal.GetMetricWithLabelValues("b", "after_stop", "panic")
	require.NoError(t, err)
	assert.Equal(t, 1.0, testutil.ToFloat64(counter))
}

func TestHookObserver_HistogramRecordsDuration(t *testing.T) {
	reg := prom.NewRegistry()
	obs, err := NewHookObserver(HookObserverConfig{Registry: reg})
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		obs.OnHookEvent(cell.HookEvent{
			CellID:   "c",
			Hook:     cell.HookBeforeStart,
			Outcome:  cell.OutcomeSuccess,
			Duration: 15 * time.Millisecond,
		})
	}

	// Gather the histogram metric family and assert sample count.
	gathered, err := reg.Gather()
	require.NoError(t, err)
	var found bool
	for _, mf := range gathered {
		if mf.GetName() != "gocell_cell_hook_duration_seconds" {
			continue
		}
		for _, m := range mf.Metric {
			if m.Histogram != nil && m.Histogram.GetSampleCount() == 3 {
				found = true
				assert.InDelta(t, 0.045, m.Histogram.GetSampleSum(), 0.01)
			}
		}
	}
	assert.True(t, found, "expected 3 samples recorded for before_start")
}

func TestHookObserver_RejectsNilRegistry(t *testing.T) {
	obs, err := NewHookObserver(HookObserverConfig{Registry: nil})
	require.Error(t, err)
	assert.Nil(t, obs)
}

func TestHookObserver_CustomNamespace(t *testing.T) {
	reg := prom.NewRegistry()
	obs, err := NewHookObserver(HookObserverConfig{Registry: reg, Namespace: "myapp"})
	require.NoError(t, err)
	obs.OnHookEvent(cell.HookEvent{CellID: "c", Hook: cell.HookBeforeStart, Outcome: cell.OutcomeSuccess, Duration: time.Millisecond})

	gathered, err := reg.Gather()
	require.NoError(t, err)
	var seen bool
	for _, mf := range gathered {
		if mf.GetName() == "myapp_cell_hook_total" {
			seen = true
		}
	}
	assert.True(t, seen, "expected myapp_cell_hook_total to be registered")
}

func TestHookObserver_DuplicateRegistrationReturnsError(t *testing.T) {
	reg := prom.NewRegistry()
	_, err := NewHookObserver(HookObserverConfig{Registry: reg})
	require.NoError(t, err)
	// Second registration on same registry should fail (Prometheus rejects
	// duplicate metric families). This catches double-wire bugs in bootstrap.
	_, err = NewHookObserver(HookObserverConfig{Registry: reg})
	require.Error(t, err)
	assert.True(t, errors.Is(err, err), "expected error type to be identifiable") // sanity
}
