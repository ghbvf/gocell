//go:build integration

package rabbitmq

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
)

// publishIndexed publishes n v1-envelope entries whose IDs encode a strictly
// ascending index "evt-%04d". They are published sequentially on one publisher,
// so the broker enqueues them in index order — the order a serial subscription
// must then deliver them in.
func publishIndexed(t *testing.T, pub *Publisher, topic string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		entry := mustNewEntry(t, "serial.indexed",
			[]byte(`{"i":`+strconv.Itoa(i)+`}`),
			outbox.WithID(fmt.Sprintf("evt-%04d", i)),
			outbox.WithCreatedAt(time.Now().UTC()))
		payload, err := outbox.MarshalEnvelope(entry)
		require.NoError(t, err)
		require.NoError(t, pub.Publish(context.Background(), topic, payload), "publish evt-%04d", i)
	}
}

// serialIndexOf parses the ascending index out of an "evt-%04d" entry ID.
func serialIndexOf(t *testing.T, e outbox.Entry) int {
	t.Helper()
	i, err := strconv.Atoi(strings.TrimPrefix(e.ID(), "evt-"))
	require.NoError(t, err, "entry ID %q must be evt-NNNN", e.ID())
	return i
}

// TestIntegration_SerialMode_StrictOrderAndSingleFlight proves the two of the three
// serial legs that need a live broker: a SerialMode subscription delivers a single
// stream strictly in publish order (FIFO), and never runs two handlers at once
// (single-flight). The handler sleeps briefly so a concurrent (goroutine-per-delivery)
// dispatch would overlap; maxActive must stay 1.
func TestIntegration_SerialMode_StrictOrderAndSingleFlight(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	const n = 25
	topic := "test.serial.order"
	queueName := "test.serial.order.q"
	pub := NewPublisher(clock.Real(), conn)
	sub := NewSubscriber(clock.Real(), conn, SubscriberConfig{
		QueueName: queueName, PrefetchCount: 10, DLXExchange: "test.dlx",
	})

	var (
		mu        sync.Mutex
		gotOrder  []int
		active    atomic.Int32
		maxActive atomic.Int32
	)
	handler := func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		if cur := active.Add(1); cur > maxActive.Load() {
			maxActive.Store(cur)
		}
		defer active.Add(-1)
		time.Sleep(testtime.FastPoll) // widen the overlap window
		mu.Lock()
		gotOrder = append(gotOrder, serialIndexOf(t, e))
		mu.Unlock()
		return outbox.Ack()
	}

	subCtx, subCancel := context.WithTimeout(context.Background(), testtime.D30s)
	defer subCancel()
	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- sub.Subscribe(subCtx, outbox.Subscription{
			Topic: topic, ConsumerGroup: "cell-serial", CellID: "cell", SerialMode: true,
		}, entryToSubHandler(handler))
	}()
	waitForSubscriberReady(t, conn, queueName, subErrCh, testtime.EventuallyLong)

	publishIndexed(t, pub, topic, n)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(gotOrder) == n
	}, testtime.D30s, testtime.FastPoll, "all %d serial messages must be delivered", n)

	subCancel()
	assert.NoError(t, <-subErrCh)

	mu.Lock()
	defer mu.Unlock()
	want := make([]int, n)
	for i := range want {
		want[i] = i
	}
	assert.Equal(t, want, gotOrder, "serial subscription must deliver strictly in publish (FIFO) order")
	assert.Equal(t, int32(1), maxActive.Load(), "serial subscription must never run two handlers concurrently (single-flight)")
}

// TestIntegration_SerialMode_RequeuePreservesOrderNoGap is the decision-#2
// anti-regression: a transient requeue must NOT reorder the stream or skip a
// position. One message requeues once (in-place Nack(requeue=true) → head re-entry
// under prefetch=1), then succeeds. The successful-apply order must remain the
// complete, gap-free ascending sequence — the requeued index re-applied before its
// successor. A delay-tier requeue (tail re-entry) would fail this; serial mode
// forbids that schedule, so the in-place path is the only one taken.
func TestIntegration_SerialMode_RequeuePreservesOrderNoGap(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	const n = 12
	const requeueOnce = 6 // the index that fails its first delivery, then succeeds
	topic := "test.serial.requeue"
	queueName := "test.serial.requeue.q"
	pub := NewPublisher(clock.Real(), conn)
	sub := NewSubscriber(clock.Real(), conn, SubscriberConfig{
		QueueName: queueName, PrefetchCount: 5, DLXExchange: "test.dlx",
	})

	var (
		mu       sync.Mutex
		acked    []int           // indices in successful-apply order
		attempts = map[int]int{} // per-index delivery count
		requeued atomic.Bool
	)
	handler := func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		idx := serialIndexOf(t, e)
		mu.Lock()
		attempts[idx]++
		first := attempts[idx] == 1
		mu.Unlock()
		if idx == requeueOnce && first {
			requeued.Store(true)
			return outbox.Requeue(fmt.Errorf("transient: force one requeue of index %d", idx))
		}
		mu.Lock()
		acked = append(acked, idx)
		mu.Unlock()
		return outbox.Ack()
	}

	subCtx, subCancel := context.WithTimeout(context.Background(), testtime.D30s)
	defer subCancel()
	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- sub.Subscribe(subCtx, outbox.Subscription{
			Topic: topic, ConsumerGroup: "cell-serial-rq", CellID: "cell", SerialMode: true,
		}, entryToSubHandler(handler))
	}()
	waitForSubscriberReady(t, conn, queueName, subErrCh, testtime.EventuallyLong)

	publishIndexed(t, pub, topic, n)

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(acked) == n
	}, testtime.D30s, testtime.FastPoll, "all %d messages must eventually be acked", n)

	subCancel()
	assert.NoError(t, <-subErrCh)

	mu.Lock()
	defer mu.Unlock()
	assert.True(t, requeued.Load(), "the test must have exercised at least one requeue")
	assert.Equal(t, 2, attempts[requeueOnce], "the requeued index must be delivered exactly twice (fail, then succeed)")
	want := make([]int, n)
	for i := range want {
		want[i] = i
	}
	assert.Equal(t, want, acked,
		"in-place requeue under prefetch=1 must preserve strict order with no gap: the requeued index "+
			"re-applies at the head before its successor, so the successful-apply order is the full ascending sequence")
}

// TestIntegration_SerialMode_SingleActiveConsumer proves the cross-pod leg: two
// subscribers on the same serial (x-single-active-consumer) queue → exactly one is
// active, so all deliveries land on a single consumer and the stream is never split.
func TestIntegration_SerialMode_SingleActiveConsumer(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	const n = 15
	topic := "test.serial.sac"
	queueName := "test.serial.sac.q"
	pub := NewPublisher(clock.Real(), conn)

	var countA, countB atomic.Int32
	start := func(counter *atomic.Int32) (context.CancelFunc, <-chan error) {
		s := NewSubscriber(clock.Real(), conn, SubscriberConfig{
			QueueName: queueName, PrefetchCount: 1, DLXExchange: "test.dlx",
		})
		ctx, cancel := context.WithTimeout(context.Background(), testtime.D30s)
		errCh := make(chan error, 1)
		go func() {
			errCh <- s.Subscribe(ctx, outbox.Subscription{
				Topic: topic, ConsumerGroup: "cell-sac", CellID: "cell", SerialMode: true,
			}, entryToSubHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
				counter.Add(1)
				return outbox.Ack()
			}))
		}()
		return cancel, errCh
	}

	cancelA, errA := start(&countA)
	defer cancelA()
	waitForSubscriberReady(t, conn, queueName, errA, testtime.EventuallyLong)
	// Second consumer joins the same SAC queue; it must stay on standby.
	cancelB, errB := start(&countB)
	defer cancelB()

	publishIndexed(t, pub, topic, n)

	require.Eventually(t, func() bool {
		return countA.Load()+countB.Load() == int32(n)
	}, testtime.D30s, testtime.FastPoll, "all %d messages must be delivered to the active consumer", n)

	cancelA()
	cancelB()
	assert.NoError(t, <-errA)
	assert.NoError(t, <-errB)

	a, b := countA.Load(), countB.Load()
	assert.Equal(t, int32(n), a+b, "every message delivered exactly once")
	assert.True(t, a == 0 || b == 0,
		"x-single-active-consumer must keep exactly one consumer active; the stream must not be split (a=%d, b=%d)", a, b)
}
