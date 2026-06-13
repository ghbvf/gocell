package projection

import (
	"context"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// MemCursor is an in-process Cursor paired with a MemReplaySource: it resolves
// an entry's monotonic position by looking it up in the source. Suitable for
// tests and demos only.
//
// Position returns the 1-based insertion index of the entry in the source (same
// value as MemReplaySource.Position). If the entry is not present in the paired
// source, Position returns 0 and a permanent error — retry cannot fix a missing
// source entry.
//
// ref: Axon TrackingToken (position is a property of the token store / stream).
type MemCursor struct {
	src *MemReplaySource
}

// NewMemCursor returns a MemCursor backed by src. src is required: a nil source
// is rejected here (fail-fast) rather than deferred to the first Position call.
func NewMemCursor(src *MemReplaySource) (*MemCursor, error) {
	if src == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewMemCursor: src MemReplaySource is required")
	}
	return &MemCursor{src: src}, nil
}

// Position returns the 1-based insertion index of entry in the paired
// MemReplaySource, or 0 and a permanent error if the entry is not present.
func (c *MemCursor) Position(entry ProjectionEvent) (int64, error) {
	pos := c.src.Position(entry)
	if pos == 0 {
		// entry not in the replay source → cannot assign a stream position;
		// permanent (retry cannot fix a missing source entry).
		return 0, outbox.NewPermanentError(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.MemCursor: entry not present in the paired MemReplaySource"))
	}
	return pos, nil
}

// ResolveCarrier returns entry unchanged: MemCursor resolves position by EventID
// scan against the paired MemReplaySource, so a bare live entry is already
// resolvable by Position — no carrier wrapping is needed. This identity makes
// MemCursor a LiveCursor (usable as a projection.Coordinator cursor). ctx is
// unused: no I/O.
func (c *MemCursor) ResolveCarrier(_ context.Context, entry ProjectionEvent) (ProjectionEvent, error) {
	return entry, nil
}

// compile-time interface check: MemCursor is a full LiveCursor (Position + ResolveCarrier).
var _ LiveCursor = (*MemCursor)(nil)
