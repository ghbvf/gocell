package testutil

import (
	"context"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// RecordingWriter is test-only and does NOT satisfy L2 durability semantics
// (no retry, no idempotency, no transactional row visibility). Production code
// must use adapters/postgres OutboxWriter.
//
// It records every Entry written to it. Tests assert against Entries to verify
// L2 OutboxFact semantics (transactional outbox row presence, EventType, payload
// shape, event_id).
//
// Set Err to simulate failure for negative-path tests; when Err is non-nil,
// Write returns Err immediately and Entries is not appended (rolling-back
// callers must observe the error propagating from RunInTx).
type RecordingWriter struct {
	Entries []outbox.Entry
	Err     error
}

// Write implements outbox.Writer.
func (w *RecordingWriter) Write(_ context.Context, entry outbox.Entry) error {
	if w.Err != nil {
		return w.Err
	}
	w.Entries = append(w.Entries, entry)
	return nil
}

var _ outbox.Writer = (*RecordingWriter)(nil)
