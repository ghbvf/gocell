package projection

import "github.com/ghbvf/gocell/kernel/outbox"

// Cursor maps a consumed event to its monotonic stream position. outbox.Entry
// carries no sequence field — the position is supplied by the replay source.
// The Coordinator compares Position(entry) against the persisted checkpoint to
// enforce exactly-once delivery to Apply (skip when pos <= checkpoint). A
// resolution error is transient by default (Coordinator requeues) unless wrapped
// in outbox.NewPermanentError. PR-01 ships the interface + a test fake only; the
// production journal/metadata-backed cursor lands in a later PR.
// ref: Axon TrackingToken (position is a property of the token store / stream).
type Cursor interface {
	Position(entry outbox.Entry) (int64, error)
}
