package sagaprojection_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/kernel/saga/sagajournaltest"
	"github.com/ghbvf/gocell/kernel/saga/sagaprojection"
)

// failingGlobalReader implements journal.GlobalReader and always returns sentinel
// on both methods — used to exercise SagaJournalSource's error-propagation paths.
type failingGlobalReader struct{ sentinel error }

func (f failingGlobalReader) LoadSince(_ context.Context, _ int64, _ int) ([]journal.GlobalEvent, error) {
	return nil, f.sentinel
}

func (f failingGlobalReader) HeadSeq(_ context.Context) (int64, error) {
	return 0, f.sentinel
}

// foreignProjectionEvent is a non-saga ProjectionEvent carrier used to exercise
// SagaJournalSource.Position's wrong-carrier-type rejection branch.
type foreignProjectionEvent struct{}

func (foreignProjectionEvent) EventID() string                                    { return "foreign-carrier" }
func (foreignProjectionEvent) Payload() []byte                                    { return nil }
func (foreignProjectionEvent) OccurredAt() time.Time                              { return time.Time{} }
func (foreignProjectionEvent) Stream() string                                     { return "foreign" }
func (foreignProjectionEvent) RestoreContext(ctx context.Context) context.Context { return ctx }

// TestNewSagaJournalSource_NilReader asserts the constructor fail-fasts on a nil
// GlobalReader (the GoCell required-dep nil-guard convention) rather than deferring
// to a nil-pointer panic on first use.
func TestNewSagaJournalSource_NilReader(t *testing.T) {
	t.Parallel()
	if _, err := sagaprojection.NewSagaJournalSource(nil); err == nil {
		t.Fatal("NewSagaJournalSource(nil) = nil error, want non-nil (nil GlobalReader must fail fast at construction)")
	}
}

// TestSagaProjectionEvent_Payload asserts Payload() round-trips the journal event's
// raw bytes through the carrier (delivered via Replay).
func TestSagaProjectionEvent_Payload(t *testing.T) {
	t.Parallel()
	factory := newMemJournalFactory()
	j, clk, cleanup := factory(t)
	defer cleanup()

	want := []byte(`{"step":"charge","amount":42}`)
	inst := sagajournaltest.NewInstanceFixture(t, "payload-inst", clk.Now())
	if err := j.Enqueue(context.Background(), inst); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	_, leaseID, err := j.ClaimPending(context.Background(), 10, time.Hour)
	if err != nil || leaseID == "" {
		t.Fatalf("ClaimPending: err=%v leaseID=%v", err, leaseID)
	}
	if _, err := j.Append(context.Background(), inst.ID, leaseID, journal.Event{
		Kind:     journal.KindStepStarted,
		StepName: "charge",
		Payload:  want,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	gr, ok := j.(journal.GlobalReader)
	if !ok {
		t.Fatalf("journal %T does not implement journal.GlobalReader", j)
	}
	src, err := sagaprojection.NewSagaJournalSource(gr)
	if err != nil {
		t.Fatalf("NewSagaJournalSource: %v", err)
	}

	var got []byte
	if err := src.Replay(context.Background(), 0, func(e projection.ProjectionEvent) error {
		got = e.Payload()
		return nil
	}); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("Payload() = %q, want %q", got, want)
	}
}

// TestSagaJournalSource_Position_WrongCarrierType asserts Position returns a
// permanent error for a ProjectionEvent that is not a saga journal carrier.
func TestSagaJournalSource_Position_WrongCarrierType(t *testing.T) {
	t.Parallel()
	src, err := sagaprojection.NewSagaJournalSource(failingGlobalReader{sentinel: errors.New("unused")})
	if err != nil {
		t.Fatalf("NewSagaJournalSource: %v", err)
	}
	_, posErr := src.Position(foreignProjectionEvent{})
	if posErr == nil {
		t.Fatal("Position(foreign carrier) = nil error, want permanent error")
	}
	var permErr *outbox.PermanentError
	if !errors.As(posErr, &permErr) {
		t.Errorf("Position(foreign carrier) error %v is not *outbox.PermanentError (Cursor invariant #4)", posErr)
	}
}

// TestSagaJournalSource_Replay_ContextCancelled asserts Replay honors ctx
// cancellation and returns the ctx error before touching the reader.
func TestSagaJournalSource_Replay_ContextCancelled(t *testing.T) {
	t.Parallel()
	src, err := sagaprojection.NewSagaJournalSource(failingGlobalReader{sentinel: errors.New("must-not-be-reached")})
	if err != nil {
		t.Fatalf("NewSagaJournalSource: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	replayErr := src.Replay(ctx, 0, func(projection.ProjectionEvent) error {
		t.Fatal("fn must not be called when ctx is already canceled")
		return nil
	})
	if !errors.Is(replayErr, context.Canceled) {
		t.Errorf("Replay(canceled ctx) = %v, want context.Canceled", replayErr)
	}
}

// TestSagaJournalSource_Replay_LoadSinceError asserts Replay wraps and propagates a
// reader LoadSince failure (transient store error).
func TestSagaJournalSource_Replay_LoadSinceError(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("loadsince boom")
	src, err := sagaprojection.NewSagaJournalSource(failingGlobalReader{sentinel: sentinel})
	if err != nil {
		t.Fatalf("NewSagaJournalSource: %v", err)
	}
	replayErr := src.Replay(context.Background(), 0, func(projection.ProjectionEvent) error { return nil })
	if !errors.Is(replayErr, sentinel) {
		t.Errorf("Replay over failing reader = %v, want wrapped %v", replayErr, sentinel)
	}
}
