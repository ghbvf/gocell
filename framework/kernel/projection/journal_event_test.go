package projection_test

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/projection"
)

// ctxKey is a private context key used to prove RestoreContext does not strip an
// ambient value (idempotency / no-overwrite of the surrounding context).
type ctxKey struct{}

// TestJournalEvent_DelegatesToEntry asserts every ProjectionEvent accessor on the
// carrier returns the wrapped Entry's value, and GlobalSeq returns the intrinsic
// position. These delegating methods are otherwise only exercised indirectly by the
// conformance harness (which reads EventID alone), so cover them directly here.
func TestJournalEvent_DelegatesToEntry(t *testing.T) {
	clk := clockmock.New(time.Now())
	entry := newJournalEntry(t, clk)
	carrier := projection.NewJournalEvent(entry, 7)

	if got, want := carrier.EventID(), entry.EventID(); got != want {
		t.Errorf("EventID = %q, want %q", got, want)
	}
	if got, want := string(carrier.Payload()), string(entry.Payload()); got != want {
		t.Errorf("Payload = %q, want %q", got, want)
	}
	if got, want := carrier.OccurredAt(), entry.OccurredAt(); !got.Equal(want) {
		t.Errorf("OccurredAt = %v, want %v", got, want)
	}
	if got, want := carrier.Stream(), entry.Stream(); got != want {
		t.Errorf("Stream = %q, want %q", got, want)
	}
	if got := carrier.GlobalSeq(); got != 7 {
		t.Errorf("GlobalSeq = %d, want 7", got)
	}
}

// TestJournalEvent_RestoreContextDelegates asserts RestoreContext delegates to the
// wrapped Entry: it returns a non-nil context and preserves an ambient value (it must
// not strip the surrounding context — the outbox no-overwrite restore contract).
func TestJournalEvent_RestoreContextDelegates(t *testing.T) {
	clk := clockmock.New(time.Now())
	carrier := projection.NewJournalEvent(newJournalEntry(t, clk), 3)

	base := context.WithValue(context.Background(), ctxKey{}, "ambient")
	restored := carrier.RestoreContext(base)
	if restored == nil {
		t.Fatal("RestoreContext returned nil context")
	}
	if got := restored.Value(ctxKey{}); got != "ambient" {
		t.Errorf("RestoreContext stripped the ambient value: got %v, want \"ambient\"", got)
	}
}

// TestPositionFromCarrier covers the shared resolver directly: a valid carrier yields
// its global_seq; a non-JournalEvent carrier and the seq-0 sentinel are permanent.
func TestPositionFromCarrier(t *testing.T) {
	clk := clockmock.New(time.Now())

	t.Run("valid carrier returns global_seq", func(t *testing.T) {
		pos, err := projection.PositionFromCarrier(projection.NewJournalEvent(newJournalEntry(t, clk), 99))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pos != 99 {
			t.Errorf("position = %d, want 99", pos)
		}
	})
	t.Run("non-carrier is permanent", func(t *testing.T) {
		_, err := projection.PositionFromCarrier(newJournalEntry(t, clk)) // bare outbox.Entry
		assertPermanent(t, err)
	})
	t.Run("seq-0 sentinel is permanent", func(t *testing.T) {
		_, err := projection.PositionFromCarrier(projection.NewJournalEvent(newJournalEntry(t, clk), 0))
		assertPermanent(t, err)
	})
}
