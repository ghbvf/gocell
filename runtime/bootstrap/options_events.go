package bootstrap

// options_events.go — With* option functions for the bootstrapEvents group.
//
// Covers: WithWorkers, WithPublisher, WithSubscriber, WithConsumerMiddleware,
// WithEventRouterReadyTimeout, WithDisableObservabilityRestore.
//
// ref: ThreeDotsLabs/watermill message/router.go — AddMiddleware wraps handlers
// at router level; pipeline middleware at receive-endpoint configuration.
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.

import (
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/worker"
)

// WithWorkers adds background workers.
func WithWorkers(ws ...worker.Worker) Option {
	return func(b *Bootstrap) {
		b.events.workers = append(b.events.workers, ws...)
	}
}

// WithPublisher sets the outbox.Publisher used for event publishing.
//
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.
func WithPublisher(p outbox.Publisher) Option {
	return func(b *Bootstrap) {
		b.events.publisher = p
	}
}

// WithSubscriber sets the outbox.Subscriber used for event consumption.
//
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.
func WithSubscriber(s outbox.Subscriber) Option {
	return func(b *Bootstrap) {
		b.events.subscriber = s
	}
}

// WithConsumerMiddleware registers subscriber-side middleware applied to every
// topic handler before it is passed to the underlying Subscriber.Subscribe call.
// Middleware is applied in registration order; each entry wraps the next, so the
// first registered middleware is outermost at invocation time.
//
// Typical use: inject ConsumerBase.AsMiddleware so every consumer inherits
// two-phase Claimer idempotency, backoff retry, and DLX routing without each
// slice wiring it individually. Observability context restoration
// (entry.Observability → ctx) is the outermost step inside
// outbox.SubscriberWithMiddleware.Subscribe, so middleware registered here
// always sees a context populated with trace_id/request_id/correlation_id.
//
// ref: ThreeDotsLabs/watermill message/router.go — AddMiddleware wraps handlers
// at router level; MassTransit UseMessageRetry — pipeline middleware at
// receive-endpoint configuration.
func WithConsumerMiddleware(mw ...outbox.SubscriptionMiddleware) Option {
	return func(b *Bootstrap) {
		b.events.consumerMiddleware = append(b.events.consumerMiddleware, mw...)
	}
}

// WithEventRouterReadyTimeout overrides the EventRouter Phase-3 ready-wait
// budget. A non-positive value disables the bound (router waits indefinitely
// until ctx cancel). Default: eventrouter.DefaultReadyTimeout (30s).
//
// On timeout, Bootstrap.Run returns an error listing not-ready
// "consumerGroup/topic" pairs so operators can pinpoint the stuck subscription.
func WithEventRouterReadyTimeout(d time.Duration) Option {
	return func(b *Bootstrap) {
		b.events.routerReadyTimeoutSet = true
		b.events.routerReadyTimeout = d
	}
}

// WithDisableObservabilityRestore prevents the consumer-side
// ObservabilityContextMiddleware from restoring request_id / correlation_id /
// trace_id from outbox entry metadata into the handler context. The kill
// switch for the consume-side observability bridge — set this only when
// integrating with a custom observability stack that resets context keys
// itself.
func WithDisableObservabilityRestore() Option {
	return func(b *Bootstrap) {
		b.events.disableObservabilityRestore = true
	}
}
