package projection

import (
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// TestMemCursor verifies MemCursor.Position behavior:
//
//	(a) entry present in the paired source → returns 1-based position, nil error
//	(b) entry absent → returns 0 + a permanent error
//	(c) empty source → absent behavior on any entry
func TestMemCursor(t *testing.T) {
	t.Parallel()

	clk := clockmock.New(time.Now())

	// Build two real entries via the MemReplaySource so they have valid unique IDs.
	src := NewMemReplaySource()
	e1 := mustNewTestEntry(t, clk, "topic.v1")
	e2 := mustNewTestEntry(t, clk, "topic.v1")
	src.Append(e1)
	src.Append(e2)

	// Absent entry — created but never appended.
	eAbsent := mustNewTestEntry(t, clk, "topic.v1")

	cur, err := NewMemCursor(src)
	if err != nil {
		t.Fatalf("NewMemCursor() error = %v, want nil", err)
	}

	tests := []struct {
		name       string
		entry      outbox.Entry
		wantPos    int64
		wantPerm   bool // expect a *outbox.PermanentError
		wantErrNil bool // expect nil error
		// Note: wantPerm=false && !wantErrNil is structurally unreachable for
		// MemCursor: an absent entry always returns a *outbox.PermanentError,
		// so every non-nil error case has wantPerm=true. The field is kept
		// for future implementations that may return non-permanent errors.
	}{
		{
			name:       "first entry returns position 1",
			entry:      e1,
			wantPos:    1,
			wantErrNil: true,
		},
		{
			name:       "second entry returns position 2",
			entry:      e2,
			wantPos:    2,
			wantErrNil: true,
		},
		{
			name:     "absent entry returns 0 and permanent error",
			entry:    eAbsent,
			wantPos:  0,
			wantPerm: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pos, err := cur.Position(tc.entry)
			if pos != tc.wantPos {
				t.Errorf("Position() pos = %d, want %d", pos, tc.wantPos)
			}
			if tc.wantErrNil {
				if err != nil {
					t.Errorf("Position() error = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Position() error = nil, want non-nil")
			}
			if tc.wantPerm {
				var pe *outbox.PermanentError
				if !errors.As(err, &pe) {
					t.Errorf("Position() error %v is not a *outbox.PermanentError", err)
				}
			}
		})
	}
}

// TestMemCursor_EmptySource verifies that an entry not appended to an empty
// source returns 0 + permanent error.
func TestMemCursor_EmptySource(t *testing.T) {
	t.Parallel()

	clk := clockmock.New(time.Now())
	emptySrc := NewMemReplaySource()
	cur, err := NewMemCursor(emptySrc)
	if err != nil {
		t.Fatalf("NewMemCursor() error = %v, want nil", err)
	}
	e := mustNewTestEntry(t, clk, "topic.v1")

	pos, err := cur.Position(e)
	if pos != 0 {
		t.Errorf("Position() pos = %d, want 0 for empty source", pos)
	}
	if err == nil {
		t.Fatal("Position() error = nil, want permanent error for empty source")
	}
	var pe *outbox.PermanentError
	if !errors.As(err, &pe) {
		t.Errorf("Position() error %v is not a *outbox.PermanentError", err)
	}
}

// TestMemCursor_NilSource verifies NewMemCursor fails fast on a nil source
// rather than deferring a nil dereference to the first Position call.
func TestMemCursor_NilSource(t *testing.T) {
	t.Parallel()

	cur, err := NewMemCursor(nil)
	if err == nil {
		t.Fatal("NewMemCursor(nil) error = nil, want non-nil")
	}
	if cur != nil {
		t.Errorf("NewMemCursor(nil) cursor = %v, want nil", cur)
	}
}

// TestMemCursor_InterfaceCompliance is a compile-time guard that MemCursor
// implements Cursor. This mirrors the var _ assertion in memcursor.go.
func TestMemCursor_InterfaceCompliance(t *testing.T) {
	t.Parallel()
	var _ Cursor = (*MemCursor)(nil)
}
