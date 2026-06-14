package projection

import "github.com/ghbvf/gocell/framework/kernel/cellvocab"

// ProjectionEvent is the minimal typed read-only carrier the harness applies:
// ReplaySource.Replay's callback, Cursor.Position, and Apply all carry a
// ProjectionEvent rather than the concrete outbox.Entry, so the outbox event
// source and a saga-journal event source (EPIC #1609 PR-03) share one typed
// funnel. It is an alias of cellvocab.ProjectionEvent — the carrier type lives in
// the cellvocab leaf because kernel/projection imports kernel/cell (whose
// ProjectionApply mirror references the same carrier), so the type must live in a
// package both import without a cycle. See cellvocab.ProjectionEvent for the
// method contract and the not-sealed rationale; carrier-shape enforcement is
// archtest PROJECTION-EVENT-CARRIER-TYPED-01.
type ProjectionEvent = cellvocab.ProjectionEvent

// Apply is the business event→state projection hook: given a consumed
// ProjectionEvent, mutate the read-model. The transaction is ambient — Apply
// obtains it via persistence.TxFromContext(ctx) exactly like outbox.Writer.Write,
// because the Coordinator invokes Apply inside persistence.TxRunner.RunInTx so the
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
// It is an alias of cellvocab.ProjectionApply (also aliased by
// kernel/cell.ProjectionApply), the single underlying type that lets the
// bootstrap drain pass a cell-recorded hook to Coordinator.Subscribe with no
// named-type conversion.
//
// ref: JasperFx/marten async-daemon IDocumentOperations apply shape.
type Apply = cellvocab.ProjectionApply

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
// It is an alias of cellvocab.ProjectionResetHook (also aliased by
// kernel/cell.ProjectionResetHook).
//
// ref: AxonFramework @ResetHandler — called during TrackingEventProcessor reset
// to let the projection clear application state before replay.
type OnReset = cellvocab.ProjectionResetHook
