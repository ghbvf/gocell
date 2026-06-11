//go:build !integration

package mqtt

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// newSubEnvelope builds a valid v1 outbox wire envelope for the given topic and
// payload so the subscriber's UnmarshalEnvelope succeeds.
func newSubEnvelope(t *testing.T, topic string, payload []byte) []byte {
	t.Helper()
	entry := mustNewEntry(t, "test.event", payload, outbox.WithID(uuid.NewString()), outbox.WithTopic(topic))
	raw, err := outbox.MarshalEnvelope(entry)
	require.NoError(t, err)
	return raw
}

// fakeFailingClaimer always returns an infra error from Claim, to drive the
// fail-closed Requeue-downgrade path in ConsumerBase.
type fakeFailingClaimer struct{ err error }

func (f *fakeFailingClaimer) Claim(
	context.Context, string, time.Duration, time.Duration,
) (idempotency.ClaimState, idempotency.Receipt, error) {
	return 0, nil, f.err
}

func (f *fakeFailingClaimer) Kind() idempotency.ClaimerKind { return idempotency.ClaimerKindInMemory }

// newTestSubscriber opens a connection to the shared broker and constructs a
// Subscriber with the given collector.
func newTestSubscriber(t *testing.T, addr string, collector SubscriberCollector) (*Subscriber, *Connection) {
	t.Helper()
	clk := clock.Real()
	cfg := newInternalConfig(t, addr)
	// autopaho binds the ConnectionManager lifecycle to the ctx passed to Open:
	// canceling it tears the connection down. This ctx must therefore outlive the
	// helper — scope its cancel to t.Cleanup (fires at test end, before/with
	// conn.Close), NOT a defer that fires when newTestSubscriber returns. A
	// returning-helper defer would cancel the ctx immediately, leaving every
	// later cm.Subscribe with ConnectionDownError. cfg.connectTimeout already
	// bounds the bootstrap connect inside Open.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)
	opts := []SubscriberOption{}
	if collector != nil {
		opts = append(opts, WithSubscriberCollector(collector))
	}
	sub, err := NewSubscriber(clk, conn, ns, SubscriberConfig{}, opts...)
	require.NoError(t, err)
	return sub, conn
}

// startSubscribe runs sub.Subscribe in a goroutine and waits for Ready. It
// returns a cancel func to stop the subscription.
func startSubscribe(t *testing.T, sub *Subscriber, subscription outbox.Subscription, handler outbox.SubscriberHandler) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = sub.Subscribe(ctx, subscription, handler) }()
	select {
	case <-sub.Ready(subscription):
	case <-time.After(testtime.D5s):
		cancel()
		t.Fatal("subscribe did not become ready")
	}
	return cancel
}

// publishTo publishes payload to topic via the connection's namespace.
func publishTo(t *testing.T, conn *Connection, topic string, payload []byte) {
	t.Helper()
	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)
	pt, err := ns.Mint(topic)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D5s)
	defer cancel()
	_, err = conn.Publish(ctx, pt, payload, publishOpts{QoS: 1})
	require.NoError(t, err)
}

func newSubscription(topic, group string) outbox.Subscription {
	return outbox.Subscription{
		Topic:             topic,
		ConsumerGroup:     group,
		CellID:            "testcell",
		ContractID:        "event.test.v1",
		ContractKind:      "event",
		ContractTransport: "mqtt",
	}
}

// ---------------------------------------------------------------------------
// Compile-time + construction
// ---------------------------------------------------------------------------

func TestSubscriber_ImplementsInterfaces(t *testing.T) {
	t.Parallel()
	var _ outbox.Subscriber = (*Subscriber)(nil)
	var _ outbox.SubscriberIntakeStopper = (*Subscriber)(nil)
}

func TestNewSubscriber_NilConnection(t *testing.T) {
	t.Parallel()
	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)
	_, err = NewSubscriber(clock.Real(), nil, ns, SubscriberConfig{})
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

func TestNewSubscriber_ZeroNamespace(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()
	clk := clock.Real()
	cfg := newInternalConfig(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()
	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	_, err = NewSubscriber(clk, conn, TopicNamespace{}, SubscriberConfig{})
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidTopicNamespace, ec.Code)
}

func TestSubscriberConfig_SetDefaults(t *testing.T) {
	t.Parallel()
	var sc SubscriberConfig
	sc.setDefaults()
	assert.Equal(t, defaultSubscriberQoS, sc.QoS)
	assert.Equal(t, defaultSettlementTimeout, sc.SettlementTimeout)
	assert.Equal(t, defaultStopIntakePerCallTimeout, sc.StopIntakePerCallTimeout)
	assert.Equal(t, defaultStopIntakeDrainTimeout, sc.StopIntakeDrainTimeout)
}

func TestSubscriber_Setup_InvalidFilter(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()
	sub, _ := newTestSubscriber(t, addr, nil)

	// Filter outside the "test" namespace is rejected.
	err := sub.Setup(context.Background(), newSubscription("other/x", "cg"))
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTTopicOutsideNamespace, ec.Code)

	// Valid filter passes.
	require.NoError(t, sub.Setup(context.Background(), newSubscription("test/x", "cg")))
}

// ---------------------------------------------------------------------------
// Ready
// ---------------------------------------------------------------------------

func TestSubscriber_Ready_ClosesAfterSubscribe(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()
	sub, _ := newTestSubscriber(t, addr, nil)
	subscription := newSubscription("test/ready/+", "ready-cg-"+uuid.NewString())

	readyCh := sub.Ready(subscription)
	select {
	case <-readyCh:
		t.Fatal("ready channel closed before Subscribe")
	default:
	}

	cancel := startSubscribe(t, sub, subscription, func(_ context.Context, _ outbox.Entry) (outbox.DeliveryOutcome, outbox.Settlement) {
		return outbox.DeliveryOutcome{Disposition: outbox.DispositionAck}, nil
	})
	defer cancel()

	select {
	case <-readyCh:
		// expected: same channel returned earlier is now closed
	case <-time.After(testtime.D5s):
		t.Fatal("ready channel not closed after Subscribe confirmed SUBACK")
	}
}

// ---------------------------------------------------------------------------
// Disposition matrix
// ---------------------------------------------------------------------------

func TestSubscriber_DispositionMatrix(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		result      func() outbox.DeliveryOutcome
		wantSuccess int
		wantReason  ConsumeFailureReason
		wantCommit  int
		wantRelease int
		wantDLX     int // expected $dead routing count (Option C: only Reject routes)
	}{
		{
			name:        "ack",
			result:      func() outbox.DeliveryOutcome { return outbox.DeliveryOutcome{Disposition: outbox.DispositionAck} },
			wantSuccess: 1,
			wantCommit:  1,
			wantRelease: 0,
		},
		{
			name: "requeue",
			result: func() outbox.DeliveryOutcome {
				return outbox.DeliveryOutcome{Disposition: outbox.DispositionRequeue, Err: errors.New("transient")}
			},
			wantReason:  consumeReasonRequeue,
			wantRelease: 1,
			wantDLX:     0, // Option C: Requeue leaves unacked for reconnect, no $dead
		},
		{
			name: "reject",
			result: func() outbox.DeliveryOutcome {
				return outbox.DeliveryOutcome{Disposition: outbox.DispositionReject, Err: errors.New("permanent")}
			},
			wantReason:  consumeReasonReject,
			wantRelease: 1,
			wantDLX:     1, // Reject routes the envelope to $dead before ack-as-poison
		},
		{
			name:        "zero-value-disposition",
			result:      func() outbox.DeliveryOutcome { return outbox.DeliveryOutcome{} },
			wantReason:  consumeReasonUnknownDisposition,
			wantRelease: 1,
			wantDLX:     0, // unknown disposition degrades to Requeue (leave unacked), no $dead
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr, stop := startInternalBroker(t)
			defer stop()
			coll := newRecordingSubCollector()
			sub, conn := newTestSubscriber(t, addr, coll)

			settlement := &recordingSettlement{}
			var called atomic.Bool
			// Subtests run in parallel against the SHARED broker. A common
			// "test/disp/+" filter lets MQTT shared-subscription fanout deliver
			// one subtest's PUBLISH to every other subtest's subscription (each
			// distinct consumer group receives a full copy), corrupting the
			// per-subtest commit/release/success counts. A per-name topic prefix
			// makes the filters disjoint so no cross-subtest delivery occurs.
			prefix := "test/disp/" + tc.name
			subscription := newSubscription(prefix+"/+", "disp-"+tc.name+"-"+uuid.NewString())
			cancel := startSubscribe(t, sub, subscription, func(_ context.Context, _ outbox.Entry) (outbox.DeliveryOutcome, outbox.Settlement) {
				called.Store(true)
				return tc.result(), settlement
			})
			defer cancel()

			topic := prefix + "/" + uuid.NewString()
			publishTo(t, conn, topic, newSubEnvelope(t, topic, []byte(`{"k":"v"}`)))

			testwait.External(t, "handler-called", called.Load, testtime.D5s, testtime.D10ms)

			// Wait for the TERMINAL settlement: A4 (#1142) releases the claim only
			// AFTER routeDeadLetter + ackPoison, so "failure recorded" no longer
			// implies "released". Poll until success (ack) or release
			// (requeue/reject) reaches the expected count, so the release assertion
			// below is not racing the (now-last) releaseSettlement step.
			testwait.External(t, "dispatch-settled", func() bool {
				success, failure, _ := coll.snapshot()
				_, release := settlement.counts()
				return success >= tc.wantSuccess && release >= tc.wantRelease && (tc.wantReason == "" || failure >= 1)
			}, testtime.D5s, testtime.D10ms)

			// The $dead routing (RecordDeadLetter) runs AFTER RecordConsumeFailure
			// and includes a broker round-trip, so wait for it explicitly when
			// expected to avoid racing the assertion.
			if tc.wantDLX > 0 {
				testwait.External(t, "dead-letter-routed", func() bool {
					total, _ := coll.dlxSnapshot()
					return total >= tc.wantDLX
				}, testtime.D5s, testtime.D10ms)
			}

			success, failure, lastReason := coll.snapshot()
			commit, release := settlement.counts()
			dlxTotal, dlxByReason := coll.dlxSnapshot()
			assert.Equal(t, tc.wantSuccess, success, "consume success count")
			assert.Equal(t, tc.wantCommit, commit, "settlement commit count")
			assert.Equal(t, tc.wantRelease, release, "settlement release count")
			assert.Equal(t, tc.wantDLX, dlxTotal, "$dead routing count")
			if tc.wantDLX > 0 {
				assert.Equal(t, tc.wantDLX, dlxByReason[tc.wantReason],
					"$dead recorded under the disposition reason %s", tc.wantReason)
			}
			if tc.wantReason != "" {
				assert.GreaterOrEqual(t, failure, 1)
				assert.Equal(t, tc.wantReason, lastReason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Unmarshal-fail poison
// ---------------------------------------------------------------------------

func TestSubscriber_UnmarshalFail_PoisonAcked(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()
	coll := newRecordingSubCollector()
	sub, conn := newTestSubscriber(t, addr, coll)

	var handlerCalled atomic.Bool
	subscription := newSubscription("test/poison/+", "poison-cg-"+uuid.NewString())
	cancel := startSubscribe(t, sub, subscription, func(_ context.Context, _ outbox.Entry) (outbox.DeliveryOutcome, outbox.Settlement) {
		handlerCalled.Store(true)
		return outbox.DeliveryOutcome{Disposition: outbox.DispositionAck}, nil
	})
	defer cancel()

	// Publish a non-envelope payload: handler must NOT be called.
	topic := "test/poison/" + uuid.NewString()
	publishTo(t, conn, topic, []byte("not-a-valid-envelope"))

	testwait.External(t, "unmarshal-failure-recorded", func() bool {
		_, failure, _ := coll.snapshot()
		return failure >= 1
	}, testtime.D5s, testtime.D10ms)

	// The undecodable payload is routed to $dead/<pb.Topic> (raw bytes) before
	// the poison ack — wait for the dead-letter record.
	testwait.External(t, "poison-dead-letter-routed", func() bool {
		total, _ := coll.dlxSnapshot()
		return total >= 1
	}, testtime.D5s, testtime.D10ms)

	_, failure, lastReason := coll.snapshot()
	dlxTotal, dlxByReason := coll.dlxSnapshot()
	assert.Equal(t, 1, failure)
	assert.Equal(t, consumeReasonUnmarshal, lastReason)
	assert.Equal(t, 1, dlxTotal, "poison message routed to $dead")
	assert.Equal(t, 1, dlxByReason[consumeReasonUnmarshal], "$dead recorded under the unmarshal reason")
	assert.False(t, handlerCalled.Load(), "handler must not be called for poison message")
}

// ---------------------------------------------------------------------------
// Claim-failure → Requeue downgrade (fail-closed idempotency)
// ---------------------------------------------------------------------------

func TestSubscriber_ClaimFailure_RequeueDowngrade(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()
	coll := newRecordingSubCollector()
	sub, conn := newTestSubscriber(t, addr, coll)

	// Build a REAL ConsumerBase with a failing Claimer; fail-closed default policy
	// retries Claim then returns Requeue. Use a fast retry budget so the test
	// doesn't wait on default 1s backoff.
	cb, err := outbox.NewConsumerBase(
		&fakeFailingClaimer{err: errors.New("redis down")},
		outbox.ConsumerBaseConfig{
			ClaimRetryCount:     1, // single Claim attempt, no backoff sleep
			ClaimRetryBaseDelay: time.Millisecond,
		},
		clock.Real(),
	)
	require.NoError(t, err)

	swm, err := outbox.NewSubscriberWithMiddleware(sub, cb)
	require.NoError(t, err)

	var businessCalled atomic.Bool
	subscription := newSubscription("test/claim/+", "claim-cg-"+uuid.NewString())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = swm.SubscribeEntry(ctx, subscription, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			businessCalled.Store(true)
			return outbox.Ack()
		})
	}()
	select {
	case <-swm.Ready(subscription):
	case <-time.After(testtime.D5s):
		t.Fatal("subscribe not ready")
	}

	topic := "test/claim/" + uuid.NewString()
	publishTo(t, conn, topic, newSubEnvelope(t, topic, []byte(`{"k":"v"}`)))

	// Claim fails → ConsumerBase returns Requeue → subscriber leaves unacked +
	// records requeue. Business handler must NOT run (no claim acquired). Nil
	// Settlement on the requeue path means no Release call (nothing to release).
	testwait.External(t, "requeue-recorded", func() bool {
		_, failure, _ := coll.snapshot()
		return failure >= 1
	}, testtime.D5s, testtime.D10ms)

	success, _, lastReason := coll.snapshot()
	assert.Equal(t, 0, success, "must NOT ack on claim failure (fail-closed)")
	assert.Equal(t, consumeReasonRequeue, lastReason)
	assert.False(t, businessCalled.Load(), "business handler must not run when Claim fails")
}

// ---------------------------------------------------------------------------
// Competing consumers ($share)
// ---------------------------------------------------------------------------

func TestSubscriber_CompetingConsumers_ShareDistributes(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	group := "compete-cg-" + uuid.NewString()
	filter := "test/compete/+"
	subscription := newSubscription(filter, group)

	var received1, received2 atomic.Int64
	seen := &sync.Map{} // dedup detection: payload -> struct{}
	var dup atomic.Int64

	mkHandler := func(counter *atomic.Int64) outbox.SubscriberHandler {
		return func(_ context.Context, entry outbox.Entry) (outbox.DeliveryOutcome, outbox.Settlement) {
			counter.Add(1)
			if _, loaded := seen.LoadOrStore(string(entry.Payload()), struct{}{}); loaded {
				dup.Add(1)
			}
			return outbox.DeliveryOutcome{Disposition: outbox.DispositionAck}, nil
		}
	}

	sub1, conn := newTestSubscriber(t, addr, nil)
	sub2, _ := newTestSubscriber(t, addr, nil)

	cancel1 := startSubscribe(t, sub1, subscription, mkHandler(&received1))
	defer cancel1()
	cancel2 := startSubscribe(t, sub2, subscription, mkHandler(&received2))
	defer cancel2()

	const n = 10
	for i := 0; i < n; i++ {
		topic := "test/compete/" + uuid.NewString()
		payload := []byte(fmt.Sprintf(`{"seq":%d}`, i))
		publishTo(t, conn, topic, newSubEnvelope(t, topic, payload))
	}

	testwait.External(t, "all-delivered", func() bool {
		return received1.Load()+received2.Load() >= n
	}, testtime.D10s, testtime.D10ms)

	total := received1.Load() + received2.Load()
	assert.Equal(t, int64(n), total, "each message delivered exactly once across the group")
	assert.Equal(t, int64(0), dup.Load(), "no message delivered to both consumers")
}

// ---------------------------------------------------------------------------
// StopIntake drains in-flight handlers
// ---------------------------------------------------------------------------

func TestSubscriber_StopIntake_DrainsInFlight(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()
	sub, conn := newTestSubscriber(t, addr, nil)

	entered := make(chan struct{})
	release := make(chan struct{})
	var completed atomic.Bool
	var once sync.Once

	subscription := newSubscription("test/drain/+", "drain-cg-"+uuid.NewString())
	cancel := startSubscribe(t, sub, subscription, func(_ context.Context, _ outbox.Entry) (outbox.DeliveryOutcome, outbox.Settlement) {
		once.Do(func() { close(entered) })
		<-release
		completed.Store(true)
		return outbox.DeliveryOutcome{Disposition: outbox.DispositionAck}, nil
	})
	defer cancel()

	topic := "test/drain/" + uuid.NewString()
	publishTo(t, conn, topic, newSubEnvelope(t, topic, []byte(`{"k":"v"}`)))

	select {
	case <-entered:
	case <-time.After(testtime.D5s):
		t.Fatal("handler never entered")
	}

	// StopIntake must wait for the in-flight handler. Run it in a goroutine and
	// confirm it does not return until we release the handler.
	stopDone := make(chan error, 1)
	go func() {
		stopCtx, c := context.WithTimeout(context.Background(), testtime.D10s)
		defer c()
		stopDone <- sub.StopIntake(stopCtx)
	}()

	select {
	case <-stopDone:
		t.Fatal("StopIntake returned before in-flight handler completed")
	case <-time.After(testtime.D100ms):
		// expected: StopIntake is still draining
	}

	close(release)
	select {
	case err := <-stopDone:
		require.NoError(t, err, "StopIntake should return nil once in-flight handler drains")
	case <-time.After(testtime.D5s):
		t.Fatal("StopIntake did not return after handler released")
	}
	assert.True(t, completed.Load(), "in-flight handler must complete during drain")
}

// ---------------------------------------------------------------------------
// Close idempotency + Subscribe-after-Close
// ---------------------------------------------------------------------------

func TestSubscriber_Close_Idempotent(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()
	sub, _ := newTestSubscriber(t, addr, nil)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D5s)
	defer cancel()
	assert.NoError(t, sub.Close(ctx), "first Close")
	assert.NoError(t, sub.Close(ctx), "second Close idempotent")
}

func TestSubscriber_Subscribe_AfterClose(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()
	sub, _ := newTestSubscriber(t, addr, nil)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D5s)
	defer cancel()
	require.NoError(t, sub.Close(ctx))

	err := sub.Subscribe(context.Background(), newSubscription("test/x/+", "cg"), ackHandler)
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTClosed, ec.Code)
}

func TestSubscriber_Close_StopsBlockedSubscribe(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()
	sub, _ := newTestSubscriber(t, addr, nil)

	subscription := newSubscription("test/block/+", "block-cg-"+uuid.NewString())
	subDone := make(chan error, 1)
	ackHandler := func(_ context.Context, _ outbox.Entry) (outbox.DeliveryOutcome, outbox.Settlement) {
		return outbox.DeliveryOutcome{Disposition: outbox.DispositionAck}, nil
	}
	go func() {
		subDone <- sub.Subscribe(context.Background(), subscription, ackHandler)
	}()
	select {
	case <-sub.Ready(subscription):
	case <-time.After(testtime.D5s):
		t.Fatal("subscribe not ready")
	}

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D5s)
	defer cancel()
	require.NoError(t, sub.Close(ctx))

	select {
	case err := <-subDone:
		require.NoError(t, err, "blocked Subscribe should return nil on Close")
	case <-time.After(testtime.D5s):
		t.Fatal("Subscribe did not unblock after Close")
	}
}

// ---------------------------------------------------------------------------
// Metrics emission (recording collector observes success + failure)
// ---------------------------------------------------------------------------

func TestSubscriber_MetricsEmission(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()
	coll := newRecordingSubCollector()
	sub, conn := newTestSubscriber(t, addr, coll)

	// First message acked (success), second is poison (failure).
	subscription := newSubscription("test/metrics/+", "metrics-cg-"+uuid.NewString())
	cancel := startSubscribe(t, sub, subscription, func(_ context.Context, _ outbox.Entry) (outbox.DeliveryOutcome, outbox.Settlement) {
		return outbox.DeliveryOutcome{Disposition: outbox.DispositionAck}, &recordingSettlement{}
	})
	defer cancel()

	okTopic := "test/metrics/" + uuid.NewString()
	publishTo(t, conn, okTopic, newSubEnvelope(t, okTopic, []byte(`{"ok":true}`)))
	testwait.External(t, "success-recorded", func() bool {
		s, _, _ := coll.snapshot()
		return s >= 1
	}, testtime.D5s, testtime.D10ms)

	poisonTopic := "test/metrics/" + uuid.NewString()
	publishTo(t, conn, poisonTopic, []byte("garbage"))
	testwait.External(t, "failure-recorded", func() bool {
		_, f, _ := coll.snapshot()
		return f >= 1
	}, testtime.D5s, testtime.D10ms)

	// Unique consumer group + unique topics under test/metrics/<uuid> make this
	// subscription's delivery set disjoint from every other parallel subtest, so
	// exact counts are safe: exactly one success (the acked envelope) and exactly
	// one failure (the poison payload). Exact (not GreaterOrEqual) catches a
	// double-delivery / double-record bug that >=1 would silently pass.
	success, failure, lastReason := coll.snapshot()
	assert.Equal(t, 1, success, "exactly one consume success")
	assert.Equal(t, 1, failure, "exactly one consume failure")
	assert.Equal(t, consumeReasonUnmarshal, lastReason, "the single failure is the poison unmarshal")
}

// TestSubscriber_InflightGauge_LifecycleNetZero drives a single successful
// delivery and asserts the mqtt_consume_inflight gauge delta stream: it peaks at
// exactly one in-flight delivery (entry +1) and nets to zero after the delivery
// settles (completion -1), via exactly two AdjustInflight calls. The drop paths
// (stopIntake / workerSem race) share the same adjustInflight helper, so this
// happy-path lifecycle test covers the entry+completion accounting for all sites.
func TestSubscriber_InflightGauge_LifecycleNetZero(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()
	coll := newRecordingSubCollector()
	sub, conn := newTestSubscriber(t, addr, coll)

	subscription := newSubscription("test/inflight/+", "inflight-cg-"+uuid.NewString())
	cancel := startSubscribe(t, sub, subscription, func(_ context.Context, _ outbox.Entry) (outbox.DeliveryOutcome, outbox.Settlement) {
		return outbox.DeliveryOutcome{Disposition: outbox.DispositionAck}, &recordingSettlement{}
	})
	defer cancel()

	topic := "test/inflight/" + uuid.NewString()
	publishTo(t, conn, topic, newSubEnvelope(t, topic, []byte(`{"k":"v"}`)))

	// Wait for the delivery to fully account: both the entry +1 and the
	// completion -1 recorded (calls >= 2) AND drained back to net zero. net==0
	// alone is true from the start (before any delivery), so it must be paired
	// with calls>=2 to actually wait for the lifecycle.
	testwait.External(t, "inflight-drained", func() bool {
		net, _, calls := coll.inflightSnapshot()
		return calls >= 2 && net == 0
	}, testtime.D5s, testtime.D10ms)

	net, peak, calls := coll.inflightSnapshot()
	assert.Equal(t, int64(0), net, "inflight gauge delta must net to zero after delivery settles")
	assert.Equal(t, int64(1), peak, "inflight peaked at exactly one in-flight delivery")
	assert.Equal(t, 2, calls, "exactly one +1 (entry) and one -1 (completion)")
}

// ---------------------------------------------------------------------------
// dispatchAck branches: commit-success-then-ack-FAIL, commit-FAIL
// ---------------------------------------------------------------------------

// TestSubscriber_DispatchAck_AckFailsAfterCommit drives the post-commit ack
// failure branch (subscriber.go dispatchAck): Settlement.Commit SUCCEEDS but
// the subsequent broker ack FAILS. This is the dangerous-redeliver case and
// must record consumeReasonAckFailed (NOT consumeReasonCommitFailed), with the
// commit half observed and NO success recorded.
//
// White-box: a Subscriber bound to the real broker cannot fail the ack
// (onPublishReceived overwrites ackClient with the live delivering client on
// every PUBLISH), so we call dispatchAck directly against a Connection whose
// ackClient is a failing fakeAcker.
func TestSubscriber_DispatchAck_AckFailsAfterCommit(t *testing.T) {
	t.Parallel()
	coll := newRecordingSubCollector()
	sub := newDispatchAckSubscriber(t, errors.New("broker gone"), coll)

	settlement := &recordingSettlement{} // commitErr nil → Commit succeeds
	pb, entry := dispatchAckEntry(t, "test/ackfail/x")
	sub.dispatchAck(context.Background(), pb, outbox.DeliveryOutcome{Disposition: outbox.DispositionAck}, settlement, entry, time.Now())

	success, failure, lastReason := coll.snapshot()
	commit, release := settlement.counts()
	assert.Equal(t, 1, commit, "Settlement.Commit must be called (and succeed) before broker ack")
	assert.Equal(t, 0, release, "ack failure after a SUCCESSFUL commit must NOT release the claim")
	assert.Equal(t, 0, success, "ack failure must NOT record consume success")
	assert.Equal(t, 1, failure, "exactly one failure recorded")
	assert.Equal(t, consumeReasonAckFailed, lastReason,
		"post-commit ack failure must record ack_failed, NOT commit_failed")
}

// TestSubscriber_DispatchAck_CommitFails drives the commit-failure branch
// (subscriber.go dispatchAck): Settlement.Commit FAILS (lease expired), so the
// message is left unacked, the claim is Released, and consumeReasonCommitFailed
// is recorded. The broker ack must NOT be attempted (success stays 0).
func TestSubscriber_DispatchAck_CommitFails(t *testing.T) {
	t.Parallel()
	coll := newRecordingSubCollector()
	// ackClient would succeed if reached — proving the ack is never attempted
	// because Commit fails first.
	sub := newDispatchAckSubscriber(t, nil, coll)

	settlement := &recordingSettlement{commitErr: errors.New("lease expired")}
	pb, entry := dispatchAckEntry(t, "test/commitfail/x")
	sub.dispatchAck(context.Background(), pb, outbox.DeliveryOutcome{Disposition: outbox.DispositionAck}, settlement, entry, time.Now())

	success, failure, lastReason := coll.snapshot()
	commit, release := settlement.counts()
	assert.Equal(t, 1, commit, "Settlement.Commit must be attempted")
	assert.Equal(t, 1, release, "commit failure must Release the claim for another holder")
	assert.Equal(t, 0, success, "commit failure must NOT record consume success (message left unacked)")
	assert.Equal(t, 1, failure, "exactly one failure recorded")
	assert.Equal(t, consumeReasonCommitFailed, lastReason,
		"commit failure must record commit_failed")
}

// ---------------------------------------------------------------------------
// StopIntake drain timeout (returns ErrAdapterMQTTSubscriberCloseTimeout)
// ---------------------------------------------------------------------------

// TestSubscriber_StopIntake_DrainTimeout verifies that when an in-flight
// handler does not finish within StopIntakeDrainTimeout, StopIntake returns an
// *errcode.Error with code ErrAdapterMQTTSubscriberCloseTimeout (NOT
// ErrAdapterMQTTClosed). A blocked handler keeps wg / inflight > 0 so the drain
// timer fires, exercising the residual-count Warn path.
func TestSubscriber_StopIntake_DrainTimeout(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()
	sub, conn := newTestSubscriber(t, addr, nil)
	// Tighten the drain budget so the timeout fires fast; per-call timeout small
	// so UNSUBSCRIBE during StopIntake does not dominate the wait.
	sub.config.StopIntakeDrainTimeout = testtime.D50ms
	sub.config.StopIntakePerCallTimeout = testtime.D50ms

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	// Unblock the stuck handler at cleanup so the in-flight goroutine is not
	// leaked past the test.
	t.Cleanup(func() { close(release) })

	subscription := newSubscription("test/draintimeout/+", "draintimeout-cg-"+uuid.NewString())
	cancel := startSubscribe(t, sub, subscription, func(_ context.Context, _ outbox.Entry) (outbox.DeliveryOutcome, outbox.Settlement) {
		once.Do(func() { close(entered) })
		<-release // blocks until cleanup; keeps inflight > 0 past the drain budget
		return outbox.DeliveryOutcome{Disposition: outbox.DispositionAck}, nil
	})
	defer cancel()

	topic := "test/draintimeout/" + uuid.NewString()
	publishTo(t, conn, topic, newSubEnvelope(t, topic, []byte(`{"k":"v"}`)))

	// Synchronize on "handler entered" so the in-flight count is real before
	// StopIntake samples the drain.
	select {
	case <-entered:
	case <-time.After(testtime.D5s):
		t.Fatal("handler never entered")
	}

	stopCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
	defer c()
	err := sub.StopIntake(stopCtx)
	require.Error(t, err, "StopIntake must return a drain-timeout error with the handler stuck")
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTSubscriberCloseTimeout, ec.Code,
		"drain timeout must be ErrAdapterMQTTSubscriberCloseTimeout, not ErrAdapterMQTTClosed")
}

// ---------------------------------------------------------------------------
// QoS validation (F10): QoS 2 rejected at construction (fail-closed)
// ---------------------------------------------------------------------------

func TestNewSubscriber_RejectsQoS2(t *testing.T) {
	t.Parallel()
	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)
	// QoS 2 is unsupported by the manual-ack idempotency model; NewSubscriber must
	// fail-closed rather than silently SUBSCRIBE at QoS 2. No broker needed — the
	// guard runs before any connection use.
	_, err = NewSubscriber(clock.Real(), &Connection{}, ns, SubscriberConfig{QoS: 2})
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

// ---------------------------------------------------------------------------
// SettlementObserver notification (F4): every broker-settlement path notifies
// the HandleResult's SettlementObservers (parity with rabbitmq + eventbus).
// ---------------------------------------------------------------------------

// TestSubscriber_NotifySettlement_FiresObservers is a white-box test driving
// dispatchDisposition directly (no broker): it asserts that a SettlementObserver
// attached to the HandleResult is invoked exactly once with the expected
// Disposition + SettlementResult on the Ack / Reject / Requeue paths.
func TestSubscriber_NotifySettlement_FiresObservers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		result     func(obs outbox.SettlementObserver) outbox.DeliveryOutcome
		wantDisp   outbox.Disposition
		wantResult outbox.SettlementResult
	}{
		{
			name: "ack",
			result: func(o outbox.SettlementObserver) outbox.DeliveryOutcome {
				return outbox.DeliveryOutcome{
					Disposition:         outbox.DispositionAck,
					SettlementObservers: []outbox.SettlementObserver{o},
				}
			},
			wantDisp:   outbox.DispositionAck,
			wantResult: outbox.SettlementResultSuccess,
		},
		{
			name: "reject",
			result: func(o outbox.SettlementObserver) outbox.DeliveryOutcome {
				return outbox.DeliveryOutcome{
					Disposition:         outbox.DispositionReject,
					Err:                 errors.New("permanent"),
					SettlementObservers: []outbox.SettlementObserver{o},
				}
			},
			wantDisp:   outbox.DispositionReject,
			wantResult: outbox.SettlementResultSuccess,
		},
		{
			// ConsumerBase exhausts its retry budget and returns a terminal
			// Reject tagged ProcessReason=retry_exhausted; the MQTT settle loop
			// must classify the settlement as RetryExhausted (parity with
			// rabbitmq + eventbus), not Success.
			name: "reject-retry-exhausted",
			result: func(o outbox.SettlementObserver) outbox.DeliveryOutcome {
				return outbox.DeliveryOutcome{
					Disposition:         outbox.DispositionReject,
					Err:                 errors.New("retry budget exhausted"),
					ProcessReason:       outbox.ProcessReasonRetryExhausted,
					SettlementObservers: []outbox.SettlementObserver{o},
				}
			},
			wantDisp:   outbox.DispositionReject,
			wantResult: outbox.SettlementResultRetryExhausted,
		},
		{
			name: "requeue",
			result: func(o outbox.SettlementObserver) outbox.DeliveryOutcome {
				return outbox.DeliveryOutcome{
					Disposition:         outbox.DispositionRequeue,
					Err:                 errors.New("transient"),
					SettlementObservers: []outbox.SettlementObserver{o},
				}
			},
			wantDisp:   outbox.DispositionRequeue,
			wantResult: outbox.SettlementResultSuccess,
		},
	}
	// Broker-backed: the Reject path now publishes to $dead (routeDeadLetter), so
	// the observer must fire through the REAL dispatch path (a synthetic fake-conn
	// would nil-panic on the dead-letter publish). Drive a real delivery whose
	// handler returns the disposition + observer in its HandleResult.
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr, stop := startInternalBroker(t)
			defer stop()
			sub, conn := newTestSubscriber(t, addr, nil)

			var count atomic.Int32
			var mu sync.Mutex
			var gotObs outbox.SettlementObservation
			obs := outbox.SettlementObserverFunc(func(_ context.Context, o outbox.SettlementObservation) {
				count.Add(1)
				mu.Lock()
				gotObs = o
				mu.Unlock()
			})

			prefix := "test/notify/" + tc.name
			subscription := newSubscription(prefix+"/+", "notify-"+tc.name+"-"+uuid.NewString())
			cancel := startSubscribe(t, sub, subscription, func(_ context.Context, _ outbox.Entry) (outbox.DeliveryOutcome, outbox.Settlement) {
				return tc.result(obs), &recordingSettlement{}
			})
			defer cancel()

			topic := prefix + "/" + uuid.NewString()
			publishTo(t, conn, topic, newSubEnvelope(t, topic, []byte(`{"k":"v"}`)))

			testwait.External(t, "settlement-observer-fired", func() bool {
				return count.Load() >= 1
			}, testtime.D5s, testtime.D10ms)

			require.Equal(t, int32(1), count.Load(), "SettlementObserver must fire exactly once")
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, tc.wantDisp, gotObs.Disposition, "observation Disposition")
			assert.Equal(t, tc.wantResult, gotObs.Result, "observation SettlementResult")
		})
	}
}

// ---------------------------------------------------------------------------
// Concurrent dispatch (F3-design fix): a slow handler must not block intake
// ---------------------------------------------------------------------------

// TestSubscriber_ConcurrentDispatch_SlowHandlerDoesNotBlock proves the bounded
// worker pool decouples handler latency from intake: while a "slow" handler is
// blocked, a subsequently-delivered "fast" message still completes. Before the
// fix, processDelivery ran synchronously in paho's single inbound-dispatch
// goroutine, so the slow handler would have stalled all further deliveries.
func TestSubscriber_ConcurrentDispatch_SlowHandlerDoesNotBlock(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()
	sub, conn := newTestSubscriber(t, addr, nil)

	const slowPayload = `{"k":"slow"}`
	block := make(chan struct{})
	var slowEntered, fastDone atomic.Bool
	t.Cleanup(func() {
		select {
		case <-block:
		default:
			close(block) // unblock the stuck handler so the goroutine is not leaked
		}
	})

	subscription := newSubscription("test/conc/+", "conc-cg-"+uuid.NewString())
	cancel := startSubscribe(t, sub, subscription, func(_ context.Context, entry outbox.Entry) (outbox.DeliveryOutcome, outbox.Settlement) {
		if string(entry.Payload()) == slowPayload {
			slowEntered.Store(true)
			<-block // block until released
		} else {
			fastDone.Store(true)
		}
		return outbox.DeliveryOutcome{Disposition: outbox.DispositionAck}, nil
	})
	defer cancel()

	// Deliver the slow message first; wait until its handler is actually in-flight.
	slowTopic := "test/conc/" + uuid.NewString()
	publishTo(t, conn, slowTopic, newSubEnvelope(t, slowTopic, []byte(slowPayload)))
	testwait.External(t, "slow-handler-entered", slowEntered.Load, testtime.D5s, testtime.D10ms)

	// Deliver the fast message; it must complete WHILE the slow handler is blocked.
	fastTopic := "test/conc/" + uuid.NewString()
	publishTo(t, conn, fastTopic, newSubEnvelope(t, fastTopic, []byte(`{"k":"fast"}`)))
	testwait.External(t, "fast-handler-done-while-slow-blocked", fastDone.Load, testtime.D5s, testtime.D10ms)

	assert.True(t, slowEntered.Load(), "slow handler must still be in-flight (proving concurrency, not serialization)")
}
