package bootstrap

// phases_events.go — event router startup and subscription validation (phase6).
//
// Covers:
//   - phase6StartEventRouter: subscription registration + evtRouter.Run on runCtx
//   - checkNoEventRegistrars: fail-fast when EventRegistrar cells have no subscriber
//
// ref: uber-go/fx app.go — Run vs stop ctx separation: event router uses runCtx
// (independent of external ctx) so lifecycle is owned by phase10 teardown, not
// by the caller cancelling the external context.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/assembly"
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
		return b.checkNoEventRegistrars(s.asm)
	}

	// Observability context restoration is the OUTERMOST step inside
	// SubscriberWithMiddleware.Subscribe — built-in invariant, not a
	// middleware here. ContractTracingMiddleware therefore observes a
	// ctx already populated with entry.Observability fields.
	mws := []outbox.SubscriptionMiddleware{
		eventrouter.ContractTracingMiddleware(b.http.wrapperTracer, b.http.errorRedactor),
	}
	mws = append(mws, b.events.consumerMiddleware...)

	var evtRouterOpts []eventrouter.Option
	if b.events.routerReadyTimeoutSet {
		evtRouterOpts = append(evtRouterOpts, eventrouter.WithReadyTimeout(b.events.routerReadyTimeout))
	}
	evtRouter := eventrouter.New(&outbox.SubscriberWithMiddleware{
		Inner:      sub,
		Middleware: mws,
	}, evtRouterOpts...)

	for _, id := range s.asm.CellIDs() {
		c := s.asm.Cell(id)
		if er, ok := c.(cell.EventRegistrar); ok {
			if err := er.RegisterSubscriptions(evtRouter); err != nil {
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

// checkNoEventRegistrars returns an error when any cell implements EventRegistrar
// but no subscriber is configured.
func (b *Bootstrap) checkNoEventRegistrars(asm *assembly.CoreAssembly) error {
	for _, id := range asm.CellIDs() {
		if _, ok := asm.Cell(id).(cell.EventRegistrar); ok {
			return fmt.Errorf(
				"bootstrap: cell %s implements EventRegistrar but no subscriber is configured; "+
					"add WithSubscriber to bootstrap options", id)
		}
	}
	return nil
}
