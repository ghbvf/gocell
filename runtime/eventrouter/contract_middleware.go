package eventrouter

import (
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// ContractTracingMiddleware wraps contract-bound subscriptions with
// wrapper.WrapConsumer. It is intended to sit after
// outbox.ObservabilityContextMiddleware and before ConsumerBase so the span
// sees restored trace/request metadata and covers idempotency, retries,
// skips, and final disposition downgrades.
func ContractTracingMiddleware(tr wrapper.Tracer, redactor wrapper.ErrorRedactor) outbox.SubscriptionMiddleware {
	return func(sub outbox.Subscription, next outbox.EntryHandler) outbox.EntryHandler {
		if sub.ContractID == "" {
			return next
		}
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
		return wrapper.WrapConsumer(tr, spec, next, opts...)
	}
}
