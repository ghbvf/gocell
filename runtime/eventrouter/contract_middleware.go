package eventrouter

import (
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// ContractTracingMiddleware wraps contract-bound subscriptions with
// wrapper.WrapConsumer tracing. SubscriberWithMiddleware.SubscribeEntry restores
// entry.Observability into ctx as the OUTERMOST step before any user
// middleware runs, so this middleware's span sees the originating
// trace/request metadata.
//
// The contract span covers idempotency, retries, skips, and final disposition
// downgrades (ConsumerBase is wired inside SubscriberWithMiddleware.SubscribeEntry,
// after the business middleware chain — so the span wraps everything).
//
// wrapper.MustWrapConsumer is called ONCE at middleware construction (not per
// delivery). spec.Validate() panic therefore fires at subscription registration
// time, not at first delivery. Router.AddContractHandler validates the
// ContractSpec before reaching this middleware, providing defense in depth.
//
// Error redaction is hardcoded inside wrapper.WrapConsumer (pkg/redaction);
// this middleware does not pipe a redactor — there is no caller-side opt-out.
//
// ref: ThreeDotsLabs/watermill message/router.go — middleware wraps handler
// at registration time, not per message.
// ref: go-kratos/kratos middleware/tracing — span created in middleware
// constructor; per-request span started inside the returned handler.
func ContractTracingMiddleware(tr wrapper.Tracer) outbox.SubscriptionMiddleware {
	return func(sub outbox.Subscription, next outbox.EntryHandler) outbox.EntryHandler {
		spec := wrapper.ContractSpec{
			ID:        sub.ContractID,
			Kind:      sub.ContractKind,
			Transport: sub.ContractTransport,
			Topic:     sub.Topic,
		}
		// spec.Validate() panic fires here at middleware construction (registration
		// time), not at first delivery. Router.AddContractHandler's spec.Validate()
		// is the primary guard; MustWrapConsumer is the second line of defense.
		return wrapper.MustWrapConsumer(tr, spec, next)
	}
}
