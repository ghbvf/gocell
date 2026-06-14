package projection

import (
	"context"
)

// Apply, OnReset, and the ProjectionEvent carrier are declared in event.go (as
// aliases of the cellvocab leaf types that break the cell↔projection cycle).

// CheckpointStore persists a projection's consumed offset. It is the framework's
// own offset table — it does NOT touch any business read-model schema (the
// CellTx-offset design keeps the harness clear of the GAP-8 seal; ADR §4).
//
// The offset is an opaque, monotonically increasing cursor over the projection's
// input stream. Its concrete mapping to a stream position is owned by the replay
// source defined in PR-01 (the harness compares a replayed event's position
// against the stored checkpoint to skip already-applied events) — it is NOT an
// outbox.Entry field (Entry carries no sequence number today). LoadOffset returns
// 0 for an unknown (cellID, projectionID) pair (cold start = offset 0).
//
// Both methods are ambient-tx: SaveOffset participates in the caller's
// transaction via persistence.TxFromContext(ctx) (no raw db handle — enforced by
// PROJECTION-CHECKPOINT-TX-BOUND-01), so the offset advance commits together
// with the Apply mutation.
//
// Implementations: mem (PR-01) + postgres (PR-02); both verified by the shared
// projectiontest.RunCheckpointConformance template (PR-01). Decided in ADR §3
// Q1 (caller-provided tx + harness-internal SaveOffset, mirroring outbox.Writer
// and Axon's JdbcTokenStore same-tx commit).
type CheckpointStore interface {
	// LoadOffset returns the last committed offset for the projection, or 0 if
	// none has been recorded yet (cold start).
	LoadOffset(ctx context.Context, cellID, projectionID string) (int64, error)
	// SaveOffset advances the projection's committed offset within the ambient
	// transaction carried by ctx.
	SaveOffset(ctx context.Context, cellID, projectionID string, offset int64) error
}

// Option configures a projection subscription. It is the frozen functional-option
// seam consumed by Coordinator.Subscribe (PR-01); concrete option constructors
// (e.g. starting offset, fail-open policy) are added alongside the Coordinator.
// Declared here so the Subscribe API surface is fixed by the ADR rather than
// drifting when the implementation lands.
//
// Available option constructors:
//   - [WithOnReset] — registers an OnReset hook invoked during the rebuild Reset phase.
//
// Passing no opts (an empty or nil slice) is valid.
type Option func(*subscribeOptions)

// subscribeOptions holds the resolved Subscribe configuration. It is the target
// of Option closures.
type subscribeOptions struct {
	onReset OnReset
}

// WithOnReset configures the OnReset hook for the projection. When a rebuild is
// triggered, the hook is called inside the Reset transaction so the read-model
// can be cleared atomically with the checkpoint offset reset to 0. Passing nil
// is valid (no-op; offset is still reset to 0).
func WithOnReset(fn OnReset) Option {
	return func(o *subscribeOptions) {
		o.onReset = fn
	}
}
