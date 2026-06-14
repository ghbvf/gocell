//go:build integration

package mqtt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
)

// TestMQTT_Conformance runs the kernel/outbox/outboxtest.TestPubSub conformance
// suite against the MQTT adapter over a real Mosquitto broker (testcontainers).
//
// Two impedance mismatches between the broker-agnostic suite and the MQTT
// adapter are bridged by thin wrappers, NOT by relaxing the production adapter:
//
//  1. Topic shape. outboxtest.TestTopic returns "test-<t.Name()>-<hex>", which
//     contains uppercase + "/" from subtest names — no lowercase MQTT namespace
//     (each level ^[a-z0-9_-]+$) can contain it, so the publisher's Mint would
//     reject it. topicMappingPublisher / conformanceSubscriber deterministically
//     map every topic to "<conformanceNamespace>/<sha256hex(topic)>" (collision-
//     free, valid charset, single level). The SAME map is applied on publish and
//     subscribe so messages still round-trip; distinct test topics still map to
//     distinct MQTT topics (isolation preserved).
//  2. Consumer group. The suite leaves Subscription.ConsumerGroup empty on most
//     paths, but MQTT shared subscriptions ("$share/{group}/{filter}") require a
//     non-empty group (MintFilter rejects empty). The wrapper injects a single
//     fixed conformanceDefaultGroup when empty — fixed (not derived from CellID)
//     so Subscribe and the Ready/Setup probe (waitForSubscription passes a
//     different CellID) resolve to the same readyKey(group, topic), and so the
//     two competing-consumer subscriptions share one group and compete.
//
// Features (Option C, ADR-050 §6):
//   - SupportsRequeue=false: MQTT is a transport, not a work queue; Requeue is
//     leave-unacked-for-reconnect (no prompt redelivery). The suite's
//     Requeue/zero-value/receipt-requeue subtests gate on this and skip.
//   - GuaranteedOrder=false: bounded concurrent dispatch + $share semantics give
//     no ordering guarantee.
//   - BroadcastSubscribe=false: $share is competing consumers, not fanout.
//   - SupportsReceipt=false: the raw Subscriber does not thread Receipt
//     (ConsumerBase middleware does), same as adapters/rabbitmq.
func TestMQTT_Conformance(t *testing.T) {
	outboxtest.TestPubSub(t, outboxtest.Features{
		GuaranteedOrder:    false,
		SupportsRequeue:    false,
		SupportsReject:     true,
		SupportsReceipt:    false,
		BlockingSubscribe:  true,
		BroadcastSubscribe: false,
	}, func(t *testing.T) (outbox.Publisher, outbox.Subscriber) {
		// autopaho binds the ConnectionManager lifecycle to the ctx passed to
		// Open; it must outlive this constructor, so scope cancel to t.Cleanup —
		// a defer would cancel the moment this closure returns, killing the conn
		// before the suite uses it.
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)

		conn, err := Open(ctx, clock.Real(), newITestConfig(t, "conformance"))
		require.NoError(t, err, "Open conformance connection")
		t.Cleanup(func() {
			closeCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
			defer c()
			_ = conn.Close(closeCtx)
		})

		ns, err := ParseTopicNamespace(conformanceNamespace)
		require.NoError(t, err, "ParseTopicNamespace")

		pub, err := NewPublisher(clock.Real(), conn, ns)
		require.NoError(t, err, "NewPublisher")
		sub, err := NewSubscriber(clock.Real(), conn, ns, SubscriberConfig{})
		require.NoError(t, err, "NewSubscriber")
		t.Cleanup(func() { _ = sub.Close(context.Background()) })

		// outboxtest.PublishN wraps payloads in the v1 wire envelope; no extra
		// wrapper is needed here.
		return topicMappingPublisher{inner: pub}, conformanceSubscriber{inner: sub}
	})
}

const (
	// conformanceNamespace is the lowercase MQTT namespace all conformance topics
	// are mapped under (see mapConformanceTopic).
	conformanceNamespace = "ct"
	// conformanceDefaultGroup is injected when the suite leaves ConsumerGroup
	// empty. Fixed (not derived from CellID) so Subscribe and the Ready/Setup
	// probe — which passes a different CellID — resolve to the same group, and so
	// competing consumers share one group.
	conformanceDefaultGroup = "conformance"
)

// mapConformanceTopic maps an arbitrary outboxtest topic to a deterministic,
// namespace-valid MQTT topic "ct/<sha256hex>". Hex is [0-9a-f] (a valid MQTT
// level), the hash is collision-free for distinct topics, and the same input
// always maps to the same output so publish/subscribe round-trip.
func mapConformanceTopic(topic string) string {
	sum := sha256.Sum256([]byte(topic))
	return conformanceNamespace + "/" + hex.EncodeToString(sum[:])
}

// remapConformanceSubscription maps the subscription topic into the conformance
// namespace and injects the fixed default consumer group when empty.
func remapConformanceSubscription(sub outbox.Subscription) outbox.Subscription {
	sub.Topic = mapConformanceTopic(sub.Topic)
	if sub.ConsumerGroup == "" {
		sub.ConsumerGroup = conformanceDefaultGroup
	}
	return sub
}

// topicMappingPublisher adapts an MQTT Publisher to the conformance topic map.
type topicMappingPublisher struct{ inner outbox.Publisher }

func (p topicMappingPublisher) Publish(ctx context.Context, topic string, payload []byte) error {
	return p.inner.Publish(ctx, mapConformanceTopic(topic), payload)
}

func (p topicMappingPublisher) Close(ctx context.Context) error { return p.inner.Close(ctx) }

// conformanceSubscriber adapts an MQTT Subscriber: it maps the subscription
// topic and injects a default consumer group on every entry point so Setup,
// Ready, and Subscribe agree on the (group, topic) key.
type conformanceSubscriber struct{ inner outbox.Subscriber }

func (s conformanceSubscriber) Setup(ctx context.Context, sub outbox.Subscription) error {
	return s.inner.Setup(ctx, remapConformanceSubscription(sub))
}

func (s conformanceSubscriber) Ready(sub outbox.Subscription) <-chan struct{} {
	return s.inner.Ready(remapConformanceSubscription(sub))
}

func (s conformanceSubscriber) Subscribe(ctx context.Context, sub outbox.Subscription, handler outbox.SubscriberHandler) error {
	return s.inner.Subscribe(ctx, remapConformanceSubscription(sub), handler)
}

func (s conformanceSubscriber) Close(ctx context.Context) error { return s.inner.Close(ctx) }
