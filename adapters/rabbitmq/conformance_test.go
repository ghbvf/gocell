//go:build integration

package rabbitmq

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
)

// TestRabbitMQ_Conformance runs the full outboxtest conformance suite against
// a real RabbitMQ broker via testcontainers.
//
// Features:
//   - GuaranteedOrder:    true  — single consumer on a single queue is FIFO.
//   - SupportsRequeue:    true  — Nack(requeue=true) redelivers.
//   - SupportsReject:     true  — Nack(requeue=false) routes to DLX.
//   - SupportsReceipt:    false — raw Subscriber does not thread Receipt;
//     ConsumerBase middleware handles that.
//   - BlockingSubscribe:  true  — Subscribe blocks until ctx cancelled.
//   - BroadcastSubscribe: false — same queue = competing consumers, not fan-out.
func TestRabbitMQ_Conformance(t *testing.T) {
	// One testcontainer is shared (it is expensive to start) but each subtest
	// receives its own independent Connection. This prevents a prior subtest's
	// teardown (e.g. SubscribeBlocksUntilCancel) from leaving the shared
	// Connection in a reconnecting state that causes the next subtest's
	// InitializeSubscription → acquireChannel call to fail with
	// ERR_ADAPTER_AMQP_CONNECT "connection not available".
	brokerURL, containerCleanup := startRabbitMQBroker(t)
	t.Cleanup(containerCleanup)

	outboxtest.TestPubSub(t, outboxtest.Features{
		GuaranteedOrder:    true,
		SupportsRequeue:    true,
		SupportsReject:     true,
		SupportsReceipt:    false,
		BlockingSubscribe:  true,
		BroadcastSubscribe: false,
	}, func(t *testing.T) (outbox.Publisher, outbox.Subscriber) {
		// Fresh Connection per subtest: isolates connection state so one
		// subtest's teardown cannot bleed into the next.
		conn := newIntegrationConnection(t, brokerURL)

		pub := NewPublisher(conn)
		sub := NewSubscriber(conn, SubscriberConfig{
			DLXExchange:     "test.dlx",
			PrefetchCount:   1,
			ShutdownTimeout: 5 * time.Second,
		})
		t.Cleanup(func() { _ = sub.Close() })
		// outboxtest.PublishN wraps payloads in a v1 wire envelope, matching
		// the RabbitMQ subscriber's unmarshalDelivery contract — no additional
		// envelope wrapper is needed here.
		return pub, sub
	})
}
