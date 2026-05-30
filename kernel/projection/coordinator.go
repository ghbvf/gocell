package projection

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/clock"
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
// # Per-projection model (PR-03)
//
// Each Coordinator is per-projection (projectionID moves from Subscribe to
// NewCoordinator). Subscribe may only be called once; a second call returns
// an error. This is a Hard constraint enforced by a subscribed atomic.Bool CAS.
// A cell hosting N projections creates N Coordinators.
//
// # Gate mechanism (PR-03)
//
// During a rebuild, the live event handler parks on an unbuffered channel gate
// (liveGate). An OPEN gate is a CLOSED channel (reads pass immediately); a
// SHUT gate is a fresh open (non-closed) channel (reads block). Only the
// single rebuild goroutine calls shutGate/openGate.
//
// ref: JasperFx/marten async-daemon ProjectionDaemon.
// ref: AxonFramework TrackingEventProcessor.
type Coordinator struct {
	clk          clock.Clock
	cellID       string
	projectionID string
	reg          cell.Registrar
	txRunner     persistence.TxRunner
	store        CheckpointStore
	cursor       Cursor
	replay       ReplaySource
	tracer       wrapper.Tracer
	metrics      *Metrics // optional; nil = instruments disabled

	phase    atomic.Uint32
	done     chan struct{}
	liveGate atomic.Pointer[chan struct{}]

	subscribed atomic.Bool
	apply      Apply
	spec       contractspec.ContractSpec
	onReset    OnReset

	rebuildCancel       context.CancelFunc
	rebuildWG           sync.WaitGroup
	lastAppliedUnixNano atomic.Int64
}

// NewCoordinator constructs a Coordinator. clk is the first parameter per
// CLOCK-POSITIONAL-INJECTION-01. All parameters except metrics are required.
//
// metrics is optional (nil = all instruments disabled). When non-nil, the
// label set is validated via metrics.preflight during construction.
//
// Required dependencies are validated with hand-written nil guards (using
// validation.IsNilInterface) rather than gocell:"required" codegen — kernel/
// has no codegen dependency (established kernel-layer pattern).
//
// ref: ADR docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md
func NewCoordinator(
	clk clock.Clock,
	cellID, projectionID string,
	reg cell.Registrar,
	txRunner persistence.TxRunner,
	store CheckpointStore,
	cursor Cursor,
	replay ReplaySource,
	tracer wrapper.Tracer,
	metrics *Metrics,
) (*Coordinator, error) {
	clock.MustHaveClock(clk, "projection.NewCoordinator")
	if cellID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: cellID required")
	}
	if projectionID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: projectionID required")
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
	if validation.IsNilInterface(replay) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: replay required")
	}
	if validation.IsNilInterface(tracer) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: tracer required")
	}
	c := &Coordinator{
		clk:          clk,
		cellID:       cellID,
		projectionID: projectionID,
		reg:          reg,
		txRunner:     txRunner,
		store:        store,
		cursor:       cursor,
		replay:       replay,
		tracer:       tracer,
		metrics:      metrics,
		done:         make(chan struct{}),
	}
	c.phase.Store(uint32(PhaseLive))
	c.initGateOpen()
	return c, nil
}

// Phase returns the current lifecycle phase of the Coordinator. Best-effort
// snapshot: may be stale by the time the caller acts on it.
func (c *Coordinator) Phase() Phase {
	return Phase(c.phase.Load()) //nolint:gosec // uint32 holds Phase iota+1 values [1,5], no overflow
}

// Subscribe registers the event subscription for this Coordinator's projection.
// Subscribe must be called exactly once; a second call returns an error (v1
// single-input-stream Hard constraint: one Coordinator, one input stream).
//
// spec must be an event-kind contract spec (spec.Kind must be "event").
// apply must be non-nil. opts are applied to subscribeOptions.
//
// The consumerGroup is derived as cellID + "-" + projectionID and is not
// caller-configurable (each projection has exactly one consumer group).
//
// After a successful Subscribe, apply and onReset (from opts) are captured for
// reuse by Rebuild.
func (c *Coordinator) Subscribe(
	ctx context.Context,
	spec contractspec.ContractSpec,
	apply Apply,
	opts ...Option,
) error {
	if !c.subscribed.CompareAndSwap(false, true) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.Subscribe: already subscribed; one Coordinator hosts exactly one projection (v1 single input stream)")
	}

	if apply == nil {
		c.subscribed.Store(false) // allow retry with valid apply
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.Subscribe: apply required")
	}
	if spec.Kind != cellvocab.ContractEvent {
		c.subscribed.Store(false)
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.Subscribe: spec.Kind must be \"event\"; a projection consumes an event-kind input stream")
	}
	if err := spec.Validate(); err != nil {
		c.subscribed.Store(false)
		return fmt.Errorf("projection.Subscribe[%s]: invalid spec: %w", c.projectionID, err)
	}

	var o subscribeOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}

	// Capture for Rebuild reuse.
	c.apply = apply
	c.spec = spec
	c.onReset = o.onReset

	_ = ctx // reserved for future span propagation

	cg := c.cellID + "-" + c.projectionID
	h := c.buildHandler(apply)
	if err := c.reg.Subscribe(spec, h, cg, c.cellID, cell.WithSubscriptionSliceID(c.projectionID)); err != nil {
		return fmt.Errorf("projection.Subscribe[%s]: %w", c.projectionID, err)
	}
	return nil
}

// buildHandler returns the outbox.EntryHandler closure for this projection.
// It parks on the liveGate during rebuild phases, then runs applyOne inside
// TxRunner.RunInTx.
//
// Gate semantics: OPEN gate = closed channel (reads pass); SHUT gate = open
// channel (reads block until Catchup phase opens the gate).
func (c *Coordinator) buildHandler(apply Apply) outbox.EntryHandler {
	return func(ctx context.Context, entry outbox.Entry) outbox.HandleResult {
		// Park during rebuild phases (stopped/reset/replay). OPEN gate is a closed
		// channel so the first case fires immediately.
		gp := c.liveGate.Load()
		select {
		case <-*gp:
			// gate is open — proceed
		case <-ctx.Done():
			return outbox.Requeue(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"projection.handler: context canceled while waiting for gate"))
		}

		ctx, span := c.tracer.Start(ctx, "projection.apply",
			wrapper.Attr{Key: "gocell.projection.cell", Value: c.cellID},
			wrapper.Attr{Key: "gocell.projection.id", Value: c.projectionID},
		)
		defer span.End()

		err := c.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
			return c.applyOne(txCtx, entry, apply)
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
// call Apply, advance checkpoint, and update lastApplied metrics.
// It runs inside a transaction provided by the caller (buildHandler's RunInTx).
func (c *Coordinator) applyOne(ctx context.Context, entry outbox.Entry, apply Apply) error {
	current, err := c.store.LoadOffset(ctx, c.cellID, c.projectionID)
	if err != nil {
		return fmt.Errorf("projection.applyOne[load]: %w", err)
	}

	pos, err := c.cursor.Position(entry)
	if err != nil {
		return fmt.Errorf("projection.applyOne[cursor]: %w", err)
	}

	// Enforce the Cursor 1-based invariant (cursor.go #2) at the trust boundary.
	if pos < 1 {
		return outbox.NewPermanentError(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.applyOne: cursor returned a non-positive position; Cursor must honor the 1-based invariant"))
	}

	// Exactly-once: skip events already reflected in the checkpoint.
	if pos <= current {
		return nil
	}

	if err := apply(ctx, entry); err != nil {
		return fmt.Errorf("projection.applyOne[apply]: %w", err)
	}

	if err := c.store.SaveOffset(ctx, c.cellID, c.projectionID, pos); err != nil {
		return fmt.Errorf("projection.applyOne[save]: %w", err)
	}

	// Record last applied domain time (used by readyz lag computation).
	c.lastAppliedUnixNano.Store(entry.OccurredAt().UnixNano())
	lagSecs := c.clk.Since(entry.OccurredAt()).Seconds()
	c.metrics.setReplayLag(ctx, c.cellID, c.projectionID, lagSecs)
	return nil
}

// ---------------------------------------------------------------------------
// Gate helpers
// ---------------------------------------------------------------------------

// initGateOpen initializes the liveGate to an OPEN state: a pre-closed channel.
// Called once in NewCoordinator.
func (c *Coordinator) initGateOpen() {
	ch := make(chan struct{})
	close(ch) // closed = OPEN = passes immediately
	c.liveGate.Store(&ch)
}

// shutGate sets the gate to SHUT: replaces with a fresh open (non-closed)
// channel. Live handlers that load after this call will park.
// Only the rebuild goroutine calls this.
func (c *Coordinator) shutGate() {
	ch := make(chan struct{})
	c.liveGate.Store(&ch)
}

// openGate sets the gate to OPEN: closes the current channel idempotently
// so parked handlers unblock. Only the rebuild goroutine calls this.
func (c *Coordinator) openGate() {
	gp := c.liveGate.Load()
	// Close idempotently via select-default; double-close panics, so guard with recover.
	func() {
		defer func() { recover() }() //nolint:errcheck // intentional idempotent close
		close(*gp)
	}()
}

// classify maps an error to the appropriate HandleResult disposition.
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
