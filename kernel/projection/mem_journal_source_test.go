package projection_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/projection"
	"github.com/ghbvf/gocell/kernel/projection/projectiontest"
)

// newJournalEntry builds a fresh valid outbox.Entry for journal-source seeding.
func newJournalEntry(t *testing.T, clk *clockmock.FakeClock) outbox.Entry {
	t.Helper()
	e, err := outbox.NewEntry(clk, context.Background(), "ordersummary.v1", []byte(`{}`))
	if err != nil {
		t.Fatalf("NewEntry: %v", err)
	}
	return e
}

// TestMemProjectionEventSource_ReplayConformance enrolls MemProjectionEventSource in the
// shared ReplaySource conformance suite (PROJECTION-EVENT-JOURNAL-SOURCE-CONFORMANCE-ENROLL,
// reusing PROJECTION-REPLAY-SOURCE-CONFORMANCE-ENROLL-01). The seed appends fresh entries
// and returns the wrapped *JournalEvent carriers the source delivers.
func TestMemProjectionEventSource_ReplayConformance(t *testing.T) {
	clk := clockmock.New(time.Now())
	src := projection.NewMemProjectionEventSource()
	seed := func(n int) []projection.ProjectionEvent {
		events := make([]projection.ProjectionEvent, n)
		for i := 0; i < n; i++ {
			events[i] = src.Append(newJournalEntry(t, clk))
		}
		return events
	}
	projectiontest.RunReplaySourceConformance(t, src, seed)
}

// TestMemProjectionEventSource_CursorConformance enrolls MemProjectionEventSource in the
// shared Cursor conformance suite. newUnseeded returns a seq-0 sentinel carrier (the
// unseeded value a conforming source never emits) so the permanent-error path is exercised.
func TestMemProjectionEventSource_CursorConformance(t *testing.T) {
	clk := clockmock.New(time.Now())
	src := projection.NewMemProjectionEventSource()
	seed := func(n int) []projection.ProjectionEvent {
		events := make([]projection.ProjectionEvent, n)
		for i := 0; i < n; i++ {
			events[i] = src.Append(newJournalEntry(t, clk))
		}
		return events
	}
	newUnseeded := func() projection.ProjectionEvent {
		return projection.NewJournalEvent(newJournalEntry(t, clk), 0)
	}
	projectiontest.RunCursorConformance(t, src, seed, newUnseeded)
}

// TestMemProjectionEventSource_PositionReadsCarrierNoScan is the #1504 root-fix regression
// for the mem source: Position must read the carrier's own global_seq and NOT scan the
// backing store. A carrier that was never appended to src still resolves to its intrinsic
// seq — proving Position is independent of backing-store membership (contrast
// MemReplaySource.Position, which scans entries by EventID and would return 0 / a
// permanent error for an unknown carrier).
func TestMemProjectionEventSource_PositionReadsCarrierNoScan(t *testing.T) {
	clk := clockmock.New(time.Now())
	src := projection.NewMemProjectionEventSource() // intentionally empty — nothing appended
	carrier := projection.NewJournalEvent(newJournalEntry(t, clk), 42)

	pos, err := src.Position(carrier)
	if err != nil {
		t.Fatalf("Position(unappended carrier) = %v, want it to resolve from the carrier itself", err)
	}
	if pos != 42 {
		t.Errorf("Position = %d, want 42 (the carrier's intrinsic global_seq, no backing scan)", pos)
	}
}

// TestMemProjectionEventSource_PositionRejectsForeignCarrier asserts the type-mismatch and
// seq-0 sentinel branches of PositionFromCarrier both return permanent errors.
func TestMemProjectionEventSource_PositionRejectsForeignCarrier(t *testing.T) {
	clk := clockmock.New(time.Now())
	src := projection.NewMemProjectionEventSource()

	t.Run("non-JournalEvent carrier", func(t *testing.T) {
		// A bare outbox.Entry is a valid ProjectionEvent but not a *JournalEvent.
		_, err := src.Position(newJournalEntry(t, clk))
		assertPermanent(t, err)
	})
	t.Run("seq-0 sentinel", func(t *testing.T) {
		_, err := src.Position(projection.NewJournalEvent(newJournalEntry(t, clk), 0))
		assertPermanent(t, err)
	})
}

// TestMemProjectionEventSource_ReplayHonorsCtxCancel asserts Replay stops with the ctx
// error before delivering any event when the context is already canceled (covers the
// ctx.Err() guard in Replay's loop).
func TestMemProjectionEventSource_ReplayHonorsCtxCancel(t *testing.T) {
	clk := clockmock.New(time.Now())
	src := projection.NewMemProjectionEventSource()
	src.Append(newJournalEntry(t, clk))
	src.Append(newJournalEntry(t, clk))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel: the first qualifying event must trip the ctx.Err() check

	delivered := 0
	err := src.Replay(ctx, 0, func(projection.ProjectionEvent) error {
		delivered++
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Replay error = %v, want context.Canceled", err)
	}
	if delivered != 0 {
		t.Errorf("Replay delivered %d events on a canceled ctx, want 0", delivered)
	}
}

// TestMemProjectionEventSource_ResolveCarrier covers the live-carrier resolver
// (LiveCarrierResolver): a bare appended entry resolves to its position-bearing
// JournalEvent, an already-positioned carrier is returned idempotently, and an
// entry never journaled is a permanent error (the live-path gap closure).
func TestMemProjectionEventSource_ResolveCarrier(t *testing.T) {
	clk := clockmock.New(time.Now())
	src := projection.NewMemProjectionEventSource()
	bare := newJournalEntry(t, clk)
	carrier := src.Append(bare) // assigns global_seq 1
	wantPos, err := src.Position(carrier)
	if err != nil {
		t.Fatalf("Position(carrier): %v", err)
	}

	t.Run("bare entry resolves to its journal carrier", func(t *testing.T) {
		resolved, rerr := src.ResolveCarrier(context.Background(), bare)
		if rerr != nil {
			t.Fatalf("ResolveCarrier(bare appended entry): %v", rerr)
		}
		gotPos, perr := src.Position(resolved)
		if perr != nil {
			t.Fatalf("Position(resolved): %v", perr)
		}
		if gotPos != wantPos {
			t.Errorf("Position(ResolveCarrier(bare)) = %d, want %d (the appended carrier's global_seq)", gotPos, wantPos)
		}
	})

	t.Run("already-positioned carrier is idempotent", func(t *testing.T) {
		resolved, rerr := src.ResolveCarrier(context.Background(), carrier)
		if rerr != nil {
			t.Fatalf("ResolveCarrier(*JournalEvent): %v", rerr)
		}
		if resolved != projection.ProjectionEvent(carrier) {
			t.Errorf("ResolveCarrier(carrier) must return the same *JournalEvent unchanged (idempotent)")
		}
	})

	t.Run("entry never journaled is permanent", func(t *testing.T) {
		_, rerr := src.ResolveCarrier(context.Background(), newJournalEntry(t, clk))
		assertPermanent(t, rerr)
	})
}

func assertPermanent(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("Position = nil error, want a permanent error")
	}
	var permErr *outbox.PermanentError
	if !errors.As(err, &permErr) {
		t.Errorf("Position error %v is not an *outbox.PermanentError (Cursor invariant #4)", err)
	}
}
