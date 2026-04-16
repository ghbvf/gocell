package assembly

import (
	"context"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureObserver records every HookEvent for assertion.
type captureObserver struct {
	mu     sync.Mutex
	events []cell.HookEvent
}

func (o *captureObserver) OnHookEvent(e cell.HookEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, e)
}

func (o *captureObserver) snapshot() []cell.HookEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]cell.HookEvent, len(o.events))
	copy(out, o.events)
	return out
}

// summarize returns a compact (cellID, hook, outcome) tuple per event for
// order/identity assertions without coupling tests to durations.
func summarize(events []cell.HookEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.CellID + "." + string(e.Hook) + "=" + string(e.Outcome)
	}
	return out
}

func TestObserver_HappyPath_SuccessOutcomes(t *testing.T) {
	obs := &captureObserver{}
	a := New(Config{
		ID:             "obs-happy",
		DurabilityMode: cell.DurabilityDemo,
		HookObserver:   obs,
	})
	var calls []string
	require.NoError(t, a.Register(newHookOrderCell("A", &calls, "")))
	require.NoError(t, a.Register(newHookOrderCell("B", &calls, "")))

	require.NoError(t, a.Start(context.Background()))
	require.NoError(t, a.Stop(context.Background()))

	// 2 cells × 4 hooks = 8 events total (A BeforeStart/AfterStart,
	// B BeforeStart/AfterStart, B BeforeStop/AfterStop, A BeforeStop/AfterStop).
	got := summarize(obs.snapshot())
	want := []string{
		"A.before_start=success",
		"A.after_start=success",
		"B.before_start=success",
		"B.after_start=success",
		"B.before_stop=success",
		"B.after_stop=success",
		"A.before_stop=success",
		"A.after_stop=success",
	}
	assert.Equal(t, want, got)
	// Every event must record a positive duration — a zero value would hide
	// a bug where invokeHook skipped time.Since(start).
	for _, e := range obs.snapshot() {
		assert.Positive(t, e.Duration.Nanoseconds(), "duration should be recorded for %s.%s", e.CellID, e.Hook)
	}
}

func TestObserver_FailureOutcome(t *testing.T) {
	tests := []struct {
		name   string
		failOn string
		phase  cell.HookPhase
	}{
		{"before_start failure", "BeforeStart", cell.HookBeforeStart},
		{"after_start failure", "AfterStart", cell.HookAfterStart},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obs := &captureObserver{}
			a := New(Config{ID: "obs-fail", DurabilityMode: cell.DurabilityDemo, HookObserver: obs})
			var calls []string
			require.NoError(t, a.Register(newHookOrderCell("X", &calls, tc.failOn)))

			err := a.Start(context.Background())
			require.Error(t, err)

			events := obs.snapshot()
			// Find the failure event for the targeted phase.
			var found bool
			for _, e := range events {
				if e.CellID == "X" && e.Hook == tc.phase {
					assert.Equal(t, cell.OutcomeFailure, e.Outcome)
					require.Error(t, e.Err)
					found = true
				}
			}
			assert.True(t, found, "expected failure event for %s", tc.phase)
		})
	}
}

func TestObserver_PanicOutcome(t *testing.T) {
	tests := []struct {
		name    string
		panicOn string
		phase   cell.HookPhase
	}{
		{"before_start panic", "BeforeStart", cell.HookBeforeStart},
		{"after_start panic", "AfterStart", cell.HookAfterStart},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obs := &captureObserver{}
			a := New(Config{ID: "obs-panic", DurabilityMode: cell.DurabilityDemo, HookObserver: obs})
			var calls []string
			require.NoError(t, a.Register(newPanicHookCell("P", &calls, tc.panicOn)))

			err := a.Start(context.Background())
			require.Error(t, err)

			var panicked bool
			for _, e := range obs.snapshot() {
				if e.CellID == "P" && e.Hook == tc.phase {
					assert.Equal(t, cell.OutcomePanic, e.Outcome)
					require.Error(t, e.Err)
					assert.Contains(t, e.Err.Error(), "panic")
					panicked = true
				}
			}
			assert.True(t, panicked, "expected panic event for %s", tc.phase)
		})
	}
}

func TestObserver_StopPhasePanic(t *testing.T) {
	obs := &captureObserver{}
	a := New(Config{ID: "obs-stop-panic", DurabilityMode: cell.DurabilityDemo, HookObserver: obs})
	var calls []string
	require.NoError(t, a.Register(newPanicHookCell("P", &calls, "AfterStop")))
	require.NoError(t, a.Start(context.Background()))

	// Stop — AfterStop panics but Stop must continue (best-effort).
	err := a.Stop(context.Background())
	require.Error(t, err)

	var afterStopSeen bool
	for _, e := range obs.snapshot() {
		if e.CellID == "P" && e.Hook == cell.HookAfterStop {
			assert.Equal(t, cell.OutcomePanic, e.Outcome)
			afterStopSeen = true
		}
	}
	assert.True(t, afterStopSeen, "expected after_stop panic event")
}

func TestObserver_NilDefaultsToNop(t *testing.T) {
	// Nil observer must not panic; Config zero-value is valid.
	a := New(Config{
		ID:             "obs-nil",
		DurabilityMode: cell.DurabilityDemo,
		// HookObserver: nil,
	})
	var calls []string
	require.NoError(t, a.Register(newHookOrderCell("A", &calls, "")))
	require.NoError(t, a.Start(context.Background()))
	require.NoError(t, a.Stop(context.Background()))
}

// badObserver panics on every call — simulates a buggy observer.
type badObserver struct{}

func (badObserver) OnHookEvent(cell.HookEvent) {
	panic("observer sink crashed")
}

func TestObserver_PanicInSink_IsIsolated(t *testing.T) {
	// A panicking observer must not crash the assembly lifecycle.
	a := New(Config{ID: "obs-bad", DurabilityMode: cell.DurabilityDemo, HookObserver: badObserver{}})
	var calls []string
	require.NoError(t, a.Register(newHookOrderCell("A", &calls, "")))
	require.NoError(t, a.Start(context.Background()))
	require.NoError(t, a.Stop(context.Background()))
}

// Compile-time interface conformance check.
var _ cell.LifecycleHookObserver = (*captureObserver)(nil)
var _ cell.LifecycleHookObserver = badObserver{}
