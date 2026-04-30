package bootstrap

// options_events.go — With* option functions covering outbox pubsub, event
// router, and worker registration.
//
// Covers: WithWorkers, WithPublisher, WithSubscriber, WithConsumerMiddleware,
// WithEventRouterReadyTimeout.
//
// ref: ThreeDotsLabs/watermill message/router.go — AddMiddleware wraps handlers
// at router level; pipeline middleware at receive-endpoint configuration.
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.

import (
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/worker"
)

// WithWorkers adds background workers.
func WithWorkers(ws ...worker.Worker) Option {
	return func(b *Bootstrap) {
		b.workers = append(b.workers, ws...)
	}
}

// WithPublisher sets the outbox.Publisher used for event publishing.
//
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.
func WithPublisher(p outbox.Publisher) Option {
	return func(b *Bootstrap) {
		b.publisher = p
	}
}

// WithSubscriber sets the outbox.Subscriber used for event consumption.
//
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.
func WithSubscriber(s outbox.Subscriber) Option {
	return func(b *Bootstrap) {
		b.subscriber = s
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
		b.consumerMiddleware = append(b.consumerMiddleware, mw...)
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
		b.routerReadyTimeoutSet = true
		b.routerReadyTimeout = d
	}
}

// WithSubscriptionValidator registers a registration-time subscription
// validator that is invoked by the EventRouter for every
// Cell.RegisterSubscriptions call. A non-nil error from any validator
// fails the subscription registration and surfaces during phase6 startup.
//
// Composition roots use this to enforce domain-specific invariants without
// polluting Cell code with infrastructure concerns. Nil validators are silently
// ignored.
//
// ref: Finding 2 (PR #334 L4) — fail at registration boundary, not delivery time.
func WithSubscriptionValidator(v ...cell.SubscriptionValidator) Option {
	return func(b *Bootstrap) {
		b.subscriptionValidators = append(b.subscriptionValidators, v...)
	}
}
