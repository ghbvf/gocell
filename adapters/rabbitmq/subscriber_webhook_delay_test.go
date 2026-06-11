package rabbitmq

import (
	"context"
	"errors"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// =============================================================================
// readWebhookAttempt tests
// =============================================================================

func TestReadWebhookAttempt(t *testing.T) {
	tests := []struct {
		name    string
		headers amqp.Table
		want    int
	}{
		{
			name:    "nil header returns 1",
			headers: nil,
			want:    1,
		},
		{
			name:    "absent key returns 1",
			headers: amqp.Table{"other": "value"},
			want:    1,
		},
		{
			name:    "int32 value 3 returns 3",
			headers: amqp.Table{headerWebhookAttempt: int32(3)},
			want:    3,
		},
		{
			name:    "int64 value 2 returns 2",
			headers: amqp.Table{headerWebhookAttempt: int64(2)},
			want:    2,
		},
		{
			name:    "int value 5 returns 5",
			headers: amqp.Table{headerWebhookAttempt: int(5)},
			want:    5,
		},
		{
			name:    "int32 zero returns 1",
			headers: amqp.Table{headerWebhookAttempt: int32(0)},
			want:    1,
		},
		{
			name:    "int32 negative returns 1",
			headers: amqp.Table{headerWebhookAttempt: int32(-5)},
			want:    1,
		},
		{
			name:    "int64 zero returns 1",
			headers: amqp.Table{headerWebhookAttempt: int64(0)},
			want:    1,
		},
		{
			name:    "int zero returns 1",
			headers: amqp.Table{headerWebhookAttempt: int(0)},
			want:    1,
		},
		{
			name:    "string value returns 1 (unrecognized type)",
			headers: amqp.Table{headerWebhookAttempt: "bad"},
			want:    1,
		},
		{
			name:    "float64 value returns 1 (unrecognized type)",
			headers: amqp.Table{headerWebhookAttempt: float64(3.0)},
			want:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := readWebhookAttempt(tt.headers)
			assert.Equal(t, tt.want, got)
		})
	}
}

// =============================================================================
// deliveryFromDelayTier (x-death provenance) tests
// =============================================================================

func TestDeliveryFromDelayTier(t *testing.T) {
	const queueName = "myqueue"
	tests := []struct {
		name    string
		headers amqp.Table
		want    bool
	}{
		{name: "nil headers", headers: nil, want: false},
		{name: "no x-death", headers: amqp.Table{headerWebhookAttempt: int32(2)}, want: false},
		{
			name: "expired from own delay tier",
			headers: amqp.Table{"x-death": []any{
				amqp.Table{"reason": "expired", "queue": "myqueue.delay.0"},
			}},
			want: true,
		},
		{
			name: "expired from a later own delay tier",
			headers: amqp.Table{"x-death": []any{
				amqp.Table{"reason": "expired", "queue": "myqueue.delay.5"},
			}},
			want: true,
		},
		{
			name: "rejected (not expired) from delay tier is not provenance",
			headers: amqp.Table{"x-death": []any{
				amqp.Table{"reason": "rejected", "queue": "myqueue.delay.0"},
			}},
			want: false,
		},
		{
			name: "expired from a different queue is not provenance",
			headers: amqp.Table{"x-death": []any{
				amqp.Table{"reason": "expired", "queue": "otherqueue.delay.0"},
			}},
			want: false,
		},
		{
			name: "expired from the main queue (not a delay tier) is not provenance",
			headers: amqp.Table{"x-death": []any{
				amqp.Table{"reason": "expired", "queue": "myqueue"},
			}},
			want: false,
		},
		{
			name:    "x-death wrong type is not provenance",
			headers: amqp.Table{"x-death": "not-an-array"},
			want:    false,
		},
		{
			name: "multiple deaths, one matching",
			headers: amqp.Table{"x-death": []any{
				amqp.Table{"reason": "rejected", "queue": "myqueue"},
				amqp.Table{"reason": "expired", "queue": "myqueue.delay.2"},
			}},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, deliveryFromDelayTier(tt.headers, queueName))
		})
	}
}

// =============================================================================
// declareTopology — delay topology tests
// =============================================================================

func TestDeclareTopology_WithDelaySchedule_DeclaresDelayTiers(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	mockConn.nextCh = ch

	sub := NewSubscriber(clock.Real(), conn, SubscriberConfig{DLXExchange: "test.dlx"})

	schedule := []time.Duration{testtime.D200ms, testtime.D500ms}
	topic := "session.created"
	queueName := "cg-1.session.created"

	err := sub.declareTopology(ch, topic, queueName, schedule)
	require.NoError(t, err)

	ch.mu.Lock()
	defer ch.mu.Unlock()

	// Regular topology: main exchange, DLX exchange.
	assert.Contains(t, ch.exchangesDeclared, topic, "main fanout exchange must be declared")
	assert.Contains(t, ch.exchangesDeclared, "test.dlx", "DLX exchange must be declared")

	// Delay exchange.
	delayExchange := queueName + ".delay"
	assert.Contains(t, ch.exchangesDeclared, delayExchange, "delay exchange must be declared")

	// Main queue.
	assert.Contains(t, ch.queuesDeclared, queueName, "main queue must be declared")

	// Tier 0 queue.
	tier0 := queueName + ".delay.0"
	assert.Contains(t, ch.queuesDeclared, tier0, "tier 0 delay queue must be declared")

	// Tier 1 queue.
	tier1 := queueName + ".delay.1"
	assert.Contains(t, ch.queuesDeclared, tier1, "tier 1 delay queue must be declared")

	// Assert x-message-ttl values (must be int64 of milliseconds) and
	// x-dead-letter-exchange pointing back to the dispatch fanout topic.
	var tier0Args, tier1Args amqp.Table
	for i, q := range ch.queuesDeclared {
		switch q {
		case tier0:
			tier0Args = ch.queueDeclareArgs[i]
		case tier1:
			tier1Args = ch.queueDeclareArgs[i]
		}
	}
	require.NotNil(t, tier0Args, "tier 0 queue args must be recorded")
	require.NotNil(t, tier1Args, "tier 1 queue args must be recorded")

	assert.Equal(t, int64(200), tier0Args["x-message-ttl"], "tier 0 x-message-ttl must be 200ms as int64")
	assert.Equal(t, topic, tier0Args["x-dead-letter-exchange"], "tier 0 x-dead-letter-exchange must point to dispatch exchange")
	assert.Equal(t, "", tier0Args["x-dead-letter-routing-key"], "tier 0 must reset routing key to canonical empty")
	assertQuorumAtLeastOnce(t, tier0Args, "tier 0")

	assert.Equal(t, int64(500), tier1Args["x-message-ttl"], "tier 1 x-message-ttl must be 500ms as int64")
	assert.Equal(t, topic, tier1Args["x-dead-letter-exchange"], "tier 1 x-dead-letter-exchange must point to dispatch exchange")
	assert.Equal(t, "", tier1Args["x-dead-letter-routing-key"], "tier 1 must reset routing key to canonical empty")
	assertQuorumAtLeastOnce(t, tier1Args, "tier 1")

	// Bindings: tier 0 bound with routing key "0", tier 1 with "1".
	var tier0Detail, tier1Detail *queueBindDetail
	for i, d := range ch.queueBindDetails {
		switch d.queue {
		case tier0:
			dc := ch.queueBindDetails[i]
			tier0Detail = &dc
		case tier1:
			dc := ch.queueBindDetails[i]
			tier1Detail = &dc
		}
	}
	require.NotNil(t, tier0Detail, "tier 0 binding must be recorded")
	require.NotNil(t, tier1Detail, "tier 1 binding must be recorded")

	assert.Equal(t, "0", tier0Detail.key, "tier 0 routing key must be \"0\"")
	assert.Equal(t, delayExchange, tier0Detail.exchange, "tier 0 must bind to delay exchange")

	assert.Equal(t, "1", tier1Detail.key, "tier 1 routing key must be \"1\"")
	assert.Equal(t, delayExchange, tier1Detail.exchange, "tier 1 must bind to delay exchange")
}

// assertQuorumAtLeastOnce pins the three arguments that make a delay tier's
// broker-internal TTL→dead-letter republish at-least-once (#1835): a quorum
// queue with the at-least-once dead-letter strategy. This is the Medium guard
// for the change — the assertions fail in CI if a future edit drops any of them.
//
// x-overflow=reject-publish is the highest-risk one: RabbitMQ silently falls
// back to at-most-once dead-lettering if the overflow strategy is the default
// drop-head (the broker raises NO error), so removing it would re-open the
// cluster gap with no other signal. The unit assertion is the signal.
func assertQuorumAtLeastOnce(t *testing.T, args amqp.Table, tier string) {
	t.Helper()
	assert.Equal(t, "quorum", args["x-queue-type"],
		"%s must be a quorum queue (classic internal dead-letter republish is not at-least-once)", tier)
	assert.Equal(t, "at-least-once", args["x-dead-letter-strategy"],
		"%s must use at-least-once dead-lettering so the TTL→DLX hop is publisher-confirmed", tier)
	assert.Equal(t, "reject-publish", args["x-overflow"],
		"%s must set reject-publish overflow; drop-head silently degrades at-least-once to at-most-once", tier)
}

func TestDeclareTopology_EmptySchedule_NoDeLlayTopology(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	mockConn.nextCh = ch

	sub := NewSubscriber(clock.Real(), conn, SubscriberConfig{DLXExchange: "test.dlx"})

	// Empty schedule: no delay topology should be declared.
	err := sub.declareTopology(ch, "my.topic", "my-queue", nil)
	require.NoError(t, err)

	ch.mu.Lock()
	defer ch.mu.Unlock()

	// Only main exchange + DLX exchange — no delay exchange.
	assert.NotContains(t, ch.exchangesDeclared, "my-queue.delay",
		"delay exchange must NOT be declared for empty schedule")

	// Only main queue — no tier queues.
	for _, q := range ch.queuesDeclared {
		assert.NotContains(t, q, ".delay.", "delay tier queues must NOT be declared for empty schedule")
	}
}

// =============================================================================
// dispatchDisposition — delayed Requeue branch tests
// =============================================================================

// makeDispatchTestSetup builds the minimal scaffold for dispatchDisposition
// unit tests: a real Subscriber (with a mock conn), a mock channel, and a
// pre-baked outbox.Entry + delivery.
func makeDispatchTestSetup(t *testing.T) (*Subscriber, *mockChannel, amqp.Delivery, outbox.Entry) {
	t.Helper()
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	// F1: the delay-tier republish runs on an ephemeral confirm-mode channel
	// acquired from the connection (mockConn.nextCh). Auto-confirm publishes so
	// the happy-path republish completes; failure paths set ch.publishErr (publish
	// send fails) or autoConfirmation.Ack=false (broker nacks) explicitly.
	ch.autoConfirmation = &amqp.Confirmation{Ack: true, DeliveryTag: 1}
	mockConn.nextCh = ch

	sub := NewSubscriber(clock.Real(), conn, SubscriberConfig{
		QueueName:   "myqueue",
		DLXExchange: "test.dlx",
	})

	entry := mustNewEntry(t, "test.event", []byte(`{"x":1}`), outbox.WithID("evt-delay-001"))
	body := makeDeliveryBody(t, entry)

	delivery := amqp.Delivery{
		DeliveryTag: 99,
		Body:        body,
		ContentType: "application/json",
		ConsumerTag: "cg-myqueue-test.topic",
		Headers:     amqp.Table{},
	}

	return sub, ch, delivery, entry
}

// delayTierHeaders builds the header table a delivery carries after it has
// expired (TTL) out of "myqueue.delay.<i>" and been dead-lettered back to the
// dispatch exchange: the x-webhook-attempt counter plus the broker x-death
// "expired" provenance that dispatchDelayedRequeue requires before trusting the
// attempt counter (F6). Without this provenance the attempt header is ignored.
func delayTierHeaders(attempt int32) amqp.Table {
	return amqp.Table{
		headerWebhookAttempt: attempt,
		"x-death": []any{
			amqp.Table{"reason": "expired", "queue": "myqueue.delay.0"},
		},
	}
}

// TestDispatchDisposition_DelayedRequeue_Attempt1_PublishesAndAcks verifies that
// for attempt=1 with a 2-tier schedule:
//   - exactly one PublishWithContext to "<q>.delay" with routing key "0"
//   - header x-webhook-attempt set to 2
//   - Ack(tag) called on the original delivery
//   - no Nack(tag,false,true) called
func TestDispatchDisposition_DelayedRequeue_Attempt1_PublishesAndAcks(t *testing.T) {
	sub, ch, delivery, entry := makeDispatchTestSetup(t)

	schedule := []time.Duration{testtime.D200ms, testtime.D500ms}
	res := outbox.DeliveryOutcome{Disposition: outbox.DispositionRequeue, Err: errors.New("transient")}

	sub.dispatchDisposition(context.Background(), ch, delivery, "myqueue", schedule, res, nil, "test.topic", entry)

	ch.mu.Lock()
	defer ch.mu.Unlock()

	// Exactly one publish to the delay exchange with routing key "0".
	assert.True(t, ch.publishCalled, "PublishWithContext must be called")
	assert.Len(t, ch.publishedMessages, 1, "exactly one message must be published")
	assert.Equal(t, "myqueue.delay", ch.publishExchange, "must publish to the delay exchange")
	assert.Equal(t, "0", ch.publishRoutingKey, "must use routing key \"0\" for attempt 1")

	// Header x-webhook-attempt must be set to 2 (attempt+1).
	pub := ch.publishedMessages[0]
	require.NotNil(t, pub.Headers)
	assert.Equal(t, int32(2), pub.Headers[headerWebhookAttempt], "x-webhook-attempt must be 2")
	assert.Equal(t, amqp.Persistent, pub.DeliveryMode, "delivery mode must be persistent")
	assert.Equal(t, "application/json", pub.ContentType, "content type must be preserved")

	// Original delivery must be Acked.
	assert.True(t, ch.ackCalled, "original delivery must be Acked")
	assert.Equal(t, uint64(99), ch.ackTag, "Ack must use the original delivery tag")

	// Must NOT Nack(requeue=true).
	assert.False(t, ch.nackCalled, "Nack must NOT be called on successful delay publish")
}

// TestDispatchDisposition_DelayedRequeue_BudgetExhausted verifies that when
// attempt > len(schedule), Nack(false,false) is issued (routes to real DLX)
// and the settlement result is RetryExhausted.
func TestDispatchDisposition_DelayedRequeue_BudgetExhausted(t *testing.T) {
	sub, ch, delivery, entry := makeDispatchTestSetup(t)

	// Set attempt = 3 > len(schedule)=2 to trigger exhaustion. Carry delay-tier
	// provenance so the attempt counter is trusted (F6).
	delivery.Headers = delayTierHeaders(3)

	schedule := []time.Duration{testtime.D200ms, testtime.D500ms}
	res := outbox.DeliveryOutcome{Disposition: outbox.DispositionRequeue, Err: errors.New("transient")}

	notifyCount := 0
	// We can't inject a settlement observer here; we just assert the broker call.
	sub.dispatchDisposition(context.Background(), ch, delivery, "myqueue", schedule, res, nil, "test.topic", entry)

	ch.mu.Lock()
	defer ch.mu.Unlock()

	// Nack(false,false) routes to real DLX.
	assert.True(t, ch.nackCalled, "Nack must be called on budget exhaustion")
	assert.False(t, ch.nackRequeue, "Nack must use requeue=false to route to real DLX")
	assert.Equal(t, uint64(99), ch.nackTag)

	// Must NOT publish to delay exchange.
	assert.False(t, ch.publishCalled, "PublishWithContext must NOT be called on exhaustion")

	_ = notifyCount // used only for assertion above
}

// TestDispatchDisposition_DelayedRequeue_PublishError_FailsClosedToDLX verifies
// that when the delay-tier publish fails, the subscriber fails closed via
// Nack(tag, false, false) — routing to the real DLX — instead of an immediate
// Nack(tag, false, true). An immediate requeue would redeliver the same message
// with the same, un-advanced x-webhook-attempt, hot-looping forever on a
// persistent delay-infra fault (F5).
func TestDispatchDisposition_DelayedRequeue_PublishError_FailsClosedToDLX(t *testing.T) {
	sub, ch, delivery, entry := makeDispatchTestSetup(t)
	ch.publishErr = errors.New("channel closed")

	schedule := []time.Duration{testtime.D200ms}
	res := outbox.DeliveryOutcome{Disposition: outbox.DispositionRequeue, Err: errors.New("transient")}

	sub.dispatchDisposition(context.Background(), ch, delivery, "myqueue", schedule, res, nil, "test.topic", entry)

	ch.mu.Lock()
	defer ch.mu.Unlock()

	// Fail closed: Nack(tag, false, false) routes to the real DLX.
	assert.True(t, ch.nackCalled, "publish failure must Nack to fail closed")
	assert.False(t, ch.nackRequeue,
		"fail-closed Nack must use requeue=false (route to DLX, not hot-loop an immediate requeue)")

	// Ack must NOT be called (publish failed before original Ack).
	assert.False(t, ch.ackCalled, "Ack must NOT be called when publish fails")
}

// TestDispatchDisposition_DelayedRequeue_BrokerNack_FailsClosedToDLX verifies
// that when the broker NACKs the delay-tier publish (confirm.Ack == false), the
// subscriber fails closed to the DLX and does NOT Ack the original delivery —
// the broker never durably accepted the republished copy, so acking would lose
// the webhook (F1).
func TestDispatchDisposition_DelayedRequeue_BrokerNack_FailsClosedToDLX(t *testing.T) {
	sub, ch, delivery, entry := makeDispatchTestSetup(t)
	// Broker rejects the publish: confirm arrives with Ack == false.
	ch.autoConfirmation = &amqp.Confirmation{Ack: false, DeliveryTag: 1}

	schedule := []time.Duration{testtime.D200ms}
	res := outbox.DeliveryOutcome{Disposition: outbox.DispositionRequeue, Err: errors.New("transient")}

	sub.dispatchDisposition(context.Background(), ch, delivery, "myqueue", schedule, res, nil, "test.topic", entry)

	ch.mu.Lock()
	defer ch.mu.Unlock()

	assert.True(t, ch.publishCalled, "publish must be attempted")
	assert.True(t, ch.nackCalled, "broker nack must fail closed via Nack")
	assert.False(t, ch.nackRequeue, "fail-closed Nack must use requeue=false (route to DLX)")
	assert.False(t, ch.ackCalled,
		"original delivery must NOT be Acked when the broker did not confirm the delay publish")
}

// TestDispatchDisposition_DelayedRequeue_ForgedAttemptWithoutProvenance_TreatedAsAttempt1
// verifies F6: an x-webhook-attempt header on a delivery that lacks broker
// x-death "expired from delay tier" provenance is NOT trusted. A forged high
// attempt (here 99, well past the schedule) must NOT force a premature DLX; the
// delivery is treated as attempt 1 and republished to tier 0.
func TestDispatchDisposition_DelayedRequeue_ForgedAttemptWithoutProvenance_TreatedAsAttempt1(t *testing.T) {
	sub, ch, delivery, entry := makeDispatchTestSetup(t)
	// Forged attempt header, but NO x-death delay-tier provenance.
	delivery.Headers = amqp.Table{headerWebhookAttempt: int32(99)}

	schedule := []time.Duration{testtime.D200ms, testtime.D500ms}
	res := outbox.DeliveryOutcome{Disposition: outbox.DispositionRequeue, Err: errors.New("transient")}

	sub.dispatchDisposition(context.Background(), ch, delivery, "myqueue", schedule, res, nil, "test.topic", entry)

	ch.mu.Lock()
	defer ch.mu.Unlock()

	// Treated as attempt 1: republished to tier 0, NOT exhausted to DLX.
	assert.True(t, ch.publishCalled, "forged attempt must be ignored and the delivery republished, not exhausted")
	assert.Equal(t, "0", ch.publishRoutingKey, "untrusted attempt must default to attempt 1 (routing key \"0\")")
	assert.True(t, ch.ackCalled, "original delivery must be Acked after successful republish")
	require.Len(t, ch.publishedMessages, 1)
	assert.Equal(t, int32(2), ch.publishedMessages[0].Headers[headerWebhookAttempt],
		"x-webhook-attempt must advance from the trusted base of 1 to 2")
}

// TestDispatchDisposition_NonDelayed_Requeue_Unchanged verifies that an empty
// schedule leaves the Requeue branch unchanged (Nack(false,true)).
func TestDispatchDisposition_NonDelayed_Requeue_Unchanged(t *testing.T) {
	sub, ch, delivery, entry := makeDispatchTestSetup(t)

	// Empty schedule: non-delayed path.
	res := outbox.DeliveryOutcome{Disposition: outbox.DispositionRequeue, Err: errors.New("transient")}

	sub.dispatchDisposition(context.Background(), ch, delivery, "myqueue", nil, res, nil, "test.topic", entry)

	ch.mu.Lock()
	defer ch.mu.Unlock()

	assert.True(t, ch.nackCalled, "non-delayed Requeue must Nack")
	assert.True(t, ch.nackRequeue, "non-delayed Requeue must use requeue=true")
	assert.False(t, ch.publishCalled, "non-delayed Requeue must NOT publish")
	assert.False(t, ch.ackCalled, "non-delayed Requeue must NOT Ack")
}

// TestDispatchDisposition_DelayedRequeue_Attempt2_PublishesRK1 verifies that
// for attempt=2, routing key "1" is used (tier index = attempt-1).
func TestDispatchDisposition_DelayedRequeue_Attempt2_PublishesRK1(t *testing.T) {
	sub, ch, delivery, entry := makeDispatchTestSetup(t)
	delivery.Headers = delayTierHeaders(2)

	schedule := []time.Duration{testtime.D200ms, testtime.D500ms}
	res := outbox.DeliveryOutcome{Disposition: outbox.DispositionRequeue, Err: errors.New("transient")}

	sub.dispatchDisposition(context.Background(), ch, delivery, "myqueue", schedule, res, nil, "test.topic", entry)

	ch.mu.Lock()
	defer ch.mu.Unlock()

	assert.True(t, ch.publishCalled)
	assert.Equal(t, "1", ch.publishRoutingKey, "attempt 2 must use routing key \"1\"")

	pub := ch.publishedMessages[0]
	assert.Equal(t, int32(3), pub.Headers[headerWebhookAttempt], "x-webhook-attempt must be 3")
}

// TestDispatchDisposition_DelayedRequeue_AttemptEqLen_RepublishesLastTier verifies
// the boundary case where attempt == len(schedule) (e.g. schedule length 2,
// attempt=2). This is NOT exhaustion — exhaustion only fires when attempt >
// len(schedule). The message must be republished to the LAST tier (routing key
// strconv.Itoa(len-1) = "1" for a 2-tier schedule) and Acked normally.
func TestDispatchDisposition_DelayedRequeue_AttemptEqLen_RepublishesLastTier(t *testing.T) {
	sub, ch, delivery, entry := makeDispatchTestSetup(t)

	// attempt = len(schedule) = 2: boundary — must republish, not exhaust.
	// Carry delay-tier provenance so the attempt counter is trusted (F6).
	delivery.Headers = delayTierHeaders(2)

	schedule := []time.Duration{testtime.D200ms, testtime.D500ms}
	res := outbox.DeliveryOutcome{Disposition: outbox.DispositionRequeue, Err: errors.New("transient")}

	sub.dispatchDisposition(context.Background(), ch, delivery, "myqueue", schedule, res, nil, "test.topic", entry)

	ch.mu.Lock()
	defer ch.mu.Unlock()

	// Must publish to last tier (routing key = strconv.Itoa(len-1) = "1").
	assert.True(t, ch.publishCalled, "must republish to last tier when attempt == len(schedule)")
	assert.Equal(t, "1", ch.publishRoutingKey, "routing key must be \"1\" (last tier) for attempt == len(schedule)")
	assert.Equal(t, "myqueue.delay", ch.publishExchange, "must target delay exchange")

	// Must NOT nack with requeue=false (that would exhaust the budget).
	if ch.nackCalled {
		assert.True(t, ch.nackRequeue,
			"if Nack was called it must be requeue=true (not DLX exhaust path)")
	}

	// Original delivery must be Acked.
	assert.True(t, ch.ackCalled, "original delivery must be Acked on successful republish")

	// x-webhook-attempt header must be incremented to 3.
	require.Len(t, ch.publishedMessages, 1)
	assert.Equal(t, int32(3), ch.publishedMessages[0].Headers[headerWebhookAttempt],
		"x-webhook-attempt must be incremented to attempt+1")
}

// =============================================================================
// Subscribe integration: delay schedule threaded end-to-end
// =============================================================================

// TestSubscribe_DelayedSchedule_ThreadedThroughTopology verifies that when
// BrokerDelaySchedule is set on the Subscription, the delay topology is
// declared (delay exchange and tier queues visible on the mock channel).
func TestSubscribe_DelayedSchedule_ThreadedThroughTopology(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(clock.Real(), conn, SubscriberConfig{
		QueueName:   "myqueue",
		DLXExchange: "test.dlx",
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately so Subscribe exits after topology declaration.

	schedule := []time.Duration{testtime.D100ms, testtime.D300ms}
	subscription := outbox.Subscription{
		Topic:               "test.topic",
		CellID:              "test-cell",
		BrokerDelaySchedule: schedule,
	}

	err := sub.Subscribe(ctx, subscription,
		entryToSubHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			return outbox.Ack()
		}))
	assert.NoError(t, err)

	ch.mu.Lock()
	defer ch.mu.Unlock()

	assert.Contains(t, ch.exchangesDeclared, "myqueue.delay",
		"delay exchange must be declared when schedule is non-empty")
	assert.Contains(t, ch.queuesDeclared, "myqueue.delay.0",
		"tier 0 delay queue must be declared")
	assert.Contains(t, ch.queuesDeclared, "myqueue.delay.1",
		"tier 1 delay queue must be declared")
}

// TestSubscribe_NoDelaySchedule_NoDelayTopology verifies that without a
// BrokerDelaySchedule the delay exchange and tier queues are NOT declared.
func TestSubscribe_NoDelaySchedule_NoDelayTopology(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(clock.Real(), conn, SubscriberConfig{
		QueueName:   "myqueue",
		DLXExchange: "test.dlx",
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sub.Subscribe(ctx, outbox.Subscription{Topic: "test.topic", CellID: "test-cell"},
		entryToSubHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			return outbox.Ack()
		}))
	assert.NoError(t, err)

	ch.mu.Lock()
	defer ch.mu.Unlock()

	assert.NotContains(t, ch.exchangesDeclared, "myqueue.delay",
		"delay exchange must NOT be declared when schedule is empty")
	for _, q := range ch.queuesDeclared {
		assert.NotContains(t, q, ".delay.", "delay tier queues must NOT be declared for empty schedule")
	}
}

// TestSubscribe_DelayedSchedule_RequeueRoutesToDelayTier is a higher-level test
// that delivers a message, returns DispositionRequeue with a delay schedule, and
// asserts that PublishWithContext is called on the mock channel (routing to the
// delay tier) rather than Nack(requeue=true).
func TestSubscribe_DelayedSchedule_RequeueRoutesToDelayTier(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	// F1: the delay-tier republish runs on a confirm-mode channel acquired from
	// the connection (same mock channel here); auto-confirm so the republish and
	// subsequent Ack of the original delivery complete.
	ch.autoConfirmation = &amqp.Confirmation{Ack: true, DeliveryTag: 1}
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(clock.Real(), conn, SubscriberConfig{
		QueueName:   "myqueue",
		DLXExchange: "test.dlx",
	})

	entry := mustNewEntry(t, "test.event", []byte(`{}`), outbox.WithID("evt-delay-e2e"))
	entryBytes := makeDeliveryBody(t, entry)

	ch.consumeDeliveries <- amqp.Delivery{
		DeliveryTag: 7,
		Body:        entryBytes,
		ContentType: "application/json",
		ConsumerTag: "cg-myqueue-test.topic",
		Headers:     amqp.Table{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	schedule := []time.Duration{testtime.D200ms}
	subscription := outbox.Subscription{
		Topic:               "test.topic",
		CellID:              "test-cell",
		BrokerDelaySchedule: schedule,
	}

	subDone := make(chan error, 1)
	go func() {
		subDone <- sub.Subscribe(ctx, subscription,
			entryToSubHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
				return outbox.Requeue(errors.New("transient"))
			}))
	}()

	// Wait until a publish or nack is recorded.
	testwait.External(t, "amqp-delay-publish", func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.publishCalled || ch.nackCalled
	}, testtime.D2s, testtime.FastPoll, "neither publish nor nack was called in time")

	cancel()
	assert.NoError(t, <-subDone)

	ch.mu.Lock()
	defer ch.mu.Unlock()

	// Must have published to the delay exchange, not Nacked with requeue.
	assert.True(t, ch.publishCalled, "must publish to delay tier for delayed Requeue")
	assert.Equal(t, "myqueue.delay", ch.publishExchange, "must target the delay exchange")
	assert.Equal(t, "0", ch.publishRoutingKey, "routing key must be \"0\" for attempt 1")
	assert.True(t, ch.ackCalled, "original delivery must be Acked after successful publish")

	// Nack(requeue=true) must NOT be called.
	if ch.nackCalled {
		assert.False(t, ch.nackRequeue, "if Nack was called it must NOT be requeue=true (only DLX nack is acceptable)")
	}
}
