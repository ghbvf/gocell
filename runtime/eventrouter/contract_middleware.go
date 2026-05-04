package eventrouter

import (
	"context"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// ContractTracingMiddleware wraps contract-bound subscriptions with
// wrapper.WrapConsumer tracing. SubscriberWithMiddleware.Subscribe restores
// entry.Observability into ctx as the OUTERMOST step before any user
// middleware runs, so this middleware's span sees the originating
// trace/request metadata. ContractTracingMiddleware sits before ConsumerBase
// and covers idempotency, retries, skips, and final disposition downgrades.
//
// Router.AddContractHandler is the only registration entry point, and it
// validates the ContractSpec at registration time — no subscription reaches
// this middleware without a populated ContractID. wrapper.MustWrapConsumer's
// spec.Validate() is the second line of defense: if any caller ever bypasses
// AddContractHandler and still threads an empty-ID subscription through,
// WrapConsumer panics at construction time. The outbox.SubscriptionMiddleware
// closure has no error-return path; MustWrapConsumer matches the contract
// while keeping the spec-validation defense in depth.
//
// Error redaction is hardcoded inside wrapper.WrapConsumer (pkg/redaction);
// this middleware does not pipe a redactor — there is no caller-side opt-out.
//
// Settlement is captured from the inner SubscriberHandler call and forwarded
// to the caller. wrapper.MustWrapConsumer operates on an EntryHandler view;
// the Settlement is captured in a per-call local variable via the closure
// passed to MustWrapConsumer, which calls next once and stores its Settlement.
// The outer SubscriberHandler then returns the captured Settlement.
func ContractTracingMiddleware(tr wrapper.Tracer) outbox.SubscriptionMiddleware {
	return func(sub outbox.Subscription, next outbox.SubscriberHandler) outbox.SubscriberHandler {
		spec := wrapper.ContractSpec{
			ID:        sub.ContractID,
			Kind:      sub.ContractKind,
			Transport: sub.ContractTransport,
			Topic:     sub.Topic,
		}
		return func(ctx context.Context, entry outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
			// Capture settlement from the single next invocation. MustWrapConsumer
			// wraps a per-call EntryHandler closure that captures the settlement in
			// a local variable. The tracing span is started inside MustWrapConsumer,
			// so the traced context (including span) flows into next via ctx.
			//
			// We construct wrappedEntry per-delivery (not per-subscription) so each
			// delivery captures its own settlement without cross-delivery races.
			var capturedSettlement outbox.Settlement
			wrappedEntry := wrapper.MustWrapConsumer(tr, spec, func(innerCtx context.Context, e outbox.Entry) outbox.HandleResult {
				result, settlement := next(innerCtx, e)
				capturedSettlement = settlement
				return result
			})
			result := wrappedEntry(ctx, entry)
			return result, capturedSettlement
		}
	}
}
