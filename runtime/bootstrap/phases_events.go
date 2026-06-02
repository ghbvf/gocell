package bootstrap

// phases_events.go — event router startup and subscription validation (phase6).
//
// Covers:
//   - phase6StartEventRouter: subscription + projection registration + evtRouter.Run on runCtx
//   - checkNoEventConsumersWhenSubscriberNil: fail-fast when cells declared
//     subscriptions or projections but no subscriber is configured
//   - autoWireEventRouterCollector: creates EventRouterCollector when a real
//     provider is configured and injects it into Router via WithEventRouterCollector
//   - autoWireOutboxRejectCollector: creates OutboxRejectCollector and wires
//     it into ConsumerBase (AttachObserver) before subscriptions start consuming.
//     PendingDepth wiring is done explicitly by each corebundle module via
//     relay.WithPendingDepthObserver, not by bootstrap auto-wire.
//
// ref: uber-go/fx app.go — Run vs stop ctx separation: event router uses runCtx
// (independent of external ctx) so lifecycle is owned by phase10 teardown, not
// by the caller canceling the external context.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/eventrouter"
	metricsmiddleware "github.com/ghbvf/gocell/runtime/observability/metrics"
)

// Compile-time check: EventRouterCollector satisfies eventrouter.EventCollector.
// Cannot live in runtime/observability/metrics because that would create
// a metrics → eventrouter import cycle (bootstrap drains eventrouter
// subscriptions via the metrics-side collector).
var _ eventrouter.EventCollector = (*metricsmiddleware.EventRouterCollector)(nil)

// phase6StartEventRouter registers subscriptions and starts the event router
// using state.runCtx (independent of the external context).
//
// Key invariant: evtRouter.Run uses state.runCtx, NOT the external ctx.
// External ctx cancellation triggers phase9 → phase10 which calls evtRouter.Close;
// that closes runCtx internally, causing Run to return.
// ref: uber-go/fx app.go:L545-567 (run vs stop ctx separation).
func (b *Bootstrap) phase6StartEventRouter(runCtx context.Context, s *phaseState) error {
	// Auto-wire outbox reject collector before subscriptions start consuming.
	// Must run before buildEventRouter so AttachObserver is called before
	// ConsumerBase begins processing any delivered entries.
	if err := b.autoWireOutboxRejectCollector(); err != nil {
		return err
	}

	sub := s.sub
	if sub == nil {
		// Both plain subscriptions and projections (which become event
		// subscriptions once drained) need a Subscriber to consume.
		return b.checkNoEventConsumersWhenSubscriberNil(s)
	}
	if !cellSnapshotsHaveSubscriptions(s) && !cellSnapshotsHaveProjections(s) {
		// No subscriptions or projections to drain: skip router build entirely.
		// Avoids invoking NewSubscriberWithMiddleware (which requires a non-nil
		// ConsumerBase) when the deployment wires a Subscriber for future use
		// but has no current handlers.
		return nil
	}
	if err := b.checkConsumerBaseConfiguredForSubscriptions(s); err != nil {
		return err
	}

	evtRouter, err := b.buildEventRouter(sub)
	if err != nil {
		return err
	}
	if err := b.drainCellSubscriptions(s, evtRouter); err != nil {
		return err
	}
	if err := b.drainCellProjections(runCtx, s, evtRouter); err != nil {
		return err
	}

	return b.startAndRegisterEventRouter(runCtx, s, evtRouter)
}

// cellSnapshotsHaveSubscriptions reports whether any cell snapshot in the
// phase state declared at least one event subscription. Used to short-circuit
// router construction when there is no work to drain.
func cellSnapshotsHaveSubscriptions(s *phaseState) bool {
	for _, id := range s.asm.CellIDs() {
		snap, ok := s.cellSnapshots[id]
		if !ok {
			continue
		}
		if len(snap.Subscriptions) > 0 {
			return true
		}
	}
	return false
}

// autoWireEventRouterCollector creates an EventRouterCollector (once, cached in
// b.eventRouterCollector) and returns it as an eventrouter.Option slice so the
// Router can record subscription lifecycle metrics.
//
// Skip conditions (return nil slice):
//   - metricsProvider is nil
//   - metricsProvider is NopProvider (default; avoid no-op allocations at startup)
//
// ref: runtime/bootstrap/phases_http.go autoWireHTTPMetricsCollector — same
// skip-on-nil/skip-on-Nop pattern and cached-field approach.
func (b *Bootstrap) autoWireEventRouterCollector() ([]eventrouter.Option, error) {
	collector, wired, err := autoWireCachedCollector(b, &b.eventRouterCollector,
		metricsmiddleware.NewEventRouterCollector,
		"bootstrap: event router metrics auto-wire conflict: WithMetricsProvider constructs the event router collector; "+
			"do not also register event_router_subscriptions_active manually on the same provider. Remove one side")
	if err != nil {
		return nil, err
	}
	if !wired {
		return nil, nil
	}
	return []eventrouter.Option{eventrouter.WithEventRouterCollector(collector)}, nil
}

// buildEventRouter creates the event router with middleware and validators.
//
// The SubscriberWithMiddleware wires the business middleware chain and
// ConsumerBase. The inner Subscriber is decorated with contract tracing so each
// delivery span closes after final broker settlement (Commit/Ack/Nack/Release).
// Returns an error if the SubscriberWithMiddleware ctor rejects nil deps.
func (b *Bootstrap) buildEventRouter(sub outbox.Subscriber) (*eventrouter.Router, error) {
	var evtRouterOpts []eventrouter.Option
	if b.routerReadyTimeoutSet {
		evtRouterOpts = append(evtRouterOpts, eventrouter.WithReadyTimeout(b.routerReadyTimeout))
	}
	// R2: auto-wire event router collector when a real Provider is configured.
	collectorOpts, err := b.autoWireEventRouterCollector()
	if err != nil {
		return nil, err
	}
	evtRouterOpts = append(evtRouterOpts, collectorOpts...)

	swm, err := outbox.NewSubscriberWithMiddleware(
		eventrouter.NewContractTracingSubscriber(sub, b.wrapperTracer),
		b.consumerBase,
		b.consumerMiddleware...,
	)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: buildEventRouter: %w", err)
	}
	evtRouter := eventrouter.New(swm, b.clock, evtRouterOpts...)

	for _, v := range b.subscriptionValidators {
		evtRouter.AddSubscriptionValidator(v)
	}
	return evtRouter, nil
}

// drainCellSubscriptions registers all cell snapshot subscriptions into the router.
//
// CellID is the AI-HARD positional parameter on Registry.Subscribe (codegen
// injects it from cell metadata at compile time), so each SubscriptionRequest
// arrives with CellID already populated. The drain loop only cross-checks
// that CellID matches the snapshot owner (fail-fast on drift) — it does NOT
// silently rewrite the field, which would mask a codegen defect where a cell
// has been miswired to register a subscription owned by a different cell.
//
// ref: ADR docs/architecture/202605111000-adr-subscription-cellid-mandatory.md
func (b *Bootstrap) drainCellSubscriptions(s *phaseState, evtRouter *eventrouter.Router) error {
	for _, id := range s.asm.CellIDs() {
		snap, ok := s.cellSnapshots[id]
		if !ok {
			continue
		}
		for _, sub := range snap.Subscriptions {
			if sub.CellID != id {
				return fmt.Errorf("bootstrap: cell %s subscription drift: declared CellID=%q but snapshot owner=%q"+
					" (codegen should inject cellID from cell metadata; check cellgen + contractgen templates)",
					id, sub.CellID, id)
			}
			var opts []cell.SubscriptionOption
			if sub.SliceID != "" {
				opts = append(opts, cell.WithSubscriptionSliceID(sub.SliceID))
			}
			if err := evtRouter.AddContractHandler(sub.Spec, sub.Handler, sub.ConsumerGroup, sub.CellID, opts...); err != nil {
				return fmt.Errorf("bootstrap: cell %s subscription setup failed: %w", id, err)
			}
		}
	}
	return nil
}

// startAndRegisterEventRouter registers the health probe, starts the router goroutine,
// and wires teardown.
func (b *Bootstrap) startAndRegisterEventRouter(runCtx context.Context, s *phaseState, evtRouter *eventrouter.Router) error {
	evtHealth := evtRouter.Health // func() error — wrap to ctx-aware signature
	if err := s.registerHealthChecker(eventRouterCheckerName, func(_ context.Context) error {
		return evtHealth()
	}, b.healthAggregator); err != nil {
		return err
	}

	slog.Info("bootstrap: starting event router",
		slog.Int("handler_count", evtRouter.HandlerCount()))

	routerErrCh := make(chan error, 1)
	// evtRouter.Run uses runCtx — not the external ctx.
	// ref: uber-go/fx run vs stop ctx separation.
	go func() {
		routerErrCh <- evtRouter.Run(runCtx)
	}()

	select {
	case err := <-routerErrCh:
		return fmt.Errorf("bootstrap: event router: %w", err)
	case <-evtRouter.Running():
		// All subscriptions consuming.
	}

	s.routerErrCh = routerErrCh
	s.addTeardown(func(c context.Context) error {
		return evtRouter.Close(c)
	})
	return nil
}

// checkNoEventConsumersWhenSubscriberNil fails fast when any cell registered an
// event consumer — a subscription (reg.Subscribe) or a projection
// (reg.RegisterProjection, which becomes an event subscription once drained) —
// but no subscriber is configured. This prevents silently dropping all event
// handlers when WithSubscriber is omitted.
func (b *Bootstrap) checkNoEventConsumersWhenSubscriberNil(s *phaseState) error {
	for _, id := range s.asm.CellIDs() {
		snap, ok := s.cellSnapshots[id]
		if !ok {
			continue
		}
		if len(snap.Subscriptions) > 0 {
			return fmt.Errorf(
				"bootstrap: cell %s registered subscriptions but no subscriber is configured; "+
					"add WithSubscriber to bootstrap options", id)
		}
		if len(snap.Projections) > 0 {
			return fmt.Errorf(
				"bootstrap: cell %s registered a projection but no subscriber is configured; "+
					"add WithSubscriber to bootstrap options", id)
		}
	}
	return nil
}

// autoWireOutboxRejectCollector creates the OutboxRejectCollector (once, cached
// in b.outboxRejectCollector) and wires it into ConsumerBase (AttachObserver).
// Called at the start of phase6, before subscriptions begin consuming, so
// AttachObserver runs before ConsumerBase processes any delivered entry.
//
// PendingDepth wiring is NOT done here — each composition-root module (e.g.
// cellmodules/configcore/storage.go) constructs a per-cell
// OutboxPendingDepthCollector and injects it directly via
// relay.WithPendingDepthObserver. This keeps the cell label accurate: the
// bootstrap auto-wire path has no per-cell context.
//
// Skip conditions:
//   - metricsProvider is nil (no backend configured)
//   - metricsProvider is NopProvider (default; avoid no-op allocations at startup)
//
// ref: runtime/bootstrap/phases_http.go autoWireHTTPMetricsCollector — same
// skip-on-nil/skip-on-Nop pattern, same cached-field approach.
func (b *Bootstrap) autoWireOutboxRejectCollector() error {
	// No cellID: reject collector is multi-cell-shared; per-cell label flows from
	// ObserveReject's call-site argument.
	collector, wired, err := autoWireCachedCollector(b, &b.outboxRejectCollector,
		metricsmiddleware.NewOutboxRejectCollector,
		"bootstrap: outbox metrics auto-wire conflict: WithMetricsProvider constructs the outbox reject collector; "+
			"do not also register outbox_consumer_rejected_total manually on the same provider. Remove one side")
	if err != nil {
		return err
	}
	if !wired {
		return nil
	}
	if b.consumerBase != nil {
		if err := b.consumerBase.AttachObserver(collector); err != nil {
			if !errors.Is(err, outbox.ErrObserverAlreadyAttached) {
				return fmt.Errorf("bootstrap: attach outbox consumer observer: %w", err)
			}
		}
	}
	return nil
}

// checkConsumerBaseConfiguredForSubscriptions fails fast when cells registered
// subscriptions or projections but the ConsumerBase wired via WithConsumerBase
// is missing or is a zero-value `&ConsumerBase{}` literal. This keeps
// idempotency and retry lifecycle wiring explicit instead of silently consuming
// with a misconfigured ConsumerBase.
//
// Both subscriptions (reg.Subscribe) and projections (reg.RegisterProjection,
// which become event subscriptions once drained by buildProjectionCoordinators)
// walk the same ConsumerBase-backed consumption path. A projection-only
// deployment must not bypass this guard — mirroring checkNoEventConsumersWhenSubscriberNil
// which already checks both snap.Subscriptions and snap.Projections.
//
// N8 (b): the IsConstructed sentinel rejects literals even when they are
// non-nil — a `&outbox.ConsumerBase{}` would previously slip past the bare
// nil check, run with claimer=nil/ClaimRetryCount=0, and silently emit
// retryLoop=0 → ClaimAcquired+nil receipt → DispositionReject/DLX paths
// (PR#374 review finding (a)).
func (b *Bootstrap) checkConsumerBaseConfiguredForSubscriptions(s *phaseState) error {
	if b.consumerBase != nil && b.consumerBase.IsConstructed() {
		return nil
	}
	for _, id := range s.asm.CellIDs() {
		snap, ok := s.cellSnapshots[id]
		if !ok {
			continue
		}
		for _, sub := range snap.Subscriptions {
			if b.consumerBase == nil {
				return fmt.Errorf(
					"bootstrap: cell %s registered subscription topic %q but no ConsumerBase is configured; "+
						"add WithConsumerBase to bootstrap options", id, sub.Spec.Topic)
			}
			return fmt.Errorf(
				"bootstrap: cell %s registered subscription topic %q but ConsumerBase (%T) was not constructed via "+
					"outbox.NewConsumerBase (got a zero-value `&outbox.ConsumerBase{}` literal); "+
					"call outbox.NewConsumerBase to obtain a properly initialized value",
				id, sub.Spec.Topic, b.consumerBase)
		}
		for _, proj := range snap.Projections {
			if b.consumerBase == nil {
				return fmt.Errorf(
					"bootstrap: cell %s registered projection topic %q but no ConsumerBase is configured; "+
						"projections consume via the same ConsumerBase path as subscriptions — "+
						"add WithConsumerBase to bootstrap options", id, proj.Spec.Topic)
			}
			return fmt.Errorf(
				"bootstrap: cell %s registered projection topic %q but ConsumerBase (%T) was not constructed via "+
					"outbox.NewConsumerBase (got a zero-value `&outbox.ConsumerBase{}` literal); "+
					"call outbox.NewConsumerBase to obtain a properly initialized value",
				id, proj.Spec.Topic, b.consumerBase)
		}
	}
	return nil
}
