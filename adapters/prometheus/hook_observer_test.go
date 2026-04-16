package prometheus

import (
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
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

	// Prometheus only exposes a metric family after the first observation —
	// verify registration by making one observation and then gathering.
	obs.OnHookEvent(cell.HookEvent{
		CellID: "c", Hook: cell.HookBeforeStart, Outcome: cell.OutcomeSuccess,
		Duration: time.Millisecond,
	})
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

	for range 3 {
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

// hookDurationCollector replicates the histogram name used by NewHookObserver
// so we can pre-register it on the registry and force the second Register
// step inside NewHookObserver to fail with AlreadyRegisteredError.
func preregisterHookDuration(t *testing.T, reg *prom.Registry, namespace string) {
	t.Helper()
	h := prom.NewHistogramVec(prom.HistogramOpts{
		Namespace: namespace,
		Name:      "cell_hook_duration_seconds",
		Help:      "Duration of cell lifecycle hook invocations in seconds.",
		Buckets:   DefaultHookDurationBuckets,
	}, []string{"cell_id", "hook"})
	require.NoError(t, reg.Register(h))
}

func TestHookObserver_SecondRegisterFailure_RollsBackFirst(t *testing.T) {
	reg := prom.NewRegistry()
	// Pre-register the histogram so NewHookObserver's second Register step fails.
	preregisterHookDuration(t, reg, "gocell")

	_, err := NewHookObserver(HookObserverConfig{Registry: reg})
	require.Error(t, err, "second registration must fail when histogram name is taken")

	// Proof of atomic rollback: counter must NOT be in the registry —
	// otherwise a retry on the same registry would fail with AlreadyRegistered
	// on cell_hook_total, trapping the caller in a half-registered state.
	gathered, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range gathered {
		assert.NotEqual(t, "gocell_cell_hook_total", mf.GetName(),
			"cell_hook_total must be unregistered after second-step failure")
	}
}

func TestHookObserver_DuplicateRegistrationReturnsError(t *testing.T) {
	reg := prom.NewRegistry()
	_, err := NewHookObserver(HookObserverConfig{Registry: reg})
	require.NoError(t, err)
	// Second registration on same registry should fail (Prometheus rejects
	// duplicate metric families). This catches double-wire bugs in bootstrap.
	_, err = NewHookObserver(HookObserverConfig{Registry: reg})
	require.Error(t, err)
	// Verify errcode.Wrap preserved the adapter code so callers can route
	// registration errors separately from config errors.
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "expected errcode.Error in chain")
	assert.Equal(t, ErrAdapterPromRegister, ec.Code)
}
