//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cellmodules/eventtransport"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/tests/testutil/rabbitmqctr"
)

// TestEventTransport_PostgresResolvesLiveRabbitMQ is the #1940 "complete switch"
// e2e: in postgres topology eventtransport.Resolve must dial a REAL RabbitMQ
// broker and return a live publisher/subscriber transport (no in-memory bus). It
// verifies the wiring beyond compile-time:
//
//   - Resolve succeeds against a live broker (real dial, not the fake-dial unit path).
//   - The resolved connection is healthy (its readiness probe passes) — a
//     constructed-but-dead connection, which the fake-dial unit test cannot
//     distinguish, would fail here.
//   - The resolved Publisher confirm-publishes to the live broker on a topology
//     the resolved Subscriber declared — i.e. the relay→broker publish path works
//     end-to-end against a real server.
//
// The full pub/sub delivery round-trip (DLX, redelivery, prefetch, settlement) is
// owned and exhaustively tested by adapters/rabbitmq; this test covers the
// eventtransport seam (Resolve → live broker transport), which is exactly the
// surface #1940 added. It lives in tests/integration (the sanctioned heavy-edge
// integration module) rather than cmd/corebundle so the broker testcontainer edge
// stays out of the production composition-root module graph (#1944 hygiene).
func TestEventTransport_PostgresResolvesLiveRabbitMQ(t *testing.T) {
	ctx := context.Background()
	container := rabbitmqctr.StartRabbitMQContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	amqpURL, err := container.AmqpURL(ctx)
	require.NoError(t, err, "rabbitmq container amqp url")

	topo, err := bootstrap.NewTopology("real", "postgres", true)
	require.NoError(t, err, "postgres topology")

	tr, err := eventtransport.Resolve(clock.Real(), topo, eventtransport.Config{AMQPURL: amqpURL})
	require.NoError(t, err, "postgres topology must resolve a real broker transport against a live broker")
	require.NotNil(t, tr.Publisher, "resolved Publisher must be non-nil")
	require.NotNil(t, tr.Subscriber, "resolved Subscriber must be non-nil")
	require.Len(t, tr.Resources, 1, "rabbitmq transport returns its connection as one managed resource")
	t.Cleanup(func() { _ = tr.Resources[0].Close(ctx) })

	// Genuinely connected: the broker connection's readiness probe(s) pass against
	// the live broker.
	probes := tr.Resources[0].Probes()
	require.NotEmpty(t, probes, "broker connection contributes a readiness probe")
	for _, p := range probes {
		assert.NoError(t, p.Check(ctx), "broker readiness probe must pass against the live broker")
	}

	// The resolved Publisher publishes to the live broker: declare topology via the
	// resolved Subscriber, then confirm-publish an enveloped entry (the relay's
	// publish path, exercised against a real server).
	sub := outbox.Subscription{
		Topic:         "eventtransport.itest.events",
		ConsumerGroup: "eventtransport-itest",
		CellID:        "eventtransport-itest",
	}
	require.NoError(t, tr.Subscriber.Setup(ctx, sub), "Setup must declare broker topology on the live broker")

	entry, err := outbox.NewEntry(clock.Real(), ctx, "eventtransport.itest.created", []byte(`{"k":"v"}`))
	require.NoError(t, err, "build outbox entry")
	payload, err := outbox.MarshalEnvelope(entry)
	require.NoError(t, err, "marshal envelope")

	require.NoError(t, tr.Publisher.Publish(ctx, sub.Topic, payload),
		"the resolved Publisher must confirm a publish to the live broker (relay→broker path)")
}
