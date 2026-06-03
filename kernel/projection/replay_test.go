// L3 conformance: projection rebuild + event replay test (go-standards.md §L3)
package projection

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// ---------------------------------------------------------------------------
// MemReplaySource unit tests
// ---------------------------------------------------------------------------

// TestMemReplaySource_HeadEmpty asserts Head returns 0 when no entries appended.
func TestMemReplaySource_HeadEmpty(t *testing.T) {
	t.Parallel()
	src := NewMemReplaySource()
	head, err := src.Head(context.Background())
	if err != nil {
		t.Fatalf("Head: unexpected error: %v", err)
	}
	if head != 0 {
		t.Errorf("Head = %d, want 0", head)
	}
}

// TestMemReplaySource_HeadAfterAppend asserts Head = number of appended entries.
func TestMemReplaySource_HeadAfterAppend(t *testing.T) {
	t.Parallel()
	src := NewMemReplaySource()
	clk := clockmock.New(time.Now())
	entry1 := mustNewTestEntry(t, clk, "topic.v1")
	entry2 := mustNewTestEntry(t, clk, "topic.v1")
	src.Append(entry1)
	src.Append(entry2)

	head, err := src.Head(context.Background())
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head != 2 {
		t.Errorf("Head = %d, want 2", head)
	}
}

// TestMemReplaySource_ReplayEmpty asserts Replay on empty source is a no-op.
func TestMemReplaySource_ReplayEmpty(t *testing.T) {
	t.Parallel()
	src := NewMemReplaySource()
	var calls int
	err := src.Replay(context.Background(), 0, func(outbox.Entry) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("Replay on empty: %v", err)
	}
	if calls != 0 {
		t.Errorf("fn called %d times on empty source, want 0", calls)
	}
}

// TestMemReplaySource_ReplayAll asserts Replay(0) yields all entries in
// ascending insertion order.
func TestMemReplaySource_ReplayAll(t *testing.T) {
	t.Parallel()
	src := NewMemReplaySource()
	clk := clockmock.New(time.Now())
	const n = 5
	for i := 0; i < n; i++ {
		src.Append(mustNewTestEntry(t, clk, "topic.v1"))
	}

	var received []int64
	err := src.Replay(context.Background(), 0, func(e outbox.Entry) error {
		pos := src.positionOf(e)
		received = append(received, pos)
		return nil
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if len(received) != n {
		t.Fatalf("received %d entries, want %d", len(received), n)
	}
	// Positions must be ascending 1-based.
	for i, pos := range received {
		wantPos := int64(i + 1)
		if pos != wantPos {
			t.Errorf("received[%d] position = %d, want %d", i, pos, wantPos)
		}
	}
}

// TestMemReplaySource_ReplayFromOffset asserts Replay(k) yields only entries
// with position > k, in ascending order.
func TestMemReplaySource_ReplayFromOffset(t *testing.T) {
	t.Parallel()
	src := NewMemReplaySource()
	clk := clockmock.New(time.Now())
	for i := 0; i < 5; i++ {
		src.Append(mustNewTestEntry(t, clk, "topic.v1"))
	}

	var received []int64
	err := src.Replay(context.Background(), 3, func(e outbox.Entry) error {
		received = append(received, src.positionOf(e))
		return nil
	})
	if err != nil {
		t.Fatalf("Replay(3): %v", err)
	}
	// Should get positions 4, 5 only.
	if len(received) != 2 {
		t.Fatalf("received %d entries after offset=3, want 2", len(received))
	}
	if received[0] != 4 {
		t.Errorf("first received position = %d, want 4", received[0])
	}
	if received[1] != 5 {
		t.Errorf("second received position = %d, want 5", received[1])
	}
}

// TestMemReplaySource_ReplayOffsetAtHead asserts Replay(n) where n=Head yields
// no entries.
func TestMemReplaySource_ReplayOffsetAtHead(t *testing.T) {
	t.Parallel()
	src := NewMemReplaySource()
	clk := clockmock.New(time.Now())
	src.Append(mustNewTestEntry(t, clk, "topic.v1"))
	src.Append(mustNewTestEntry(t, clk, "topic.v1"))

	var calls int
	err := src.Replay(context.Background(), 2, func(outbox.Entry) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("Replay at head: %v", err)
	}
	if calls != 0 {
		t.Errorf("fn called %d times at head, want 0", calls)
	}
}

// TestMemReplaySource_ReplayAbortOnError asserts fn error stops replay and is
// returned from Replay.
func TestMemReplaySource_ReplayAbortOnError(t *testing.T) {
	t.Parallel()
	src := NewMemReplaySource()
	clk := clockmock.New(time.Now())
	for i := 0; i < 3; i++ {
		src.Append(mustNewTestEntry(t, clk, "topic.v1"))
	}

	errStop := errors.New("stop")
	var calls int
	err := src.Replay(context.Background(), 0, func(outbox.Entry) error {
		calls++
		if calls == 2 {
			return errStop
		}
		return nil
	})
	if !errors.Is(err, errStop) {
		t.Errorf("Replay returned %v, want errStop", err)
	}
	if calls != 2 {
		t.Errorf("fn called %d times before abort, want 2", calls)
	}
}

// TestMemReplaySource_PositionIs1Based asserts the first appended entry has
// position 1 (Cursor 1-based invariant from cursor.go #2).
func TestMemReplaySource_PositionIs1Based(t *testing.T) {
	t.Parallel()
	src := NewMemReplaySource()
	clk := clockmock.New(time.Now())
	src.Append(mustNewTestEntry(t, clk, "topic.v1"))

	var firstPos int64 = -1
	err := src.Replay(context.Background(), 0, func(e outbox.Entry) error {
		firstPos = src.positionOf(e)
		return nil
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	if firstPos != 1 {
		t.Errorf("first entry position = %d, want 1 (1-based invariant)", firstPos)
	}
}

// Note: projectiontest.RunReplaySourceConformance is called from external packages
// (e.g. adapters that implement ReplaySource). Calling it here would create an
// import cycle (projection → projectiontest → projection). The sub-tests below
// cover the same invariants directly.

// TestMemReplaySource_PositionConsistency asserts that positionOf(e) returns the
// same 1-based index used by Replay (Cursor/Replay position coupling requirement).
// This is the joint assertion required by the spec.
func TestMemReplaySource_PositionConsistency(t *testing.T) {
	t.Parallel()
	src := NewMemReplaySource()
	clk := clockmock.New(time.Now())
	const n = 4
	for i := 0; i < n; i++ {
		src.Append(mustNewTestEntry(t, clk, "topic.v1"))
	}

	// A cursor backed by MemReplaySource.positionOf must return the same position
	// as Replay delivers entries with.
	cur := newMemCursor(src)
	var i int
	err := src.Replay(context.Background(), 0, func(e outbox.Entry) error {
		i++
		pos := src.positionOf(e)
		wantPos := int64(i) // 1-based
		if pos != wantPos {
			t.Errorf("positionOf replay[%d] = %d, want %d", i, pos, wantPos)
		}
		curPos, err := cur.Position(e)
		if err != nil {
			return err
		}
		if curPos != wantPos {
			t.Errorf("cursor.Position replay[%d] = %d, want %d", i, curPos, wantPos)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// mustNewTestEntry creates a minimal outbox.Entry for replay tests.
func mustNewTestEntry(t *testing.T, clk *clockmock.FakeClock, eventType string) outbox.Entry {
	t.Helper()
	e, err := outbox.NewEntry(clk, context.Background(), eventType, []byte(`{}`))
	if err != nil {
		t.Fatalf("outbox.NewEntry: %v", err)
	}
	return e
}
