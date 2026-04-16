package cell

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestHookPhaseConstants(t *testing.T) {
	tests := []struct {
		name  string
		phase HookPhase
		want  string
	}{
		{"before_start", HookBeforeStart, "before_start"},
		{"after_start", HookAfterStart, "after_start"},
		{"before_stop", HookBeforeStop, "before_stop"},
		{"after_stop", HookAfterStop, "after_stop"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, string(tc.phase))
		})
	}
}

func TestHookOutcomeConstants(t *testing.T) {
	tests := []struct {
		name    string
		outcome HookOutcome
		want    string
	}{
		{"success", OutcomeSuccess, "success"},
		{"failure", OutcomeFailure, "failure"},
		{"timeout", OutcomeTimeout, "timeout"},
		{"panic", OutcomePanic, "panic"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, string(tc.outcome))
		})
	}
}

func TestHookEventFields(t *testing.T) {
	err := errors.New("boom")
	evt := HookEvent{
		CellID:   "access-core",
		Hook:     HookBeforeStart,
		Outcome:  OutcomeFailure,
		Duration: 42 * time.Millisecond,
		Err:      err,
	}
	assert.Equal(t, "access-core", evt.CellID)
	assert.Equal(t, HookBeforeStart, evt.Hook)
	assert.Equal(t, OutcomeFailure, evt.Outcome)
	assert.Equal(t, 42*time.Millisecond, evt.Duration)
	assert.Same(t, err, evt.Err)
}

// recordingObserver captures every event for assertion.
type recordingObserver struct {
	events []HookEvent
}

func (r *recordingObserver) OnHookEvent(e HookEvent) {
	r.events = append(r.events, e)
}

// compile-time interface check.
var _ LifecycleHookObserver = (*recordingObserver)(nil)
var _ LifecycleHookObserver = (*NopHookObserver)(nil)
var _ LifecycleHookObserver = NopHookObserver{}

func TestNopHookObserver_DoesNotPanic(t *testing.T) {
	// NopHookObserver must accept any HookEvent without panicking or doing work.
	var obs NopHookObserver
	assert.NotPanics(t, func() {
		obs.OnHookEvent(HookEvent{})
		obs.OnHookEvent(HookEvent{
			CellID:   "x",
			Hook:     HookAfterStop,
			Outcome:  OutcomePanic,
			Duration: time.Second,
			Err:      errors.New("ignored"),
		})
	})
}

func TestLifecycleHookObserver_CustomImpl(t *testing.T) {
	obs := &recordingObserver{}
	obs.OnHookEvent(HookEvent{CellID: "a", Hook: HookBeforeStart, Outcome: OutcomeSuccess})
	obs.OnHookEvent(HookEvent{CellID: "a", Hook: HookAfterStart, Outcome: OutcomeSuccess})
	assert.Len(t, obs.events, 2)
	assert.Equal(t, HookBeforeStart, obs.events[0].Hook)
	assert.Equal(t, HookAfterStart, obs.events[1].Hook)
}
