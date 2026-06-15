package rabbitmq

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
)

// TestSubscriber_ImplementsSerialInOrderGuarantor binds the capability the
// projection drain checks: the RabbitMQ subscriber advertises that it honors
// per-subscription serial mode. The implementer set is frozen by archtest
// PROJECTION-SERIAL-DELIVERY-ENFORCEMENT-01.
func TestSubscriber_ImplementsSerialInOrderGuarantor(t *testing.T) {
	t.Parallel()
	var sub outbox.Subscriber = &Subscriber{}
	g, ok := sub.(outbox.SerialInOrderGuarantor)
	require.True(t, ok, "rabbitmq.Subscriber must implement outbox.SerialInOrderGuarantor")
	assert.True(t, g.GuaranteesSerialInOrderDelivery(),
		"the subscriber honors per-subscription serial mode; the marker reports the capability")
}

// startSubscribeUntilQos drives Subscribe on its own goroutine and blocks until
// the consume setup (Qos + QueueDeclare) has run, then cancels and returns the
// mock channel for assertion. It is the shared harness for the serial-vs-concurrent
// QoS/topology assertions below.
func startSubscribeUntilQos(t *testing.T, cfg SubscriberConfig, sub outbox.Subscription) *mockChannel {
	t.Helper()
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	s := NewSubscriber(clock.Real(), conn, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	subDone := make(chan error, 1)
	go func() {
		subDone <- s.Subscribe(ctx, sub,
			entryToSubHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
				return outbox.Ack()
			}))
	}()

	// Qos runs before Consume in subscribeOnce, after QueueDeclare in declareTopology,
	// so once qosCalled is set both the prefetch and the queue args are recorded.
	testwait.External(t, "qos-called", func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.qosCalled
	}, testtime.D2s, testtime.FastPoll, "Qos was not called")

	cancel()
	assert.NoError(t, <-subDone)
	assert.NoError(t, s.Close(context.Background()))
	return ch
}

func queueDeclaredSingleActiveConsumer(ch *mockChannel) bool {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	for _, args := range ch.queueDeclareArgs {
		if v, ok := args["x-single-active-consumer"]; ok && v == true {
			return true
		}
	}
	return false
}

// TestSubscriber_SerialMode_ForcesPrefetch1AndSingleActiveConsumer proves the
// serial branch: a SerialMode subscription is consumed with prefetch=1 (overriding
// the configured PrefetchCount) and its queue is declared x-single-active-consumer.
// These are two of the three legs of the in-order guarantee the marker asserts
// (the third — synchronous single-flight dispatch — is proven by the integration
// suite's no-overlap test).
func TestSubscriber_SerialMode_ForcesPrefetch1AndSingleActiveConsumer(t *testing.T) {
	t.Parallel()
	ch := startSubscribeUntilQos(t,
		SubscriberConfig{QueueName: "proj-queue", PrefetchCount: 5, DLXExchange: "test.dlx"},
		outbox.Subscription{Topic: "test.topic", ConsumerGroup: "cell-proj", CellID: "cell", SerialMode: true})

	ch.mu.Lock()
	prefetch := ch.qosPrefetch
	ch.mu.Unlock()
	assert.Equal(t, 1, prefetch, "serial mode must force prefetch=1 even though PrefetchCount was 5")
	assert.True(t, queueDeclaredSingleActiveConsumer(ch),
		"serial mode must declare the queue x-single-active-consumer")
}

// TestSubscriber_NonSerial_KeepsConfiguredPrefetchAndNoSAC is the control: an
// ordinary subscription keeps the configured prefetch and a plain (non-SAC) queue,
// so the serial narrowing is strictly opt-in and does not regress throughput.
func TestSubscriber_NonSerial_KeepsConfiguredPrefetchAndNoSAC(t *testing.T) {
	t.Parallel()
	ch := startSubscribeUntilQos(t,
		SubscriberConfig{QueueName: "evt-queue", PrefetchCount: 5, DLXExchange: "test.dlx"},
		outbox.Subscription{Topic: "test.topic", ConsumerGroup: "cell-sub", CellID: "cell"})

	ch.mu.Lock()
	prefetch := ch.qosPrefetch
	ch.mu.Unlock()
	assert.Equal(t, 5, prefetch, "ordinary subscription keeps the configured prefetch")
	assert.False(t, queueDeclaredSingleActiveConsumer(ch),
		"ordinary subscription must NOT declare x-single-active-consumer")
}
