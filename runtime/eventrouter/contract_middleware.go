package eventrouter

import (
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// ContractTracingMiddleware wraps contract-bound subscriptions with
// wrapper.WrapConsumer. SubscriberWithMiddleware.Subscribe restores
// entry.Observability into ctx as the OUTERMOST step before any user
// middleware runs, so this middleware's span sees the originating
// trace/request metadata. ContractTracingMiddleware sits before
// ConsumerBase and covers idempotency, retries, skips, and final
// disposition downgrades.
//
// Router.AddContractHandler is the only registration entry point, and it
// validates the ContractSpec at registration time — no subscription reaches
// this middleware without a populated ContractID. wrapper.MustWrapConsumer's
// spec.Validate() is the second line of defence: if any caller ever bypasses
// AddContractHandler and still threads an empty-ID subscription through,
// WrapConsumer panics at construction time. The outbox.SubscriptionMiddleware
// closure has no error-return path; MustWrapConsumer matches the contract
// while keeping the spec-validation defense in depth.
func ContractTracingMiddleware(tr wrapper.Tracer, redactor wrapper.ErrorRedactor) outbox.SubscriptionMiddleware {
	return func(sub outbox.Subscription, next outbox.EntryHandler) outbox.EntryHandler {
		spec := wrapper.ContractSpec{
			ID:        sub.ContractID,
			Kind:      sub.ContractKind,
			Transport: sub.ContractTransport,
			Topic:     sub.Topic,
		}
		var opts []wrapper.ConsumerOption
		if redactor != nil {
			opts = append(opts, wrapper.WithConsumerErrorRedactor(redactor))
		}
		return wrapper.MustWrapConsumer(tr, spec, next, opts...)
	}
}
