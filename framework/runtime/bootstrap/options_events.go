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

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
	runtimeoutbox "github.com/ghbvf/gocell/framework/runtime/outbox"
	"github.com/ghbvf/gocell/framework/runtime/worker"
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

// WithEventTransportKind declares whether the injected event transport is a real
// cross-process broker or an in-process bus, as resolved by
// cellmodules/eventtransport.Resolve (which mints the sealed EventTransportKind).
// The phase0 broker-mandatory gate (validateSplitTopologyBroker) requires
// IsRealBroker()==true for a split deployment topology; an unset kind (option
// omitted) is fail-closed. Colocated deployments may omit it — the gate does not
// fire without remote cells. The composition root threads the resolver's
// Transport.Kind here, so the gate trusts a type-system fact instead of the
// older StorageBackend proxy (#2211).
//
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.
func WithEventTransportKind(k EventTransportKind) Option {
	return func(b *Bootstrap) {
		b.eventTransportKind = k
	}
}

// WithConsumerMiddleware registers business subscriber-side middleware applied to
// every topic's EntryHandler before ConsumerBase idempotency is applied.
// Middleware is applied in registration order; each entry wraps the next, so the
// first registered middleware is outermost at invocation time.
//
// Business middleware operates on EntryHandler (not SubscriberHandler) — it
// does not see Settlement. ConsumerBase is field-injected into
// SubscriberWithMiddleware via WithConsumerBase and applied as the
// EntryHandler→SubscriberHandler conversion boundary after the business
// middleware chain. Observability context restoration (entry.Observability → ctx)
// is the outermost step inside SubscriberWithMiddleware.SubscribeEntry, so
// middleware registered here always sees a context populated with
// trace_id/request_id/correlation_id.
//
// ref: ThreeDotsLabs/watermill message/router.go — AddMiddleware wraps handlers
// at router level; MassTransit UseMessageRetry — pipeline middleware at
// receive-endpoint configuration.
func WithConsumerMiddleware(mw ...outbox.SubscriptionMiddleware) Option {
	return func(b *Bootstrap) {
		b.consumerMiddleware = append(b.consumerMiddleware, mw...)
	}
}

// WithConsumerBase injects a ConsumerBase into the SubscriberWithMiddleware used
// by the event router. The ConsumerBase is the explicit EntryHandler→SubscriberHandler
// conversion boundary: after the business middleware chain, ConsumerBase.Wrap
// adds idempotency (Claim/Commit/Release) and exponential-backoff retry.
//
// When any cell registers subscriptions, phase6 fails fast if this option was
// not supplied. Composition roots that consume events must wire a ConsumerBase
// explicitly so idempotency and final settlement lifecycle are not optional by
// accident.
//
// ref: uber-go/fx app.go Invoke — constructor-injected dependency, not
// middleware-position injection.
func WithConsumerBase(cb *outbox.ConsumerBase) Option {
	return func(b *Bootstrap) {
		b.consumerBase = cb
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

// WithRelay registers the relay BOTH for outbox wiring AND for lifecycle
// (Start/Stop driven through the package-private relayAdapter). Calling
// WithRelay is the ONLY supported path to integrate a relay; passing it to
// WithManagedResource is a compile-time type-mismatch — *runtimeoutbox.Relay
// does not implement kernel/lifecycle.ManagedResource. See ADR
// docs/architecture/202605201400-adr-relay-managedresource-isolation.md and
// archtest RELAY-NOT-MANAGEDRESOURCE-01.
//
// Calling WithRelay more than once is a programmer error and panics through
// the panic-taxonomy funnel (panicregister.Approved + errcode.Assertion, B
// class): the second call would silently overwrite b.relay while leaving the
// earlier relay registered in managedResources, hiding a double-managed
// resource that the previous runtime guard could not catch once the active
// b.relay pointer moved.
//
// Nil inputs are silently ignored (cumulative builder noop pattern,
// runtime-api.md §Option 范式分层): the relay remains unset, and
// autoWireOutboxRejectCollector skips relay-specific wiring.
//
// Must be called before Run(). Typical usage:
//
//	relay := runtimeoutbox.NewRelay(store, pub, cfg)
//	relay.WithPendingDepthObserver(pendingDepthCollector)
//	bootstrap.New(
//	    bootstrap.WithRelay(relay), // sole sanctioned entry; no WithManagedResource needed
//	    ...
//	)
func WithRelay(r *runtimeoutbox.Relay) Option {
	return func(b *Bootstrap) {
		if r == nil {
			return
		}
		if b.relay != nil {
			panic(panicregister.Approved("bootstrap-relay-rebind",
				errcode.Assertion("bootstrap: WithRelay called more than once; only one relay may be registered per Bootstrap")))
		}
		b.relay = r
		b.managedResources = append(b.managedResources, newRelayAdapter(r)) // auto-lifecycle via sole sanctioned holder
	}
}

// WithConfigEventCollector injects a ConfigEventCollector for config-event
// settlement observability. The collector is applied at the SubscriberHandler
// layer via WrapConfigEventSubscriber inside buildEventRouter, so settlement
// metrics are recorded after final broker disposition rather than inside the
// EntryHandler.
//
// Nil inputs are silently ignored (cumulative builder noop pattern). When this
// option is not called, NoopConfigEventCollector is used.
func WithConfigEventCollector(c obmetrics.ConfigEventCollector) Option {
	return func(b *Bootstrap) {
		if c == nil {
			return
		}
		b.configEventCollector = c
	}
}
