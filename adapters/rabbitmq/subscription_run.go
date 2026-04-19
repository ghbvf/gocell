package rabbitmq

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// subscriptionRun encapsulates all state of a single subscribeOnce invocation:
// the AMQP channel, the consumer tag used for basic.cancel, and a local
// WaitGroup tracking processDelivery goroutines spawned during THIS run.
//
// Design rationale: the Subscriber previously maintained three parallel
// structures (s.channels, s.consumerTags map, shared s.wg) that were all
// semantically "per subscribeOnce". Encapsulation here:
//
//	(a) eliminates the invariant "keys across three tables must agree",
//	(b) makes the reconnect path's A19 barrier trivial — localWg.Wait()
//	    before ch.Close, so no processDelivery goroutine races against
//	    a closed channel,
//	(c) localises cleanup so Close can iterate runs without juggling three tables.
//
// ref: nats-io/nats.go Subscription per-subscription state encapsulation
// ref: uber-go/fx per-component Lifecycle (each component owns its teardown)
// ref: rabbitmq/amqp091-go channel.go — Cancel → drain → wg.Wait → ch.Close ordering
type subscriptionRun struct {
	ch          AMQPChannel
	consumerTag string
	localWg     sync.WaitGroup // tracks processDelivery goroutines of this run only
	closed      sync.Once
}

// newSubscriptionRun creates a subscriptionRun for the given channel and consumer tag.
func newSubscriptionRun(ch AMQPChannel, tag string) *subscriptionRun {
	return &subscriptionRun{ch: ch, consumerTag: tag}
}

// registerDelivery marks one in-flight processDelivery goroutine started.
// Must be called before spawning the goroutine, inside the same goroutine that
// calls localWg.Wait() to avoid an Add-after-Wait race.
func (r *subscriptionRun) registerDelivery() {
	r.localWg.Add(1)
}

// markDeliveryDone marks one processDelivery goroutine as finished.
// Must be called with defer inside each processDelivery goroutine.
func (r *subscriptionRun) markDeliveryDone() {
	r.localWg.Done()
}

// waitAndClose drains in-flight deliveries then closes the AMQP channel exactly once.
//
// Phase 1: waits for localWg (all processDelivery goroutines of this run).
// If ctx expires before all goroutines complete, returns ctx.Err() immediately
// without closing the channel — the channel will be abandoned (process-exit
// cleanup semantics, matching the existing Close timeout path).
//
// Phase 2: closes the AMQP channel via sync.Once so that concurrent callers
// (subscribeOnce exit path + Subscriber.Close) cannot double-close.
//
// ref: rabbitmq/amqp091-go channel.go IsClosed short-circuit on double close
// ref: ThreeDotsLabs/watermill-amqp subscriber.go — closedChan→WaitGroup→ch.Close
func (r *subscriptionRun) waitAndClose(ctx context.Context) error {
	// Phase 1: wait for in-flight deliveries bounded by ctx.
	done := make(chan struct{})
	go func() {
		r.localWg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		slog.Warn("rabbitmq: subscriptionRun wait-inflight ctx expired",
			slog.String("consumer_tag", r.consumerTag),
			slog.Any("error", ctx.Err()))
		return ctx.Err()
	}

	// Phase 2: close the AMQP channel exactly once.
	var closeErr error
	r.closed.Do(func() {
		if err := r.ch.Close(); err != nil {
			slog.Debug("rabbitmq: subscriptionRun ch.Close error",
				slog.String("consumer_tag", r.consumerTag),
				slog.Any("error", err))
			closeErr = err
		}
	})
	return closeErr
}

// cancelWithBudget issues basic.cancel for this run's consumer with per-call timeout.
// Does NOT close the channel; that is waitAndClose's responsibility.
func (r *subscriptionRun) cancelWithBudget(ctx context.Context, perCallTimeout time.Duration) {
	cancelConsumerWithBudget(ctx, consumerRef{ch: r.ch, tag: r.consumerTag}, perCallTimeout)
}
