package outboxtest

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// Recorder is an in-memory outbox.Emitter that captures every entry passed to
// Emit for later assertion.
//
// Use Recorder in cell-level unit tests as a drop-in for the production
// transactional / direct emitter when the test cares about *what* was emitted
// rather than *how* it is delivered. It is concurrency-safe — production
// Emitter implementations are invoked from multiple goroutines (transactional
// writer + direct publishers), and tests that exercise those code paths must
// not race on the recorder.
//
// Plan 044 task 1 (PR0): Recorder is the canonical producer-side seam for
// `semanticForm: producer-side` journey criteria. See
// docs/plans/202605191943-044-journey-backlog-realignment.md §2.
//
// ref: ThreeDotsLabs/watermill pubsub/gochannel — in-memory implementation
// used by upstream conformance tests for producer-side assertions.
type Recorder struct {
	mu      sync.Mutex
	entries []outbox.Entry
}

// NewRecorder returns an empty Recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// Emit captures entry. Always returns nil — Recorder does not simulate
// emit-time failures; tests that need fail-injection should compose a custom
// outbox.Emitter that wraps Recorder.
func (r *Recorder) Emit(_ context.Context, entry outbox.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, entry)
	return nil
}

// Entries returns a defensive copy of the captured entries in Emit order.
// Mutating the returned slice does not affect the Recorder's internal state.
func (r *Recorder) Entries() []outbox.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]outbox.Entry, len(r.entries))
	copy(out, r.entries)
	return out
}

// EntriesByType returns entries whose EventType equals the given value, in
// Emit order. Returns a defensive copy.
func (r *Recorder) EntriesByType(eventType string) []outbox.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]outbox.Entry, 0, len(r.entries))
	for _, e := range r.entries {
		if e.EventType == eventType {
			out = append(out, e)
		}
	}
	return out
}

// Reset clears all captured entries.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = nil
}
