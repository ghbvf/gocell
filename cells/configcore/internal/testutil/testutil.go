// Package testutil provides test doubles and helpers scoped to the configcore
// cell. It lives under internal/ to enforce the per-cell convention: other
// cells (auditcore, accesscore) that need similar test doubles should define
// their own internal/testutil rather than cross-cell-share, preserving the
// cell isolation boundary. If a truly cross-cell test utility emerges in the
// future, it should be elevated to cells/testutil/ or pkg/testutil/ — but
// that decision is deferred until the need is concrete.
package testutil

import (
	"context"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// RecordingWriter records outbox entries written to it. Set Err to simulate failures.
type RecordingWriter struct {
	Entries []outbox.Entry
	Err     error
}

func (w *RecordingWriter) Write(_ context.Context, entry outbox.Entry) error {
	if w.Err != nil {
		return w.Err
	}
	w.Entries = append(w.Entries, entry)
	return nil
}

var _ outbox.Writer = (*RecordingWriter)(nil)

// NoopTxRunner executes fn directly without a real transaction. Tracks call count.
type NoopTxRunner struct{ Calls int }

func (s *NoopTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	s.Calls++
	return fn(ctx)
}

var _ persistence.TxRunner = (*NoopTxRunner)(nil)

// StubPublisher is a no-op outbox.Publisher for testing.
type StubPublisher struct{}

func (StubPublisher) Publish(_ context.Context, _ string, _ []byte) error { return nil }
func (StubPublisher) Close(_ context.Context) error                       { return nil }

var _ outbox.Publisher = StubPublisher{}

// FailingPublisher returns Err on every Publish call.
type FailingPublisher struct{ Err error }

func (f FailingPublisher) Publish(_ context.Context, _ string, _ []byte) error { return f.Err }
func (f FailingPublisher) Close(_ context.Context) error                       { return nil }

var _ outbox.Publisher = FailingPublisher{}
