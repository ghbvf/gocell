package projection

import (
	"context"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// OnReset is the business hook invoked during a projection rebuild Reset phase.
// It allows the projection owner to clear its read-model state (e.g. TRUNCATE a
// view table) before the harness resets the checkpoint offset to 0 and begins
// replay. The transaction is ambient — OnReset obtains it via
// persistence.TxFromContext(ctx), exactly like Apply. Both OnReset and the
// SaveOffset(0) call share the same transaction; if OnReset returns an error,
// the whole transaction rolls back and the read-model is left untouched.
//
// Passing nil is valid (projection has no read-model table to clear — offset
// reset alone suffices). OnReset is set via WithOnReset option on Subscribe.
//
// ref: AxonFramework @ResetHandler — called during TrackingEventProcessor reset
// to let the projection clear application state before replay.
type OnReset func(ctx context.Context) error

// Apply is the business event→state projection hook: given a consumed event,
// mutate the read-model. The transaction is ambient — Apply obtains it via
// persistence.TxFromContext(ctx) exactly like outbox.Writer.Write, because the
// Coordinator (PR-01) invokes Apply inside persistence.TxRunner.RunInTx so the
// read-model mutation and the checkpoint advance commit atomically (exactly-once
// delivery; the harness never calls Apply twice for the same offset).
//
// Apply MUST NOT open its own transaction or connection. A transient failure
// returns a plain error (the Coordinator requeues). A permanent failure returns
// an error wrapping outbox.NewPermanentError(err); the Coordinator classifies it
// as DispositionReject and routes to the DLX — the same vocabulary as the
// ConsumerBase handler convention (see .claude/rules/gocell/eventbus.md). Decided
// in ADR §3 Q2 against the eventhorizon read-modify-write entity shape and the
// explicit tx-handle parameter.
//
// ref: JasperFx/marten async-daemon IDocumentOperations apply shape.
type Apply func(ctx context.Context, event outbox.Entry) error

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
