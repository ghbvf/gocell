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

// Phase returns the current lifecycle phase of the Coordinator.
func (c *Coordinator) Phase() Phase {
	return Phase(c.phase.Load()) //nolint:gosec // uint32 holds Phase iota+1 values [1,5], no overflow
}

// Subscribe registers an event subscription for the given projectionID.
// The Apply function is called inside a TxRunner.RunInTx for each event whose
// position is strictly greater than the stored checkpoint (exactly-once).
//
// spec must pass ContractSpec.Validate(). projectionID must be non-empty.
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

	_ = ctx // context reserved for future use (e.g. span propagation)

	cg := c.cellID + "-" + projectionID
	h := c.buildHandler(c.cellID, projectionID, apply)
	if err := c.reg.Subscribe(spec, h, cg, c.cellID, cell.WithSubscriptionSliceID(projectionID)); err != nil {
		return fmt.Errorf("projection.Subscribe[%s]: %w", projectionID, err)
	}
	return nil
}

// buildHandler returns the outbox.EntryHandler closure for the given projection.
// It starts a trace span, runs applyOne inside TxRunner.RunInTx, and classifies
// the resulting error into a HandleResult disposition.
func (c *Coordinator) buildHandler(cellID, projectionID string, apply Apply) outbox.EntryHandler {
	return func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
		ctx, span := c.tracer.Start(ctx, "projection.apply",
			wrapper.Attr{Key: "projection.cell", Value: cellID},
			wrapper.Attr{Key: "projection.id", Value: projectionID},
		)
		defer span.End()

		err := c.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
			return c.applyOne(txCtx, cellID, projectionID, entry, apply)
		})
		return classify(err)
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
