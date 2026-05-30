//go:build integration

package mqtt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
	"github.com/google/uuid"
	dockercontainer "github.com/moby/moby/api/types/container"
	dockernet "github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/tests/testutil"
)

// newTestConfig returns a Config valid for connecting to the shared Mosquitto
// container. The broker URL is normalized via testutil.LoopbackIPEndpoint so
// that "localhost"-form URLs pass the loopback-IP-literal validator in
// Config.validateBrokers (secutil.ValidateTLSEndpoint rejects DNS names for
// plaintext plaintext schemes).
func newTestConfig(t *testing.T, role string) Config {
	t.Helper()
	cid, err := ParseEphemeralClientID("itest", role)
	if err != nil {
		t.Fatalf("ParseEphemeralClientID: %v", err)
	}
	brokerURL := testutil.LoopbackIPEndpoint(sharedBrokerURL(t))
	return Config{
		ClientID:       cid,
		Brokers:        []string{brokerURL},
		ConnectTimeout: testtime.D5s,
		KeepAlive:      testtime.D10s,
		Backoff: BackoffConfig{
			BaseDelay: testtime.D100ms,
			MaxDelay:  testtime.D2s,
		},
		PublishTimeout: testtime.D5s,
	}
}

// TestIntegration_PublisherQoS1 verifies end-to-end publish against a real
// Mosquitto broker: open connection, construct Publisher, publish a small
// payload to a valid topic, assert no error returned (broker PUBACK 0x00).
func TestIntegration_PublisherQoS1(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D20s)
	defer cancel()

	cfg := newTestConfig(t, "publish-qos1")
	conn, err := Open(ctx, clock.Real(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = conn.Close(cleanupCtx)
	})

	ns, err := ParseTopicNamespace("itest")
	if err != nil {
		t.Fatalf("ParseTopicNamespace: %v", err)
	}
	pub, err := NewPublisher(clock.Real(), conn, ns)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = pub.Close(cleanupCtx)
	})

	topic := "itest/qos1/" + uuid.NewString()
	if err := pub.Publish(ctx, topic, []byte(`{"hello":"world"}`)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

// TestIntegration_PublisherReconnect verifies that after a keep-alive interval
// passes, the autopaho connection remains healthy and subsequent publishes
// succeed. We use a short KeepAlive (2s) and sleep > that interval to exercise
// the keep-alive ping path — the test confirms the connection survives the
// heartbeat exchange transparently.
func TestIntegration_PublisherReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D30s)
	defer cancel()

	cfg := newTestConfig(t, "publish-reconnect")
	cfg.KeepAlive = testtime.D2s // tighter keep-alive for quick failure detection

	conn, err := Open(ctx, clock.Real(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = conn.Close(cleanupCtx)
	})

	ns, err := ParseTopicNamespace("itest")
	if err != nil {
		t.Fatalf("ParseTopicNamespace: %v", err)
	}
	pub, err := NewPublisher(clock.Real(), conn, ns)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = pub.Close(cleanupCtx)
	})

	topic := "itest/reconnect/" + uuid.NewString()
	if err := pub.Publish(ctx, topic, []byte(`{"seq":1}`)); err != nil {
		t.Fatalf("Publish seq=1: %v", err)
	}

	// Sleep > KeepAlive interval — exercises keep-alive ping path.
	// Wall-clock time must pass so the KeepAlive timer fires and exercises
	// the broker heartbeat path; polling does not accelerate KeepAlive.
	time.Sleep(testtime.D3s) //archtest:allow:test-sleep keep-alive-heartbeat-traversal: wall-clock must elapse for KeepAlive timer to fire

	if err := pub.Publish(ctx, topic, []byte(`{"seq":2}`)); err != nil {
		t.Fatalf("Publish seq=2 (post keep-alive): %v", err)
	}
}

// TestIntegration_PublisherPubAckTimeout verifies that when PublishTimeout
// fires before the broker can respond, the error is classified as PUBACK
// timeout. We provoke this by setting a very short PublishTimeout (1ns) and
// expect the context deadline to fire immediately, surfacing as
// ErrAdapterMQTTPubAckTimeout per publisher.go's wrapPublishErr.
func TestIntegration_PublisherPubAckTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	cfg := newTestConfig(t, "publish-timeout")
	cfg.PublishTimeout = time.Nanosecond // intentionally fires immediately

	conn, err := Open(ctx, clock.Real(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = conn.Close(cleanupCtx)
	})

	ns, err := ParseTopicNamespace("itest")
	if err != nil {
		t.Fatalf("ParseTopicNamespace: %v", err)
	}
	pub, err := NewPublisher(clock.Real(), conn, ns)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = pub.Close(cleanupCtx)
	})

	topic := "itest/timeout/" + uuid.NewString()
	err = pub.Publish(ctx, topic, []byte(`{"x":1}`))
	if err == nil {
		t.Fatalf("Publish succeeded; expected ErrAdapterMQTTPubAckTimeout")
	}
	// wrapPublishErr maps context.DeadlineExceeded → ErrAdapterMQTTPubAckTimeout.
	// Assert via typed errcode check rather than fragile string matching.
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("Publish err is not *errcode.Error: %v", err)
	}
	if ec.Code != ErrAdapterMQTTPubAckTimeout {
		t.Fatalf("Publish err code = %v, want %v", ec.Code, ErrAdapterMQTTPubAckTimeout)
	}
}

// TestIntegration_PublisherTrueReconnect verifies that the autopaho connection
// fully reconnects after a broker restart: it publishes, stops the broker
// container, waits for WaitConnected to return after the container restarts,
// then publishes again successfully.
func TestIntegration_PublisherTrueReconnect(t *testing.T) {
	testutil.RequireDocker(t)

	// Bring up a dedicated broker — NOT the shared one — so we can stop/start it.
	dedicatedURL, container, err := startDedicatedMosquittoContainer(t)
	if err != nil {
		t.Fatalf("start dedicated broker: %v", err)
	}
	t.Cleanup(func() {
		termCtx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
		defer cancel()
		_ = container.Terminate(termCtx)
	})

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D60s)
	defer cancel()

	cid, err := ParseEphemeralClientID("itest", "true-reconnect")
	if err != nil {
		t.Fatalf("ParseEphemeralClientID: %v", err)
	}
	cfg := Config{
		ClientID:       cid,
		Brokers:        []string{testutil.LoopbackIPEndpoint(dedicatedURL)},
		ConnectTimeout: testtime.D5s,
		KeepAlive:      testtime.D10s,
		Backoff: BackoffConfig{
			BaseDelay: testtime.D100ms,
			MaxDelay:  testtime.D2s,
		},
		PublishTimeout: testtime.D5s,
	}

	conn, err := Open(ctx, clock.Real(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = conn.Close(cleanupCtx)
	})

	ns, err := ParseTopicNamespace("itest")
	if err != nil {
		t.Fatalf("ParseTopicNamespace: %v", err)
	}
	pub, err := NewPublisher(clock.Real(), conn, ns)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = pub.Close(cleanupCtx)
	})

	topic := "itest/true-reconnect/" + uuid.NewString()

	// seq=1: initial publish — must succeed with broker up.
	if err := pub.Publish(ctx, topic, []byte(`{"seq":1}`)); err != nil {
		t.Fatalf("Publish seq=1: %v", err)
	}

	// Stop the broker — triggers disconnection.
	stopTimeout := testtime.D10s
	if err := container.Stop(ctx, &stopTimeout); err != nil {
		t.Fatalf("container.Stop: %v", err)
	}

	// Restart the broker container.
	if err := container.Start(ctx); err != nil {
		t.Fatalf("container.Start: %v", err)
	}

	// Wait for autopaho to reconnect.
	reconnectCtx, reconnectCancel := context.WithTimeout(ctx, testtime.D30s)
	defer reconnectCancel()
	if err := conn.WaitConnected(reconnectCtx); err != nil {
		t.Fatalf("WaitConnected after restart: %v", err)
	}

	// seq=2: publish after full reconnect — must succeed.
	if err := pub.Publish(ctx, topic, []byte(`{"seq":2}`)); err != nil {
		t.Fatalf("Publish seq=2 (post restart): %v", err)
	}
}

// ---------------------------------------------------------------------------
// PR-3 Subscriber integration cases
// ---------------------------------------------------------------------------

// newSubscriberForITest opens a connection to the shared broker and builds a
// Subscriber bound to the "itest" namespace.
func newSubscriberForITest(t *testing.T, role string) (*Subscriber, *Connection) {
	t.Helper()
	// autopaho binds the ConnectionManager lifecycle to the ctx passed to Open:
	// canceling it tears the connection down. Since this is a HELPER (unlike the
	// publisher tests whose `defer cancel()` lives in the test body), a
	// `context.WithTimeout + defer cancel()` would fire the moment the helper
	// returns — killing the manager before the test ever subscribes and leaving
	// every cm.Subscribe with ConnectionDownError ("subscribe not ready"). Scope
	// cancel to t.Cleanup so the manager outlives the helper; cfg.ConnectTimeout
	// already bounds the bootstrap connect inside Open. Mirrors the unit
	// newTestSubscriber fix.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cfg := newTestConfig(t, role)
	conn, err := Open(ctx, clock.Real(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		c, cn := context.WithTimeout(context.Background(), testtime.D5s)
		defer cn()
		_ = conn.Close(c)
	})
	ns, err := ParseTopicNamespace("itest")
	if err != nil {
		t.Fatalf("ParseTopicNamespace: %v", err)
	}
	sub, err := NewSubscriber(clock.Real(), conn, ns, SubscriberConfig{})
	if err != nil {
		t.Fatalf("NewSubscriber: %v", err)
	}
	return sub, conn
}

// itestSubscription builds an outbox.Subscription for the itest namespace.
func itestSubscription(topic, group string) outbox.Subscription {
	return outbox.Subscription{
		Topic:             topic,
		ConsumerGroup:     group,
		CellID:            "itest",
		ContractID:        "event.itest.v1",
		ContractKind:      "event",
		ContractTransport: "mqtt",
	}
}

// itestEnvelope marshals a valid v1 outbox envelope.
func itestEnvelope(t *testing.T, topic string, payload []byte) []byte {
	t.Helper()
	entry := mustNewEntry(t, "itest.event", payload, outbox.WithID(uuid.NewString()), outbox.WithTopic(topic))
	raw, err := outbox.MarshalEnvelope(entry)
	if err != nil {
		t.Fatalf("MarshalEnvelope: %v", err)
	}
	return raw
}

// itestPublish publishes payload to topic via conn under the itest namespace.
func itestPublish(t *testing.T, conn *Connection, topic string, payload []byte) {
	t.Helper()
	ns, err := ParseTopicNamespace("itest")
	if err != nil {
		t.Fatalf("ParseTopicNamespace: %v", err)
	}
	pt, err := ns.Mint(topic)
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D5s)
	defer cancel()
	if _, err := conn.Publish(ctx, pt, payload, publishOpts{QoS: 1}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

// TestIntegration_Subscriber_Disposition3State exercises Ack / Requeue / Reject
// end-to-end against real Mosquitto: each disposition is driven by a distinct
// message and the broker outcome (commit/release/ack) is asserted via a
// recording settlement + collector.
func TestIntegration_Subscriber_Disposition3State(t *testing.T) {
	cases := []struct {
		name        string
		result      outbox.HandleResult
		wantSuccess int
		wantReason  ConsumeFailureReason
		wantCommit  int
		wantRelease int
	}{
		{"ack", outbox.Ack(), 1, "", 1, 0},
		{"requeue", outbox.Requeue(errors.New("transient")), 0, consumeReasonRequeue, 0, 1},
		{"reject", outbox.Reject(errors.New("permanent")), 0, consumeReasonReject, 0, 1},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			coll := newRecordingSubCollector()
			sub, conn := newSubscriberForITest(t, "sub-disp-"+tc.name)
			// Re-bind with collector by constructing a fresh subscriber on same conn.
			ns, err := ParseTopicNamespace("itest")
			if err != nil {
				t.Fatalf("ns: %v", err)
			}
			sub, err = NewSubscriber(clock.Real(), conn, ns, SubscriberConfig{}, WithSubscriberCollector(coll))
			if err != nil {
				t.Fatalf("NewSubscriber: %v", err)
			}

			settlement := &recordingSettlement{}
			// Per-subtest topic prefix makes each subtest's filter disjoint, so
			// shared-subscription fanout cannot deliver one subtest's PUBLISH to
			// another (mirrors the unit DispositionMatrix fix). Consumer group is
			// still unique per subtest as a second layer of isolation.
			prefix := "itest/disp/" + tc.name
			filter := prefix + "/+"
			subscription := itestSubscription(filter, "disp-"+tc.name+"-"+uuid.NewString())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				_ = sub.Subscribe(ctx, subscription, func(_ context.Context, _ outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
					return tc.result, settlement
				})
			}()
			select {
			case <-sub.Ready(subscription):
			case <-time.After(testtime.D10s):
				t.Fatal("subscribe not ready")
			}

			topic := prefix + "/" + uuid.NewString()
			itestPublish(t, conn, topic, itestEnvelope(t, topic, []byte(`{"k":"v"}`)))

			deadline := time.Now().Add(testtime.D10s)
			for time.Now().Before(deadline) {
				s, f, _ := coll.snapshot()
				if s+f >= 1 {
					break
				}
				time.Sleep(testtime.D10ms) //archtest:allow:test-sleep poll-loop: real broker delivery latency
			}
			success, failure, reason := coll.snapshot()
			commit, release := settlement.counts()
			if success != tc.wantSuccess {
				t.Errorf("success = %d, want %d", success, tc.wantSuccess)
			}
			if commit != tc.wantCommit {
				t.Errorf("commit = %d, want %d", commit, tc.wantCommit)
			}
			if release != tc.wantRelease {
				t.Errorf("release = %d, want %d", release, tc.wantRelease)
			}
			if tc.wantReason != "" {
				if failure < 1 || reason != tc.wantReason {
					t.Errorf("failure=%d reason=%q, want reason %q", failure, reason, tc.wantReason)
				}
			}
		})
	}
}

// TestIntegration_Subscriber_SessionRecovery verifies clean=false session
// persistence on the GoCell consumer-group path, which ALWAYS uses a
// $share/{group}/{filter} shared subscription (the Subscriber derives the wire
// filter from subscription.ConsumerGroup via TopicNamespace.MintFilter).
//
// DETERMINISTIC hard assertion: a subscriber with SessionExpiry > 0 and a STABLE
// clientId subscribes, the connection is dropped (Open-ctx cancel — unclean, so
// the broker retains the persistent session + subscription rather than the
// unsubscribe-all that Connection.Close performs), then a fresh connection with
// the same clientId resumes the session and a message published while it is
// ONLINE is delivered. That proves session resume + $share subscription re-arm
// end-to-end and can go RED if either breaks.
//
// Offline-queued redelivery (messages published while NO group member is
// connected) is recorded for observability ONLY — NOT asserted. Whether the
// broker offline-queues for a $share member that dropped uncleanly is
// environment/timing-dependent: observed redelivered=3/3 on a local mosquitto
// via ctx-cancel teardown, but 0 on CI (same pinned image). It is not a $share
// contract, so the GoCell consumer-group path does not depend on it for
// durability — that comes from the producer-side transactional outbox + QoS1 +
// idempotent consumers. See the ADR "$share offline redelivery" amendment.
func TestIntegration_Subscriber_SessionRecovery(t *testing.T) {
	// Mosquitto persists per-clientId sessions. Use a dedicated container so the
	// session lifecycle is isolated from the shared broker.
	dedicatedURL, container, err := startDedicatedMosquittoContainer(t)
	if err != nil {
		t.Fatalf("start dedicated broker: %v", err)
	}
	t.Cleanup(func() {
		termCtx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
		defer cancel()
		_ = container.Terminate(termCtx)
	})

	stableID, err := ParseStableClientID("itest", "session", "recovery-fixed-001")
	if err != nil {
		t.Fatalf("ParseStableClientID: %v", err)
	}
	mkCfg := func() Config {
		return Config{
			ClientID:       stableID,
			Brokers:        []string{testutil.LoopbackIPEndpoint(dedicatedURL)},
			ConnectTimeout: testtime.D5s,
			KeepAlive:      testtime.D10s,
			SessionExpiry:  testtime.D30s, // > 0 → persistent session, clean=false
			Backoff:        BackoffConfig{BaseDelay: testtime.D100ms, MaxDelay: testtime.D2s},
			PublishTimeout: testtime.D5s,
		}
	}

	ns, err := ParseTopicNamespace("itest")
	if err != nil {
		t.Fatalf("ns: %v", err)
	}
	filter := "itest/session/data"
	group := "session-cg-" + uuid.NewString()
	subscription := itestSubscription(filter, group)

	// Phase 1: subscribe to establish the broker-side shared subscription, then
	// drop the connection by canceling the Open ctx (NOT Connection.Close, which
	// runs unsubscribeAll + a clean DISCONNECT). A cancelable (non-timeout) ctx
	// puts the manager lifecycle solely under cancel1: canceling it tears down the
	// autopaho manager / TCP uncleanly, so the broker retains the SessionExpiry>0
	// session + its subscription for the phase-2 resume.
	ctx1, cancel1 := context.WithCancel(context.Background())
	conn1, err := Open(ctx1, clock.Real(), mkCfg())
	if err != nil {
		cancel1()
		t.Fatalf("Open conn1: %v", err)
	}
	sub1, err := NewSubscriber(clock.Real(), conn1, ns, SubscriberConfig{})
	if err != nil {
		cancel1()
		t.Fatalf("NewSubscriber 1: %v", err)
	}
	// sub1.Subscribe runs under ctx1 directly: canceling ctx1 (below) both drops
	// the autopaho manager (unclean disconnect → session retained) AND unblocks
	// this Subscribe call. No separate sub-ctx is needed — a derived cancel that
	// is only invoked on the timeout path would leak on the happy path (govet
	// lostcancel).
	go func() {
		_ = sub1.Subscribe(ctx1, subscription, func(_ context.Context, _ outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
			return outbox.Ack(), nil
		})
	}()
	select {
	case <-sub1.Ready(subscription):
	case <-time.After(testtime.D10s):
		cancel1()
		t.Fatal("phase-1 subscribe not ready")
	}
	// Drop the connection UNCLEANLY (ctx cancel) — do NOT call subCancel1 first
	// (that would UNSUBSCRIBE via the Subscribe-returned cancel) and do NOT call
	// conn1.Close (that unsubscribes-all). Canceling ctx1 tears down the manager,
	// leaving the persistent session + subscription on the broker.
	cancel1()
	// archtest:allow:test-sleep teardown-settle: wall-clock must elapse for the
	// broker to observe the unclean TCP drop before the offline publish; there is
	// no client-side signal to poll on (the manager is already gone).
	time.Sleep(testtime.D1s) //archtest:allow:test-sleep teardown-settle

	// Publish while offline. On some brokers/timings the persistent session +
	// $share subscription offline-queue these and redeliver on resume; this is
	// recorded for observability but not asserted (see the doc-comment + ADR).
	pubCfg := newTestConfig(t, "session-pub")
	pubCfg.Brokers = []string{testutil.LoopbackIPEndpoint(dedicatedURL)}
	pubCtx, pubCancel := context.WithTimeout(context.Background(), testtime.D20s)
	pubConn, err := Open(pubCtx, clock.Real(), pubCfg)
	if err != nil {
		pubCancel()
		t.Fatalf("Open pub: %v", err)
	}
	const offlineCount = 3
	for i := 0; i < offlineCount; i++ {
		itestPublish(t, pubConn, filter, itestEnvelope(t, filter, []byte(fmt.Sprintf(`{"seq":%d}`, i))))
	}
	_ = pubConn.Close(context.Background())
	pubCancel()

	// Phase 2: reconnect with the SAME clientId + persistent session.
	ctx2, cancel2 := context.WithTimeout(context.Background(), testtime.D30s)
	defer cancel2()
	conn2, err := Open(ctx2, clock.Real(), mkCfg())
	if err != nil {
		t.Fatalf("Open conn2: %v", err)
	}
	t.Cleanup(func() { _ = conn2.Close(context.Background()) })
	sub2, err := NewSubscriber(clock.Real(), conn2, ns, SubscriberConfig{})
	if err != nil {
		t.Fatalf("NewSubscriber 2: %v", err)
	}
	// Separate offline-tagged deliveries (payload {"seq":<int>}) from the fresh
	// post-resume online probe ({"seq":"online"}): the online count is the hard
	// assertion (deterministic), the offline count is observability only.
	var offlineReceived atomic.Int64
	var onlineReceived atomic.Int64
	subCtx2, subCancel2 := context.WithCancel(ctx2)
	defer subCancel2()
	go func() {
		_ = sub2.Subscribe(subCtx2, subscription, func(_ context.Context, entry outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
			if isOfflineTaggedPayload(entry.Payload()) {
				offlineReceived.Add(1)
			} else {
				onlineReceived.Add(1)
			}
			return outbox.Ack(), nil
		})
	}()
	select {
	case <-sub2.Ready(subscription):
	case <-time.After(testtime.D10s):
		t.Fatal("phase-2 subscribe not ready")
	}

	// Publish a fresh online message AFTER resume. Delivery of a message published
	// while the resumed shared-subscription member is connected is the
	// DETERMINISTIC invariant this test asserts: it proves the persistent session
	// resumed and the $share subscription was re-armed end-to-end.
	itestPublish(t, conn2, filter, itestEnvelope(t, filter, []byte(`{"seq":"online"}`)))

	// HARD assertion (deterministic, can-go-RED): the resumed persistent session
	// MUST deliver a message published while it is online. This fails if session
	// resume or subscription re-arm is broken.
	deadline := time.Now().Add(testtime.D15s)
	for time.Now().Before(deadline) {
		if onlineReceived.Load() >= 1 {
			break
		}
		time.Sleep(testtime.D20ms) //archtest:allow:test-sleep poll-loop: real broker delivery latency
	}
	if onlineReceived.Load() < 1 {
		t.Fatalf("resumed persistent session did not deliver an online message "+
			"(offline=%d online=%d); session resume / subscription re-arm is broken",
			offlineReceived.Load(), onlineReceived.Load())
	}

	// Offline-queued redelivery is recorded for observability ONLY — it is NOT a
	// hard assertion. Whether the broker offline-queues messages for a $share
	// member that dropped uncleanly is environment- and timing-dependent (it hinges
	// on the broker observing the disconnect as non-clean AND retaining the share
	// session before the offline publish): it is reliable on some mosquitto
	// builds/timings and not others (observed redelivered locally, 0 on CI). The
	// GoCell consumer-group path does NOT rely on broker offline queues for
	// durability — that comes from the producer-side transactional outbox (retry
	// until an online consumer acks) + QoS1 + idempotent consumers. See the ADR
	// "$share offline redelivery" amendment.
	t.Logf("session-recovery observability: offline-redelivered=%d/%d online=%d",
		offlineReceived.Load(), offlineCount, onlineReceived.Load())
}

// isOfflineTaggedPayload reports whether an envelope payload is one of the
// offline-published messages (payload {"seq":<int>}). The post-resume online
// probe carries {"seq":"online"} (a JSON string), which fails the numeric
// decode and is therefore NOT treated as an offline message. This lets
// TestIntegration_Subscriber_SessionRecovery count offline redelivery distinctly
// from the online liveness probe.
func isOfflineTaggedPayload(payload []byte) bool {
	var probe struct {
		Seq *int `json:"seq"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return false
	}
	return probe.Seq != nil
}

// TestIntegration_Subscriber_ClientIDConflict verifies that a second connection
// with the SAME clientId causes the broker to send a DISCONNECT (0x8E session
// taken over) to the first connection. The first connection observes this via
// onServerDisconnect → connection transitions out of healthy; we assert the
// first connection's Health becomes non-nil (or it observes a disconnect).
func TestIntegration_Subscriber_ClientIDConflict(t *testing.T) {
	dedicatedURL, container, err := startDedicatedMosquittoContainer(t)
	if err != nil {
		t.Fatalf("start dedicated broker: %v", err)
	}
	t.Cleanup(func() {
		termCtx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
		defer cancel()
		_ = container.Terminate(termCtx)
	})

	stableID, err := ParseStableClientID("itest", "conflict", "fixed-001")
	if err != nil {
		t.Fatalf("ParseStableClientID: %v", err)
	}
	mkCfg := func() Config {
		return Config{
			ClientID:       stableID,
			Brokers:        []string{testutil.LoopbackIPEndpoint(dedicatedURL)},
			ConnectTimeout: testtime.D5s,
			KeepAlive:      testtime.D10s,
			Backoff:        BackoffConfig{BaseDelay: testtime.D100ms, MaxDelay: testtime.D2s},
			PublishTimeout: testtime.D5s,
		}
	}

	ctx1, cancel1 := context.WithTimeout(context.Background(), testtime.D20s)
	defer cancel1()
	conn1, err := Open(ctx1, clock.Real(), mkCfg())
	if err != nil {
		t.Fatalf("Open conn1: %v", err)
	}
	t.Cleanup(func() { _ = conn1.Close(context.Background()) })
	if hErr := conn1.Health(ctx1); hErr != nil {
		t.Fatalf("conn1 should be healthy initially: %v", hErr)
	}

	// Second connection with the SAME clientId — broker evicts the first
	// (session taken over). autopaho on conn1 will see OnConnectionDown.
	ctx2, cancel2 := context.WithTimeout(context.Background(), testtime.D20s)
	defer cancel2()
	conn2, err := Open(ctx2, clock.Real(), mkCfg())
	if err != nil {
		t.Fatalf("Open conn2 (same clientId): %v", err)
	}
	t.Cleanup(func() { _ = conn2.Close(context.Background()) })

	// conn1 should transition out of the healthy phase: the broker DISCONNECT /
	// TCP close drives OnConnectionDown → phaseDisconnected, then autopaho retries
	// (and may steal the session back, oscillating). MQTT v5 session-takeover
	// (DISCONNECT 0x8E on a same-clientId reconnect) is spec-MANDATED, so this is
	// a HARD assertion: conn1 MUST be observed non-healthy within the window. It
	// is NOT skipped — a broker that never evicts the first session is a real
	// regression this test exists to catch.
	observedUnhealthy := false
	deadline := time.Now().Add(testtime.D10s)
	for time.Now().Before(deadline) {
		if conn1.Health(context.Background()) != nil {
			observedUnhealthy = true
			break
		}
		time.Sleep(testtime.D50ms) //archtest:allow:test-sleep poll-loop: wait for broker session-takeover disconnect
	}
	if !observedUnhealthy {
		t.Fatalf("conn1 never transitioned to unhealthy after a same-clientId reconnect; " +
			"MQTT v5 session takeover (DISCONNECT 0x8E) did not occur — broker eviction is broken")
	}
}

// startDedicatedMosquittoContainer starts an eclipse-mosquitto container for
// exclusive use by TestIntegration_PublisherTrueReconnect (stop/start lifecycle).
//
// The 1883 container port is bound to a FIXED host port via PortBindings rather
// than the default random ephemeral mapping. This is essential for the reconnect
// test: a Stop()/Start() restart re-publishes the container's ports, and with a
// random mapping Docker assigns a NEW host port on restart — leaving the broker
// URL captured here stale, so autopaho keeps dialing the dead old port and
// WaitConnected times out. Pinning the host port keeps the endpoint stable
// across restart so reconnection can actually succeed.
func startDedicatedMosquittoContainer(t *testing.T) (string, testcontainers.Container, error) {
	t.Helper()
	testutil.RequireDocker(t) // INTEGRATION-GUARD: fail-fast/skip before starting a testcontainer
	ctx := context.Background()

	hostPort, err := freeLoopbackTCPPort()
	if err != nil {
		return "", nil, err
	}
	hostPortStr := strconv.Itoa(hostPort)

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        testutil.MosquittoImage,
			ExposedPorts: []string{"1883/tcp"},
			Files: []testcontainers.ContainerFile{
				{
					Reader:            strings.NewReader(mosquittoConf),
					ContainerFilePath: "/mosquitto/config/mosquitto.conf",
					FileMode:          0o644,
				},
			},
			HostConfigModifier: func(hc *dockercontainer.HostConfig) {
				hc.PortBindings = dockernet.PortMap{
					dockernet.MustParsePort("1883/tcp"): []dockernet.PortBinding{
						{HostPort: hostPortStr},
					},
				}
			},
			WaitingFor: wait.ForListeningPort("1883/tcp").
				WithStartupTimeout(testtime.D30s),
		},
		Started: true,
	})
	if err != nil {
		return "", nil, err
	}
	// Endpoint is the pinned host port, stable across Stop()/Start().
	url := "tcp://127.0.0.1:" + hostPortStr
	return url, container, nil
}

// freeLoopbackTCPPort allocates and immediately releases a loopback TCP port,
// returning its number for use as a fixed container host-port binding. The
// close-then-rebind window is small and test-binary-scoped (no external
// competitor for a loopback ephemeral port within one `go test` run) — the same
// idiom the in-process mochi broker tests use.
func freeLoopbackTCPPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if cerr := ln.Close(); cerr != nil {
		return 0, cerr
	}
	return port, nil
}

// dltCapture holds the first message received by the DLT watcher.
type dltCapture struct {
	mu      sync.Mutex
	topic   string
	payload []byte
	gotCh   chan struct{}
	once    sync.Once
}

func newDLTCapture() *dltCapture {
	return &dltCapture{gotCh: make(chan struct{})}
}

func (d *dltCapture) record(topic string, payload []byte) {
	d.once.Do(func() {
		d.mu.Lock()
		d.topic = topic
		d.payload = make([]byte, len(payload))
		copy(d.payload, payload)
		d.mu.Unlock()
		close(d.gotCh)
	})
}

func (d *dltCapture) snapshot() (topic string, payload []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.topic, d.payload
}

// newRawDLTWatcher starts a raw autopaho connection subscribed to filter
// (e.g. "$dead/itest/#") and records the first matching PUBLISH into cap.
// It connects to brokerURL using a fresh ephemeral client ID. The returned
// cleanup func disconnects the manager and must be called at test end.
func newRawDLTWatcher(
	t *testing.T, brokerURL string, filter string, cap *dltCapture,
) (cleanup func()) {
	t.Helper()
	rawCID, err := ParseEphemeralClientID("itest", "dlt-watcher")
	if err != nil {
		t.Fatalf("newRawDLTWatcher: ParseEphemeralClientID: %v", err)
	}
	u, err := url.Parse(brokerURL)
	if err != nil {
		t.Fatalf("newRawDLTWatcher: url.Parse(%q): %v", brokerURL, err)
	}

	watchCtx, watchCancel := context.WithCancel(context.Background())
	connectedCh := make(chan struct{}, 1)

	cfg := autopaho.ClientConfig{
		ServerUrls: []*url.URL{u},
		KeepAlive:  10,
		OnConnectionUp: func(_ *autopaho.ConnectionManager, _ *paho.Connack) {
			select {
			case connectedCh <- struct{}{}:
			default:
			}
		},
		OnConnectError: func(err error) {
			t.Logf("newRawDLTWatcher: connect error: %v", err)
		},
		ClientConfig: paho.ClientConfig{
			ClientID: rawCID.String(),
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				func(pr paho.PublishReceived) (bool, error) {
					if pr.Packet != nil {
						cap.record(pr.Packet.Topic, pr.Packet.Payload)
					}
					return true, nil
				},
			},
		},
	}

	cm, err := autopaho.NewConnection(watchCtx, cfg)
	if err != nil {
		watchCancel()
		t.Fatalf("newRawDLTWatcher: autopaho.NewConnection: %v", err)
	}

	// Wait for the watcher to connect before returning.
	select {
	case <-connectedCh:
	case <-time.After(testtime.D15s):
		watchCancel()
		t.Fatal("newRawDLTWatcher: timed out waiting for DLT watcher to connect")
	}

	// Subscribe to the $dead filter.
	subCtx, subCancel := context.WithTimeout(watchCtx, testtime.D10s)
	defer subCancel()
	_, subErr := cm.Subscribe(subCtx, &paho.Subscribe{
		Subscriptions: []paho.SubscribeOptions{
			{Topic: filter, QoS: 1},
		},
	})
	if subErr != nil {
		watchCancel()
		t.Fatalf("newRawDLTWatcher: cm.Subscribe(%q): %v", filter, subErr)
	}

	return func() {
		watchCancel()
		disconnCtx, disconnCancel := context.WithTimeout(context.Background(), testtime.D5s)
		defer disconnCancel()
		_ = cm.Disconnect(disconnCtx)
	}
}

// TestIntegration_Subscriber_DLTCapture proves that when a handler returns
// outbox.Reject, the adapter publishes the rejected envelope to
// "$dead/<originalTopic>" and that payload is readable by an independent
// raw MQTT subscriber — not just that a metric incremented.
//
// The DLT watcher is a plain autopaho.ConnectionManager subscribed directly to
// "$dead/itest/#". The GoCell Subscriber.Subscribe cannot subscribe to $dead/...
// (SubscribeOK rejects it — outside namespace, contains $), so the watcher must
// be a raw client. This test fails if routeDeadLetter is broken or if the
// "$dead" prefix routing is not actually delivering to the topic.
func TestIntegration_Subscriber_DLTCapture(t *testing.T) {
	brokerURL := sharedBrokerURL(t)
	rawURL := testutil.LoopbackIPEndpoint(brokerURL)

	// Per-test unique topic so parallel test runs cannot cross-deliver.
	topicSuffix := uuid.NewString()
	originalTopic := "itest/dlt/" + topicSuffix
	expectedDLTTopic := "$dead/" + originalTopic

	// Start the DLT watcher BEFORE publishing so it is subscribed when the
	// adapter routes to $dead/<topic>. "$dead/itest/#" is a raw MQTT wildcard —
	// the GoCell Subscriber cannot subscribe to it (outside its namespace).
	cap := newDLTCapture()
	watcherCleanup := newRawDLTWatcher(t, rawURL, "$dead/itest/#", cap)
	t.Cleanup(watcherCleanup)

	// Build a GoCell connection + subscriber + publisher on the shared broker.
	sub, conn := newSubscriberForITest(t, "dlt-capture-"+topicSuffix[:8])

	// Build the envelope that will be published and later appear verbatim on $dead.
	envelope := itestEnvelope(t, originalTopic, []byte(`{"dlt":"capture"}`))

	// Subscribe the GoCell subscriber to originalTopic with a handler that
	// always Rejects — this drives routeDeadLetter.
	subscription := itestSubscription(originalTopic, "dlt-cg-"+topicSuffix[:8])
	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()
	go func() {
		_ = sub.Subscribe(subCtx, subscription,
			func(_ context.Context, _ outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
				return outbox.Reject(errors.New("permanent-test")), nil
			})
	}()
	select {
	case <-sub.Ready(subscription):
	case <-time.After(testtime.D10s):
		t.Fatal("dlt capture: GoCell subscriber not ready within 10s")
	}

	// Publish the envelope. The adapter receives it, calls the handler (Reject),
	// then routes pb.Payload (the original envelope bytes) to $dead/<originalTopic>.
	itestPublish(t, conn, originalTopic, envelope)

	// Poll until the DLT watcher receives the message or timeout.
	deadline := time.Now().Add(testtime.D15s)
	for time.Now().Before(deadline) {
		select {
		case <-cap.gotCh:
			goto assertDLT
		default:
		}
		time.Sleep(testtime.D50ms) //archtest:allow:test-sleep poll-loop: real broker delivery latency
	}
	t.Fatal("dlt capture: DLT watcher did not receive a message within 15s; " +
		"routeDeadLetter may be broken or $dead/<topic> is not being published")

assertDLT:
	gotTopic, gotPayload := cap.snapshot()
	if gotTopic != expectedDLTTopic {
		t.Errorf("DLT topic = %q, want %q", gotTopic, expectedDLTTopic)
	}
	if !bytes.Equal(gotPayload, envelope) {
		t.Errorf("DLT payload mismatch:\n  got  %q\n  want %q", gotPayload, envelope)
	}
}
