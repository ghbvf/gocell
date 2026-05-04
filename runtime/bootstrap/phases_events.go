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

	evtRouter := b.buildEventRouter(sub)
	if err := b.drainCellSubscriptions(s, evtRouter); err != nil {
		return err
	}

	if evtRouter.HandlerCount() == 0 {
		return nil
	}

	return b.startAndRegisterEventRouter(runCtx, s, evtRouter)
}

// buildEventRouter creates the event router with middleware and validators.
//
// The SubscriberWithMiddleware wires three layers in order (innermost to outermost
// within SubscribeEntry):
//  1. Business middleware chain: ContractTracingMiddleware (outermost)
//     followed by b.consumerMiddleware (caller-supplied business middleware).
//     All operate on EntryHandler — they do not see Settlement.
//  2. ConsumerBase: the explicit EntryHandler→SubscriberHandler conversion
//     boundary. Field-injected via b.consumerBase (set by WithConsumerBase).
//  3. Observability restore: built-in OUTERMOST step in SubscribeEntry — always
//     applied after Inner.Subscribe is reached so every layer sees a populated ctx.
//
// ContractTracingMiddleware is outermost in the business middleware chain so the
// contract span covers idempotency retries, skips, and lease-lost downgrades.
// spec.Validate() panic fires at ContractTracingMiddleware construction (registration
// time), not at first delivery — restored from K#12 first-pass regression (P1 fix).
func (b *Bootstrap) buildEventRouter(sub outbox.Subscriber) *eventrouter.Router {
	// Business middleware chain: ContractTracingMiddleware is outermost so its
	// span covers everything inside (ConsumerBase retry, idempotency skips).
	// Caller-supplied middleware (b.consumerMiddleware) follows.
	mws := []outbox.SubscriptionMiddleware{
		eventrouter.ContractTracingMiddleware(b.wrapperTracer),
	}
	mws = append(mws, b.consumerMiddleware...)

	var evtRouterOpts []eventrouter.Option
	if b.routerReadyTimeoutSet {
		evtRouterOpts = append(evtRouterOpts, eventrouter.WithReadyTimeout(b.routerReadyTimeout))
	}
	evtRouter := eventrouter.New(&outbox.SubscriberWithMiddleware{
		Inner:        sub,
		Middleware:   mws,
		ConsumerBase: b.consumerBase, // nil-safe: degrades to nil-Settlement pass-through
	}, b.clock, evtRouterOpts...)

	for _, v := range b.subscriptionValidators {
		evtRouter.AddSubscriptionValidator(v)
	}
	return evtRouter
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
