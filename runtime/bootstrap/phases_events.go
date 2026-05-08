package bootstrap

// phases_events.go — event router startup and subscription validation (phase6).
//
// Covers:
//   - phase6StartEventRouter: subscription registration + evtRouter.Run on runCtx
//   - checkNoSubscriptionsWhenSubscriberNil: fail-fast when cells declared
//     subscriptions but no subscriber is configured
//
// ref: uber-go/fx app.go — Run vs stop ctx separation: event router uses runCtx
// (independent of external ctx) so lifecycle is owned by phase10 teardown, not
// by the caller canceling the external context.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/eventrouter"
)

// phase6StartEventRouter registers subscriptions and starts the event router
// using state.runCtx (independent of the external context).
//
// Key invariant: evtRouter.Run uses state.runCtx, NOT the external ctx.
// External ctx cancellation triggers phase9 → phase10 which calls evtRouter.Close;
// that closes runCtx internally, causing Run to return.
// ref: uber-go/fx app.go:L545-567 (run vs stop ctx separation).
func (b *Bootstrap) phase6StartEventRouter(runCtx context.Context, s *phaseState) error {
	sub := s.sub
	if sub == nil {
		return b.checkNoSubscriptionsWhenSubscriberNil(s)
	}
	if !cellSnapshotsHaveSubscriptions(s) {
		// No subscriptions to drain: skip router build entirely. Avoids
		// invoking NewSubscriberWithMiddleware (which requires a non-nil
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
func (b *Bootstrap) drainCellSubscriptions(s *phaseState, evtRouter *eventrouter.Router) error {
	for _, id := range s.asm.CellIDs() {
		snap, ok := s.cellSnapshots[id]
		if !ok {
			continue
		}
		for _, sub := range snap.Subscriptions {
			// Drain loop knows the true owner cell; set OwnerCellID so the
			// event router can record it independently of ConsumerGroup.
			sub.OwnerCellID = id
			var opts []cell.SubscriptionOption
			if sub.SliceID != "" {
				opts = append(opts, cell.WithSubscriptionSliceID(sub.SliceID))
			}
			if err := evtRouter.AddContractHandler(sub.Spec, sub.Handler, sub.ConsumerGroup, sub.OwnerCellID, opts...); err != nil {
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
	}); err != nil {
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

// checkNoSubscriptionsWhenSubscriberNil fails fast when any cell registered
// subscriptions (via reg.Subscribe in Init) but no subscriber is configured.
// This prevents silently dropping all event handlers when WithSubscriber is omitted.
func (b *Bootstrap) checkNoSubscriptionsWhenSubscriberNil(s *phaseState) error {
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
	}
	return nil
}

// checkConsumerBaseConfiguredForSubscriptions fails fast when cells registered
// subscriptions but the ConsumerBase wired via WithConsumerBase is missing or
// is a zero-value `&ConsumerBase{}` literal. This keeps idempotency and retry
// lifecycle wiring explicit instead of silently consuming with a misconfigured
// ConsumerBase.
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
	}
	return nil
}
