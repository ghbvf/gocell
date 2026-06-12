package dlxoutcome

import (
	"context"
	"testing"
)

// fakeRecorder records the dead-letter signal calls. It uses string reasons
// (R=string) to prove the generic constructors work on a plain string type —
// mirroring adapters/mqtt.ConsumeFailureReason, whose underlying kind is string —
// without importing the (internal) mqtt package.
type fakeRecorder struct {
	failures []string
	captures []string
}

func (r *fakeRecorder) RecordDeadLetterFailure(_ context.Context, reason string) {
	r.failures = append(r.failures, reason)
}

func (r *fakeRecorder) RecordDeadLetter(_ context.Context, reason string) {
	r.captures = append(r.captures, reason)
}

// TestDropped_RecordsFailureOnly asserts Dropped records exactly the alertable
// failure signal (RecordDeadLetterFailure) with the given reason and never the
// success signal — the drop is observable, never silently miscounted as captured.
func TestDropped_RecordsFailureOnly(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{"unmarshal", "reject"} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			rec := &fakeRecorder{}
			_ = Dropped(context.Background(), rec, reason)
			if got := len(rec.failures); got != 1 {
				t.Fatalf("Dropped: failure count = %d, want 1", got)
			}
			if rec.failures[0] != reason {
				t.Fatalf("Dropped: failure reason = %q, want %q", rec.failures[0], reason)
			}
			if got := len(rec.captures); got != 0 {
				t.Fatalf("Dropped: capture count = %d, want 0 (a drop must not be credited as captured)", got)
			}
		})
	}
}

// TestCaptured_RecordsSuccessOnly asserts Captured records exactly the success
// signal (RecordDeadLetter) with the given reason and never the failure signal.
func TestCaptured_RecordsSuccessOnly(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{"unmarshal", "reject"} {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			rec := &fakeRecorder{}
			_ = Captured(context.Background(), rec, reason)
			if got := len(rec.captures); got != 1 {
				t.Fatalf("Captured: capture count = %d, want 1", got)
			}
			if rec.captures[0] != reason {
				t.Fatalf("Captured: capture reason = %q, want %q", rec.captures[0], reason)
			}
			if got := len(rec.failures); got != 0 {
				t.Fatalf("Captured: failure count = %d, want 0", got)
			}
		})
	}
}
