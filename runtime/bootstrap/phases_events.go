package bootstrap

// phases_events.go — event router startup and subscription validation (phase6).
//
// Covers:
//   - phase6StartEventRouter: subscription registration + evtRouter.Run on runCtx
//   - checkNoEventRegistrars: fail-fast when EventRegistrar cells have no subscriber
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

	// Observability context restoration is the OUTERMOST step inside
	// SubscriberWithMiddleware.Subscribe — built-in invariant, not a
	// middleware here. ContractTracingMiddleware therefore observes a
	// ctx already populated with entry.Observability fields.
	mws := []outbox.SubscriptionMiddleware{
		eventrouter.ContractTracingMiddleware(b.wrapperTracer, b.errorRedactor),
	}
	mws = append(mws, b.consumerMiddleware...)

	var evtRouterOpts []eventrouter.Option
	if b.routerReadyTimeoutSet {
		evtRouterOpts = append(evtRouterOpts, eventrouter.WithReadyTimeout(b.routerReadyTimeout))
	}
	evtRouter := eventrouter.New(&outbox.SubscriberWithMiddleware{
		Inner:      sub,
		Middleware: mws,
	}, b.clock, evtRouterOpts...)

	for _, v := range b.subscriptionValidators {
		evtRouter.AddSubscriptionValidator(v)
	}

	for _, id := range s.asm.CellIDs() {
		snap, ok := s.cellSnapshots[id]
		if !ok {
			continue
		}
		for _, sub := range snap.Subscriptions {
			var opts []cell.SubscriptionOption
			if sub.SliceID != "" {
				opts = append(opts, cell.WithSubscriptionSliceID(sub.SliceID))
			}
			if err := evtRouter.AddContractHandler(sub.Spec, sub.Handler, sub.ConsumerGroup, opts...); err != nil {
				return fmt.Errorf("bootstrap: cell %s subscription setup failed: %w", id, err)
			}
		}
	}

	if evtRouter.HandlerCount() == 0 {
		return nil
	}

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
