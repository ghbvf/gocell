//go:build integration

package rabbitmq

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
	outboxrt "github.com/ghbvf/gocell/runtime/outbox"
)

// envelopingPublisher wraps a raw Publisher to serialize payloads into the
// outboxrt.WireMessage envelope expected by the RabbitMQ subscriber's
// unmarshalDelivery. Without this wrapper, the conformance harness publishes
// bare JSON payloads (e.g., {"seq":0}), but the subscriber expects an envelope
// with id, eventType, and an embedded payload field.
//
// This is NOT needed in production — the outbox relay serializes entries
// into the wire format. It is only needed for conformance tests that bypass
// the relay and test Publisher+Subscriber directly.
type envelopingPublisher struct {
	inner outbox.Publisher
}

func (p *envelopingPublisher) Publish(ctx context.Context, topic string, payload []byte) error {
	entry := outboxtest.NewEntry(topic, payload)
	wire := outboxrt.WireMessage{
		SchemaVersion: outboxrt.EnvelopeSchemaV1,
		ID:            entry.ID,
		EventType:     entry.EventType,
		Topic:         entry.Topic,
		Payload:       json.RawMessage(payload),
		CreatedAt:     entry.CreatedAt,
	}
	body, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	return p.inner.Publish(ctx, topic, body)
}

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
		return &envelopingPublisher{inner: pub}, sub
	})
}
