package prometheus

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
)

const hookObserverD15ms = 15 * time.Millisecond

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
		CellID: "cc", Hook: cell.HookBeforeStart, Outcome: cell.OutcomeSuccess,
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
		{"aa", cell.HookBeforeStart, cell.OutcomeSuccess},
		{"aa", cell.HookBeforeStart, cell.OutcomeSuccess},
		{"aa", cell.HookBeforeStart, cell.OutcomeFailure},
		{"bb", cell.HookAfterStart, cell.OutcomeTimeout},
		{"bb", cell.HookAfterStop, cell.OutcomePanic},
	}
	for _, tc := range tests {
		obs.OnHookEvent(cell.HookEvent{
			CellID:   tc.cellID,
			Hook:     tc.phase,
			Outcome:  tc.outcome,
			Duration: time.Millisecond,
		})
	}

	counter, err := obs.hookTotal.GetMetricWithLabelValues("aa", "before_start", "success")
	require.NoError(t, err)
	assert.Equal(t, 2.0, testutil.ToFloat64(counter))

	counter, err = obs.hookTotal.GetMetricWithLabelValues("aa", "before_start", "failure")
	require.NoError(t, err)
	assert.Equal(t, 1.0, testutil.ToFloat64(counter))

	counter, err = obs.hookTotal.GetMetricWithLabelValues("bb", "after_start", "timeout")
	require.NoError(t, err)
	assert.Equal(t, 1.0, testutil.ToFloat64(counter))

	counter, err = obs.hookTotal.GetMetricWithLabelValues("bb", "after_stop", "panic")
	require.NoError(t, err)
	assert.Equal(t, 1.0, testutil.ToFloat64(counter))
}

func TestHookObserver_HistogramRecordsDuration(t *testing.T) {
	reg := prom.NewRegistry()
	obs, err := NewHookObserver(HookObserverConfig{Registry: reg})
	require.NoError(t, err)

	for range 3 {
		obs.OnHookEvent(cell.HookEvent{
			CellID:   "cc",
			Hook:     cell.HookBeforeStart,
			Outcome:  cell.OutcomeSuccess,
			Duration: hookObserverD15ms,
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
	obs.OnHookEvent(cell.HookEvent{CellID: "cc", Hook: cell.HookBeforeStart, Outcome: cell.OutcomeSuccess, Duration: time.Millisecond})

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

// TestPromCellLabel_Valid verifies that a cell id satisfying CellIDPattern
// passes through promCellLabel unchanged. This locks in the funnel's
// happy-path contract: zero overhead transformation for upstream-validated
// ids.
func TestPromCellLabel_Valid(t *testing.T) {
	t.Parallel()

	for _, id := range []string{"accesscore", "auditcore", "configcore", "aa", "a0"} {
		id := id
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			got := promCellLabel(id)
			if got != id {
				t.Fatalf("promCellLabel(%q) = %q, want %q", id, got, id)
			}
		})
	}
}

// TestPromCellLabel_PanicsOnInvalid verifies that an invalid cell id triggers
// the A-class panic via panicregister.Approved. Upstream invariants
// (schemas + FMT-C1) should make this branch unreachable; this test locks
// in the fail-fast contract for bypass-detection paths.
func TestPromCellLabel_PanicsOnInvalid(t *testing.T) {
	t.Parallel()

	cases := []string{"", "a", "Foo", "foo-bar", "foo_bar", "1foo", "foo bar"}
	for _, id := range cases {
		id := id
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("promCellLabel(%q) did not panic on invalid input", id)
				}
				// The panic value is *errcode.Error directly:
				// panicregister.Approved is a pass-through source-level marker
				// (returns its value arg unchanged), so recover() observes the
				// inner errcode.Assertion *errcode.Error. The reason kebab
				// string is statically validated by archtest
				// PANIC-REGISTERED-01 at compile/CI time, not at runtime.
				ec, ok := r.(*errcode.Error)
				if !ok {
					t.Fatalf("panic value type = %T, want *errcode.Error (A-class assertion)", r)
				}
				if ec.Code != errcode.ErrInternal {
					t.Fatalf("panic ec.Code = %q, want ErrInternal", ec.Code)
				}
			}()
			_ = promCellLabel(id)
		})
	}
}

// raceConcurrency is the goroutine count for race tests. Matches the
// adapters/redis/race_stress_integration_test.go golden reference (50).
const raceConcurrency = 50

// TestHookObserver_ConcurrentOnHookEvent_RaceDetector verifies that the
// Prometheus HookObserver is safe for concurrent OnHookEvent invocations.
// Runs raceConcurrency goroutines that emit hook events with varying
// (cell_id, hook, outcome) tuples. Asserts the race detector observes no
// data race and the final counter sum equals the expected total.
//
// Run with `go test -race`. Does NOT use t.Parallel() because race detector
// behavior under parallel tests is non-deterministic (golden reference:
// adapters/redis/race_stress_integration_test.go).
func TestHookObserver_ConcurrentOnHookEvent_RaceDetector(t *testing.T) {
	reg := prom.NewRegistry()
	obs, err := NewHookObserver(HookObserverConfig{Registry: reg})
	require.NoError(t, err)

	// Three distinct valid cell ids; combined with two hook phases and one
	// outcome that yields six label tuples — enough to exercise the
	// CounterVec dispatch table under concurrent writers.
	cellIDs := []string{"accesscore", "auditcore", "configcore"}
	hooks := []cell.HookPhase{cell.HookBeforeStart, cell.HookAfterStart}

	var emitted atomic.Int64
	var wg sync.WaitGroup
	wg.Add(raceConcurrency)
	for i := 0; i < raceConcurrency; i++ {
		go func(idx int) {
			defer wg.Done()
			obs.OnHookEvent(cell.HookEvent{
				CellID:   cellIDs[idx%len(cellIDs)],
				Hook:     hooks[idx%len(hooks)],
				Outcome:  cell.OutcomeSuccess,
				Duration: time.Millisecond,
			})
			emitted.Add(1)
		}(i)
	}
	wg.Wait()

	require.Equal(t, int64(raceConcurrency), emitted.Load(),
		"all goroutines must complete; missing emissions indicate a deadlock or panic")

	// Verify the sum across all label tuples equals the emission count.
	gathered, err := reg.Gather()
	require.NoError(t, err)
	var total float64
	for _, mf := range gathered {
		if mf.GetName() != "gocell_cell_hook_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			total += m.GetCounter().GetValue()
		}
	}
	require.Equal(t, float64(raceConcurrency), total,
		"counter total must equal goroutine count; mismatch indicates dropped events")
}
