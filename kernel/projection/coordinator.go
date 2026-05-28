package projection

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Coordinator wires an event subscription to an Apply function, managing
// exactly-once delivery via a CheckpointStore. Each consumed event is processed
// inside a TxRunner.RunInTx so that the Apply mutation and the checkpoint
// advance commit atomically.
//
// The Coordinator is stateless except for the phase field — all durability is
// delegated to the CheckpointStore.
//
// ref: JasperFx/marten async-daemon ProjectionDaemon — orchestrates Apply +
// checkpoint inside a single transaction.
// ref: AxonFramework TrackingEventProcessor — cursor-based position tracking.
type Coordinator struct {
	cellID   string
	reg      cell.Registrar
	txRunner persistence.TxRunner
	store    CheckpointStore
	cursor   Cursor
	tracer   wrapper.Tracer
	phase    atomic.Uint32
}

// NewCoordinator constructs a Coordinator. All parameters are required; a nil
// or typed-nil value for any interface parameter returns a validation error.
//
// Required dependencies are validated with hand-written nil guards (using
// validation.IsNilInterface) rather than the gocell:"required" codegen funnel —
// kernel/ has no codegen dependency. This is the established kernel-layer pattern
// distinct from REQUIRED-DEP-NIL-GUARD-01 which applies to cells/ only.
//
// cursor: the production implementation (journal/metadata-backed) lands in a
// later PR; PR-01 provides only the interface and a test fake.
func NewCoordinator(
	cellID string,
	reg cell.Registrar,
	txRunner persistence.TxRunner,
	store CheckpointStore,
	cursor Cursor,
	tracer wrapper.Tracer,
) (*Coordinator, error) {
	if cellID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: cellID required")
	}
	if validation.IsNilInterface(reg) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: reg required")
	}
	if validation.IsNilInterface(txRunner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: txRunner required")
	}
	if validation.IsNilInterface(store) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: store required")
	}
	if validation.IsNilInterface(cursor) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: cursor required")
	}
	if validation.IsNilInterface(tracer) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: tracer required")
	}
	c := &Coordinator{
		cellID:   cellID,
		reg:      reg,
		txRunner: txRunner,
		store:    store,
		cursor:   cursor,
		tracer:   tracer,
	}
	c.phase.Store(uint32(PhaseLive))
	return c, nil
}

// Phase returns the current lifecycle phase of the Coordinator. This is a
// best-effort snapshot: the returned value reflects the atomic load at the
// moment of the call and may already be stale by the time the caller acts on
// it. See phase.go for the freshness contract.
func (c *Coordinator) Phase() Phase {
	return Phase(c.phase.Load()) //nolint:gosec // uint32 holds Phase iota+1 values [1,5], no overflow
}

// Subscribe registers an event subscription for the given projectionID.
// The Apply function is called inside a TxRunner.RunInTx for each event whose
// position is strictly greater than the stored checkpoint (exactly-once).
//
// spec must be the event-kind contract spec for the input stream this projection
// consumes: spec.Kind must be "event" and spec.Topic must be non-empty. The
// "projection" kind is a slice-level concept (PR-04 cellgen) — it is NOT the
// kind of the subscribed event contract.
//
// projectionID must be non-empty and should follow the no-dash convention
// (caller/cellgen responsibility — no validation here beyond empty-check).
//
// ctx is currently unused (reserved for future span propagation); callers
// should pass their caller context for forward compatibility.
//
// The consumerGroup is derived as cellID + "-" + projectionID and is not
// caller-configurable — each projection has exactly one consumer for ordering.
//
// In production, Subscribe should only be called from cellgen-generated wiring
// derived from slice.yaml contractUsages (kind:projection → cell_gen.go).
// Hand-written callsites bypass the single source of truth. Tracked in
// gh #1100 for Hard enforcement via a sealed cellgen token in PR-04.
//
// apply must be non-nil. opts are applied to subscribeOptions (currently no
// option constructors exist in v1).
func (c *Coordinator) Subscribe(
	ctx context.Context,
	spec contractspec.ContractSpec,
	projectionID string,
	apply Apply,
	opts ...Option,
) error {
	if projectionID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.Subscribe: projectionID required")
	}
	if apply == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.Subscribe: apply required")
	}
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("projection.Subscribe[%s]: invalid spec: %w", projectionID, err)
	}

	var o subscribeOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}

	_ = ctx // ctx reserved for future span propagation; callers pass their caller context

	cg := c.cellID + "-" + projectionID
	h := c.buildHandler(c.cellID, projectionID, apply)
	if err := c.reg.Subscribe(spec, h, cg, c.cellID, cell.WithSubscriptionSliceID(projectionID)); err != nil {
		return fmt.Errorf("projection.Subscribe[%s]: %w", projectionID, err)
	}
	return nil
}

// buildHandler returns the outbox.EntryHandler closure for the given projection.
// It starts a trace span named "projection.apply" (stable ops contract — rename
// requires dashboard sync), runs applyOne inside TxRunner.RunInTx, classifies
// the resulting error into a HandleResult disposition, and marks the span status:
//
//   - Ack  → SetStatus(StatusOK, "")
//   - Requeue → SetStatus(StatusError, "requeue")
//   - Reject  → SetStatus(StatusError, "reject")
//
// Detailed error recording (RecordError + redaction) is delegated to the outer
// WrapConsumer span so that sensitive substrings never reach the trace backend
// without passing through pkg/redaction.RedactError. This span only marks status.
func (c *Coordinator) buildHandler(cellID, projectionID string, apply Apply) outbox.EntryHandler {
	return func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
		ctx, span := c.tracer.Start(ctx, "projection.apply",
			wrapper.Attr{Key: "gocell.projection.cell", Value: cellID},
			wrapper.Attr{Key: "gocell.projection.id", Value: projectionID},
		)
		defer span.End()

		err := c.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
			return c.applyOne(txCtx, cellID, projectionID, entry, apply)
		})
		result := classify(err)
		switch result.Disposition {
		case outbox.DispositionAck:
			span.SetStatus(wrapper.StatusOK, "")
		case outbox.DispositionRequeue:
			span.SetStatus(wrapper.StatusError, "requeue")
		case outbox.DispositionReject:
			span.SetStatus(wrapper.StatusError, "reject")
		}
		return result
	}
}

// applyOne is the inner per-event logic: load checkpoint, compare position,
// call Apply, and advance the checkpoint. It runs inside a transaction provided
// by the caller (buildHandler's RunInTx).
func (c *Coordinator) applyOne(
	ctx context.Context,
	cellID, projectionID string,
	entry outbox.Entry,
	apply Apply,
) error {
	current, err := c.store.LoadOffset(ctx, cellID, projectionID)
	if err != nil {
		return err
	}

	pos, err := c.cursor.Position(entry)
	if err != nil {
		return err
	}

	// Exactly-once: skip events already reflected in the checkpoint.
	if pos <= current {
		return nil
	}

	if err := apply(ctx, entry); err != nil {
		return err
	}

	return c.store.SaveOffset(ctx, cellID, projectionID, pos)
}

// classify maps an error to the appropriate HandleResult disposition.
//   - nil      → Ack (success)
//   - permanent error (PermanentError anywhere in the chain) → Reject
//   - any other error → Requeue (transient)
func classify(err error) outbox.HandleResult {
	if err == nil {
		return outbox.Ack()
	}
	if isPermanent(err) {
		return outbox.Reject(err)
	}
	return outbox.Requeue(err)
}

// isPermanent reports whether err is or wraps an *outbox.PermanentError.
func isPermanent(err error) bool {
	var pe *outbox.PermanentError
	return errors.As(err, &pe)
}
