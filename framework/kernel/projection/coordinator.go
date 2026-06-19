package projection

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/contractspec"
	"github.com/ghbvf/gocell/framework/kernel/healthz"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// SubscribeRegistrar is the minimal cell.Registrar surface the Coordinator
// needs: it registers the projection's wrapped event subscription. The
// Coordinator holds this narrow interface rather than the full cell.Registrar so
// that (a) the dependency is honest (the Coordinator only ever calls Subscribe)
// and (b) a holder injected at wiring time — e.g. the bootstrap projection
// drain's capture adapter — need not stub the other Registrar methods (no
// nil-embed foot-gun). cell.Registrar satisfies this interface structurally.
//
// The signature mirrors cell.Registrar.Subscribe exactly so a *cell.RegistryRecorder
// (or the drain's capture adapter) is assignable without conversion.
type SubscribeRegistrar interface {
	Subscribe(
		spec contractspec.ContractSpec,
		handler outbox.EntryHandler,
		consumerGroup string,
		cellID string,
		opts ...cell.SubscriptionOption,
	) error
}

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
	reg          SubscribeRegistrar
	txRunner     persistence.TxRunner
	store        CheckpointStore
	cursor       LiveCursor
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

	rebuildCancel       atomic.Pointer[context.CancelFunc]
	rebuildWG           sync.WaitGroup
	lastAppliedUnixNano atomic.Int64
	closeOnce           sync.Once
}

// CoordinatorConfig bundles the Coordinator's dependencies. It mirrors the
// kernel many-dependency convention NewConsumerBase(claimer, ConsumerBaseConfig,
// clk): required deps live in a config struct validated with hand-written nil
// guards, while clk is a separate positional parameter to NewCoordinator.
//
// clk is deliberately NOT a field here: CLOCK-POSITIONAL-INJECTION-01 mandates a
// positional clock parameter and forbids a Clock field on input config structs.
//
// Metrics is optional (nil disables all instruments). Every other field is
// required; a nil/empty value is rejected by NewCoordinator. Grouping the deps
// in a struct (rather than 9 positional params) removes the silent-swap hazard
// of the two adjacent string fields CellID/ProjectionID.
//
// ref: open-source consensus — Watermill cqrs.EventProcessorConfig, Axon
// TrackingEventProcessor.Builder; kernel kernel/outbox.ConsumerBaseConfig.
type CoordinatorConfig struct {
	Registrar    SubscribeRegistrar
	CellID       string
	ProjectionID string
	TxRunner     persistence.TxRunner
	Store        CheckpointStore
	Cursor       LiveCursor
	Replay       ReplaySource
	Tracer       wrapper.Tracer
	Metrics      *Metrics // optional; nil = instruments disabled
}

// NewCoordinator constructs a per-projection Coordinator. clk is the first
// parameter per CLOCK-POSITIONAL-INJECTION-01; all other dependencies are
// supplied via cfg.
//
// When cfg.Metrics is non-nil its label set is validated via preflight during
// construction, so a metric-label misconfiguration fails fast at startup rather
// than panicking on the first runtime metric update.
//
// Required dependencies are validated with hand-written nil guards (using
// validation.IsNilInterface) rather than gocell:"required" codegen — kernel/
// has no codegen dependency (established kernel-layer pattern).
//
// ref: ADR docs/architecture/202605261620-adr-cqrs-projection-lifecycle-harness.md
// ref: kernel/outbox.NewConsumerBase (required deps + config struct + positional clk)
func NewCoordinator(clk clock.Clock, cfg CoordinatorConfig) (*Coordinator, error) {
	clock.MustHaveClock(clk, "projection.NewCoordinator")
	if cfg.CellID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: CellID required")
	}
	if cfg.ProjectionID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: ProjectionID required")
	}
	// Validate CellID and ProjectionID against the probe-name snake_case pattern so
	// metric labels are bounded to enumerated identifiers and the probe constructors
	// (Probes()) cannot fail at runtime. We exercise this by calling the probe-name
	// constructor, which itself calls NewProbeName internally.
	if _, err := healthz.ProjectionLagProbeName(cfg.CellID, cfg.ProjectionID); err != nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: CellID and ProjectionID must be valid snake_case probe-name identifiers",
			errcode.WithInternal(
				errcode.InternalAttr("cellID", cfg.CellID),
				errcode.InternalAttr("projectionID", cfg.ProjectionID),
			))
	}
	if validation.IsNilInterface(cfg.Registrar) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: Registrar required")
	}
	if validation.IsNilInterface(cfg.TxRunner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: TxRunner required")
	}
	if validation.IsNilInterface(cfg.Store) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: Store required")
	}
	if validation.IsNilInterface(cfg.Cursor) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: Cursor required")
	}
	if validation.IsNilInterface(cfg.Replay) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: Replay required")
	}
	if validation.IsNilInterface(cfg.Tracer) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.NewCoordinator: Tracer required")
	}
	// Fail fast on metric-label misconfiguration at construction (the godoc
	// contract) instead of panicking on the first runtime metric update.
	if cfg.Metrics != nil {
		if err := cfg.Metrics.preflight(cfg.CellID, cfg.ProjectionID); err != nil {
			return nil, fmt.Errorf("projection.NewCoordinator: metrics preflight: %w", err)
		}
	}
	c := &Coordinator{
		clk:          clk,
		cellID:       cfg.CellID,
		projectionID: cfg.ProjectionID,
		reg:          cfg.Registrar,
		txRunner:     cfg.TxRunner,
		store:        cfg.Store,
		cursor:       cfg.Cursor,
		replay:       cfg.Replay,
		tracer:       cfg.Tracer,
		metrics:      cfg.Metrics,
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
			return outbox.Requeue(fmt.Errorf("projection.handler: context canceled waiting for gate: %w", ctx.Err()))
		}

		ctx, span := c.tracer.Start(ctx, "projection.apply",
			wrapper.Attr{Key: "gocell.projection.cell", Value: c.cellID},
			wrapper.Attr{Key: "gocell.projection.projection", Value: c.projectionID},
		)
		defer span.End()

		err := c.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
			// Live delivery pushes a bare outbox.Entry with no intrinsic position;
			// resolve it to a position-bearing carrier at the delivery boundary so
			// the carrier-intrinsic Cursor.Position succeeds (rebuild's Replay
			// already produces positioned carriers, so drainGap skips this step).
			ev, rerr := c.cursor.ResolveCarrier(txCtx, entry)
			if rerr != nil {
				return rerr
			}
			return c.applyOne(txCtx, ev, apply)
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

// resolvePosition loads the checkpoint and resolves the entry's cursor position,
// enforcing the two guards every replay step shares: the Cursor 1-based invariant
// (cursor.go #2) and the exactly-once skip (pos <= checkpoint). It is the SINGLE
// SOURCE of those guards for both applyOne (own-stream apply) and
// advanceOffsetPastForeign (foreign-stream checkpoint advance), so the two cannot
// drift if a guard is later changed. proceed is true only when the position is
// new (> checkpoint) and must be committed; a false proceed with nil err means
// the entry was already reflected (a no-op skip).
func (c *Coordinator) resolvePosition(ctx context.Context, entry ProjectionEvent) (pos int64, proceed bool, err error) {
	current, err := c.store.LoadOffset(ctx, c.cellID, c.projectionID)
	if err != nil {
		return 0, false, fmt.Errorf("projection.position[load]: %w", err)
	}
	pos, err = c.cursor.Position(entry)
	if err != nil {
		return 0, false, fmt.Errorf("projection.position[cursor]: %w", err)
	}
	// Enforce the Cursor 1-based invariant (cursor.go #2) at the trust boundary.
	if pos < 1 {
		return 0, false, outbox.NewPermanentError(errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"projection.position: cursor returned a non-positive position; Cursor must honor the 1-based invariant"))
	}
	// Exactly-once: skip events already reflected in the checkpoint.
	if pos <= current {
		return pos, false, nil
	}
	return pos, true, nil
}

// applyOne is the inner per-event logic for an OWN-stream entry: resolve the
// position (1-based + exactly-once guards via resolvePosition), call Apply,
// advance the checkpoint, and update lastApplied lag metrics. It runs inside a
// transaction provided by the caller (buildHandler's RunInTx / drainGap).
func (c *Coordinator) applyOne(ctx context.Context, entry ProjectionEvent, apply Apply) error {
	pos, proceed, err := c.resolvePosition(ctx, entry)
	if err != nil || !proceed {
		return err
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
// so parked handlers unblock. Only the single rebuild goroutine ever calls
// this (single-writer invariant), so select-default is a safe idempotent guard:
// if the channel is already closed (first case fires), we skip; otherwise we
// close it. This avoids double-close panics without recover.
func (c *Coordinator) openGate() {
	gp := c.liveGate.Load()
	select {
	case <-*gp: // already closed (OPEN) — idempotent no-op
	default:
		close(*gp)
	}
}

// classify maps an error to the appropriate HandleResult disposition. The
// permanent-vs-transient predicate is outbox.IsPermanent — the single source
// shared with the saga-journal Tailer, so the two projection drivers cannot
// drift on what "permanent" means.
func classify(err error) outbox.HandleResult {
	if err == nil {
		return outbox.Ack()
	}
	if outbox.IsPermanent(err) {
		return outbox.Reject(err)
	}
	return outbox.Requeue(err)
}
