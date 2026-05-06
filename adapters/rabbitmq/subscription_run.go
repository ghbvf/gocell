package rabbitmq

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
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
//	(c) localizes cleanup so Close can iterate runs without juggling three tables.
//
// ref: nats-io/nats.go Subscription per-subscription state encapsulation
// ref: uber-go/fx per-component Lifecycle (each component owns its teardown)
// ref: rabbitmq/amqp091-go channel.go — Cancel → drain → wg.Wait → ch.Close ordering
type subscriptionRun struct {
	ch          AMQPChannel
	consumerTag string
	// conn is the Connection that allocated ch. Used by waitAndClose to call
	// CloseEphemeralChannel (the single canonical AMQPChannel destruction path)
	// so that inUseChannels is decremented on every subscription teardown.
	conn    *Connection
	localWg sync.WaitGroup // tracks processDelivery goroutines of this run only
	// inflight is an atomic counter mirroring localWg adds/dones. It is the
	// authoritative signal observed by Subscriber.StopIntake Phase 2 (which
	// polls it instead of calling localWg.Wait, to avoid the Add-after-Wait
	// race against drainRemaining's concurrent registerDelivery).
	// localWg remains the synchronization barrier only for waitAndClose,
	// which is invoked AFTER consumeLoop has returned (so no concurrent Add
	// can occur).
	inflight atomic.Int64
	closed   sync.Once
	// wgDoneCh is closed exactly once when all in-flight deliveries have
	// completed (localWg.Wait() returns in any wg-waiter goroutine).
	// Initialized by newSubscriptionRun; closeWgDone ensures only one of the
	// potentially concurrent wg-waiters closes it.
	wgDoneCh    chan struct{}
	closeWgDone sync.Once
}

// newSubscriptionRun creates a subscriptionRun for the given channel, consumer tag,
// and the Connection that allocated the channel. conn is required so that
// waitAndClose can call conn.CloseEphemeralChannel (the single canonical
// AMQPChannel destruction path) to correctly decrement inUseChannels.
func newSubscriptionRun(ch AMQPChannel, tag string, conn *Connection) *subscriptionRun {
	return &subscriptionRun{
		ch:          ch,
		consumerTag: tag,
		conn:        conn,
		wgDoneCh:    make(chan struct{}),
	}
}

// registerDelivery marks one in-flight processDelivery goroutine started.
// Must be called before spawning the goroutine, inside the same goroutine that
// calls localWg.Wait() to avoid an Add-after-Wait race.
func (r *subscriptionRun) registerDelivery() {
	r.localWg.Add(1)
	r.inflight.Add(1)
}

// markDeliveryDone marks one processDelivery goroutine as finished.
// Must be called with defer inside each processDelivery goroutine.
func (r *subscriptionRun) markDeliveryDone() {
	r.localWg.Done()
	r.inflight.Add(-1)
}

// inflightCount returns the current number of in-flight processDelivery
// goroutines. Used for diagnostic logging only — use localWg.Wait() for
// synchronization barriers.
func (r *subscriptionRun) inflightCount() int64 {
	return r.inflight.Load()
}

// waitAndClose drains in-flight deliveries then closes the AMQP channel exactly once.
//
// Phase 1: waits for localWg (all processDelivery goroutines of this run).
// If ctx expires before all goroutines complete, returns ctx.Err() immediately
// without closing the channel — the channel will be abandoned (process-exit
// cleanup semantics, matching the existing Close timeout path). The internal
// wg-waiter goroutine continues running until localWg.Wait() returns; callers
// that need to observe its exit (e.g. leak tests) should use wgDone().
//
// Phase 2: closes the AMQP channel via sync.Once so that concurrent callers
// (subscribeOnce exit path + Subscriber.Close) cannot double-close.
//
// ref: rabbitmq/amqp091-go channel.go IsClosed short-circuit on double close
// ref: ThreeDotsLabs/watermill-amqp subscriber.go — closedChan→WaitGroup→ch.Close
func (r *subscriptionRun) waitAndClose(ctx context.Context) error {
	// Contract: callers MUST guarantee that no further registerDelivery will
	// occur for this run before invoking waitAndClose. Production callers
	// satisfy this transitively — subscribeOnce only calls waitAndClose after
	// consumeLoop returns, and Subscriber.Close only sweeps the runs map after
	// s.wg.Wait has drained every processDelivery goroutine. With that
	// guarantee, the localWg.Wait below cannot race with a concurrent Add.
	// Subscriber.StopIntake intentionally does NOT call localWg.Wait — it polls
	// the atomic inflight counter directly, which is the canonical way to
	// observe drain progress while drainRemaining may still be dispatching.
	//
	// Phase 1: wait for in-flight deliveries bounded by ctx.
	// The wg-waiter goroutine closes r.wgDoneCh when localWg.Wait() returns,
	// providing a happens-before signal for goroutine-exit assertions in tests.
	// wgDoneCh is initialized by newSubscriptionRun so this goroutine captures
	// a stable reference with no data race even when waitAndClose is called
	// concurrently from subscribeOnce and Subscriber.Close.
	// closeWgDone ensures only one of the concurrent wg-waiters closes the channel.
	go func() {
		r.localWg.Wait()
		r.closeWgDone.Do(func() { close(r.wgDoneCh) })
	}()

	select {
	case <-r.wgDoneCh:
	case <-ctx.Done():
		slog.Warn("rabbitmq: subscriptionRun wait-inflight ctx expired",
			slog.String("consumer_tag", r.consumerTag),
			slog.Any("error", ctx.Err()))
		return ctx.Err()
	}

	// Phase 2: close the AMQP channel exactly once via the canonical destruction
	// path, which also decrements inUseChannels to release the cap slot.
	// conn is nil only in unit tests that construct subscriptionRun directly
	// without going through subscribeOnce; in that case fall back to a bare
	// ch.Close() so the WaitGroup/close-once mechanics are still testable.
	r.closed.Do(func() {
		if r.conn != nil {
			r.conn.CloseEphemeralChannel(r.ch)
		} else {
			_ = r.ch.Close()
		}
	})
	return nil
}

// wgDone returns a channel that is closed when all in-flight deliveries have
// completed (localWg.Wait() has returned in the wg-waiter goroutine spawned by
// waitAndClose). The channel is ready as soon as newSubscriptionRun returns;
// it is closed at most once regardless of concurrent waitAndClose calls.
func (r *subscriptionRun) wgDone() <-chan struct{} {
	return r.wgDoneCh
}

// cancelWithBudget issues basic.cancel for this run's consumer with per-call timeout.
// Does NOT close the channel; that is waitAndClose's responsibility.
func (r *subscriptionRun) cancelWithBudget(ctx context.Context, perCallTimeout time.Duration) {
	cancelConsumerWithBudget(ctx, consumerRef{ch: r.ch, tag: r.consumerTag}, perCallTimeout)
}
