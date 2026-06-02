//go:build !integration

package mqtt

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/eclipse/paho.golang/paho"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// ---------------------------------------------------------------------------
// route registry (register/deregister) — pure unit, no broker
// ---------------------------------------------------------------------------

func TestConnection_RouteRegistry(t *testing.T) {
	t.Parallel()
	ns, err := ParseTopicNamespace("ns")
	require.NoError(t, err)
	f1, err := ns.MintFilter("cg", "ns/a")
	require.NoError(t, err)
	f2, err := ns.MintFilter("cg", "ns/b")
	require.NoError(t, err)

	c := &Connection{}
	noop := func(*paho.Publish) {}

	c.registerRoute(mqttRoute{filter: f1, dispatch: noop})
	c.registerRoute(mqttRoute{filter: f2, dispatch: noop})

	c.subMu.RLock()
	require.Len(t, c.routes, 2)
	c.subMu.RUnlock()

	// Deregister f1 by wire form; f2 must remain.
	c.deregisterRoute(f1.String())
	c.subMu.RLock()
	require.Len(t, c.routes, 1)
	assert.Equal(t, f2.String(), c.routes[0].filter.String())
	c.subMu.RUnlock()

	// Deregistering an unknown filter is a no-op.
	c.deregisterRoute("$share/cg/ns/zzz")
	c.subMu.RLock()
	require.Len(t, c.routes, 1)
	c.subMu.RUnlock()

	// Deregister the last one.
	c.deregisterRoute(f2.String())
	c.subMu.RLock()
	require.Empty(t, c.routes)
	c.subMu.RUnlock()
}

// ---------------------------------------------------------------------------
// onPublishReceived dispatch — pure unit, no broker
// ---------------------------------------------------------------------------

// pubWithSubID builds a delivered PUBLISH carrying the given MQTT v5 Subscription
// Identifier (or none when subID == 0).
func pubWithSubID(topic string, subID int) *paho.Publish {
	pb := &paho.Publish{Topic: topic, Payload: []byte("x")}
	if subID != 0 {
		id := subID
		pb.Properties = &paho.PublishProperties{SubscriptionIdentifier: &id}
	}
	return pb
}

// TestConnection_OnPublishReceived_Dispatch verifies sub-id routing: a delivered
// PUBLISH is dispatched to the SINGLE route whose subID matches the delivered
// Subscription Identifier — the F1 regression guard. Two routes on the SAME
// topic with DIFFERENT consumer groups (distinct subIDs) must NOT both fire for
// one delivery (the old matchFilter fanout bug).
func TestConnection_OnPublishReceived_Dispatch(t *testing.T) {
	t.Parallel()
	ns, err := ParseTopicNamespace("ns")
	require.NoError(t, err)
	fA, err := ns.MintFilter("cg-a", "ns/+/temp")
	require.NoError(t, err)
	fB, err := ns.MintFilter("cg-b", "ns/+/temp") // same topic filter, different group
	require.NoError(t, err)

	var mu sync.Mutex
	var gotA, gotB []string
	c := &Connection{}
	c.registerRoute(mqttRoute{subID: 1, filter: fA, dispatch: func(pb *paho.Publish) {
		mu.Lock()
		gotA = append(gotA, pb.Topic)
		mu.Unlock()
	}})
	c.registerRoute(mqttRoute{subID: 2, filter: fB, dispatch: func(pb *paho.Publish) {
		mu.Lock()
		gotB = append(gotB, pb.Topic)
		mu.Unlock()
	}})

	// Delivery tagged with sub-id 1 → ONLY route A fires (no fanout to B).
	matched, hErr := c.onPublishReceived(paho.PublishReceived{Packet: pubWithSubID("ns/room1/temp", 1)})
	require.NoError(t, hErr)
	assert.True(t, matched)

	// Delivery tagged with sub-id 2 → ONLY route B fires.
	matched, hErr = c.onPublishReceived(paho.PublishReceived{Packet: pubWithSubID("ns/room2/temp", 2)})
	require.NoError(t, hErr)
	assert.True(t, matched)

	// Unknown sub-id (route deregistered) → dropped, matched=false.
	matched, hErr = c.onPublishReceived(paho.PublishReceived{Packet: pubWithSubID("ns/room1/temp", 99)})
	require.NoError(t, hErr)
	assert.False(t, matched, "unknown sub-id must not dispatch")

	// Missing sub-id → fail-closed drop (would risk cross-group misdelivery).
	matched, hErr = c.onPublishReceived(paho.PublishReceived{Packet: pubWithSubID("ns/room1/temp", 0)})
	require.NoError(t, hErr)
	assert.False(t, matched, "missing sub-id must fail-closed (no dispatch)")

	// Nil packet — defensive no-op.
	matched, hErr = c.onPublishReceived(paho.PublishReceived{Packet: nil})
	require.NoError(t, hErr)
	assert.False(t, matched)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"ns/room1/temp"}, gotA, "route A fired exactly once (sub-id 1 only)")
	assert.Equal(t, []string{"ns/room2/temp"}, gotB, "route B fired exactly once (sub-id 2 only)")
}

// fakeAcker records the publishes it was asked to ack and can be configured to
// return an error.
type fakeAcker struct {
	mu   sync.Mutex
	acks []*paho.Publish
	err  error
}

func (f *fakeAcker) Ack(pb *paho.Publish) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.acks = append(f.acks, pb)
	return nil
}

// TestConnection_Ack_RoutesThroughAckClient verifies that Connection.ack routes
// to the captured delivering client (mqttAcker), and surfaces failures.
func TestConnection_Ack_RoutesThroughAckClient(t *testing.T) {
	t.Parallel()

	pb := &paho.Publish{Topic: "ns/a", QoS: 1}

	t.Run("no-ack-client", func(t *testing.T) {
		t.Parallel()
		c := &Connection{}
		err := c.ack(pb)
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, ErrAdapterMQTTAck, ec.Code)
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		acker := &fakeAcker{}
		c := &Connection{ackClient: acker}
		require.NoError(t, c.ack(pb))
		acker.mu.Lock()
		require.Len(t, acker.acks, 1)
		acker.mu.Unlock()
	})

	t.Run("ack-error-wrapped", func(t *testing.T) {
		t.Parallel()
		acker := &fakeAcker{err: errors.New("broker gone")}
		c := &Connection{ackClient: acker}
		err := c.ack(pb)
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, ErrAdapterMQTTAck, ec.Code)
	})

	t.Run("closed-connection", func(t *testing.T) {
		t.Parallel()
		c := &Connection{ackClient: &fakeAcker{}}
		c.closed = true
		err := c.ack(pb)
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		assert.Equal(t, ErrAdapterMQTTClosed, ec.Code)
	})
}

// TestConnection_Subscribe_ClosedConnection verifies Subscribe is rejected on a
// closed connection before any broker interaction.
func TestConnection_Subscribe_ClosedConnection(t *testing.T) {
	t.Parallel()
	ns, err := ParseTopicNamespace("ns")
	require.NoError(t, err)
	f, err := ns.MintFilter("cg", "ns/a")
	require.NoError(t, err)

	c := &Connection{closed: true}
	cancel, subErr := c.Subscribe(context.Background(), f, 1, func(context.Context, *paho.Publish) {})
	require.Error(t, subErr)
	assert.Nil(t, cancel)
	var ec *errcode.Error
	require.True(t, errors.As(subErr, &ec))
	assert.Equal(t, ErrAdapterMQTTClosed, ec.Code)
}

// ---------------------------------------------------------------------------
// SUBACK error classification path — pure unit, no broker
// ---------------------------------------------------------------------------

func TestSubackError(t *testing.T) {
	t.Parallel()

	// All-success granted-QoS reasons → nil.
	require.NoError(t, subackError(&paho.Suback{Reasons: []byte{0x00, 0x01, 0x02}}))
	// nil suback → nil.
	require.NoError(t, subackError(nil))

	// First failing reason classified.
	cases := []struct {
		name     string
		reasons  []byte
		wantCode errcode.Code
	}{
		{"not-authorized", []byte{0x87}, ErrAdapterMQTTSubscribeNotAuthorized},
		{"shared-subs-unsupported", []byte{0x9E}, ErrAdapterMQTTSharedSubsUnsupported},
		{"quota-exceeded", []byte{0x97}, ErrAdapterMQTTSubscribeRateLimited},
		{"unspecified", []byte{0x80}, ErrAdapterMQTTSubscribe},
		{"first-failure-wins", []byte{0x01, 0x9E, 0x87}, ErrAdapterMQTTSharedSubsUnsupported},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := subackError(&paho.Suback{Reasons: tc.reasons})
			require.Error(t, err)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec))
			assert.Equal(t, tc.wantCode, ec.Code)
		})
	}
}

// TestSubackError_AuthCodeRedaction (F11) verifies the SUBACK error path applies
// the same auth-code reasonName redaction as CONNACK: for 0x87 (Not Authorized)
// the human-readable reasonName is moved to the Internal channel (server logs
// only) and only the numeric reasonCode stays in Public details; for non-auth
// codes the reasonName stays Public for operator diagnostics.
func TestSubackError_AuthCodeRedaction(t *testing.T) {
	t.Parallel()

	t.Run("auth-0x87-reasonName-internal-only", func(t *testing.T) {
		t.Parallel()
		err := subackError(&paho.Suback{Reasons: []byte{0x87}})
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		_, hasName := ec.FindAttr("reasonName")
		assert.False(t, hasName, "auth-related SUBACK reasonName must NOT be in Public details")
		_, hasCode := ec.FindAttr("reasonCode")
		assert.True(t, hasCode, "numeric reasonCode must remain in Public details")
		// reasonName must still be server-side observable via the Internal channel.
		assert.Contains(t, ec.Error(), "NotAuthorized",
			"reasonName must be carried in the Internal channel (server logs)")
	})

	t.Run("non-auth-0x9E-reasonName-public", func(t *testing.T) {
		t.Parallel()
		err := subackError(&paho.Suback{Reasons: []byte{0x9E}})
		require.Error(t, err)
		var ec *errcode.Error
		require.True(t, errors.As(err, &ec))
		_, hasName := ec.FindAttr("reasonName")
		assert.True(t, hasName, "non-auth SUBACK reasonName stays in Public details for diagnostics")
	})
}

// TestConnection_Health_SurfacesResubscribeError (F8) verifies that a
// resubscribe-after-reconnect failure makes Health report degraded even while
// the connection phase is phaseConnected — so readyz does not show green while
// the broker has no subscription for a route.
func TestConnection_Health_SurfacesResubscribeError(t *testing.T) {
	t.Parallel()
	c := &Connection{phase: phaseConnected}
	require.NoError(t, c.Health(context.Background()), "connected + no resubscribe error → healthy")

	wantErr := errcode.New(errcode.KindInternal, ErrAdapterMQTTSubscribe, "mqtt: resubscribe failed")
	c.lastResubscribeErr = wantErr
	got := c.Health(context.Background())
	require.Error(t, got, "resubscribe failure must surface as a degraded Health result")
	assert.Equal(t, wantErr, got)
}

// ---------------------------------------------------------------------------
// End-to-end against the in-process mochi broker (shared subscription)
// ---------------------------------------------------------------------------

// TestConnection_Subscribe_ReceivesPublishedMessage verifies the full receive
// path: Subscribe registers a shared-subscription route, a QoS-1 publish on the
// matching topic is delivered to the route handler, and Connection.ack clears
// the broker-side inflight (no redelivery).
func TestConnection_Subscribe_ReceivesPublishedMessage(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)

	ctx, cancelCtx := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancelCtx()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)
	f, err := ns.MintFilter("recv-cg", "test/recv/+")
	require.NoError(t, err)

	var mu sync.Mutex
	var received [][]byte
	var ackErr error
	cancel, subErr := conn.Subscribe(ctx, f, 1, func(_ context.Context, pb *paho.Publish) {
		mu.Lock()
		received = append(received, append([]byte(nil), pb.Payload...))
		mu.Unlock()
		// Manually ack so the broker clears its inflight (EnableManualAcknowledgment).
		if e := conn.ack(pb); e != nil {
			mu.Lock()
			ackErr = e
			mu.Unlock()
		}
	})
	require.NoError(t, subErr)
	require.NotNil(t, cancel)
	defer cancel()

	// Publish a QoS-1 message on the matching topic.
	pt, err := ns.Mint("test/recv/hello")
	require.NoError(t, err)
	_, err = conn.Publish(ctx, pt, []byte("hello-recv"), publishOpts{QoS: 1})
	require.NoError(t, err)

	testwait.External(t, "message-received", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 1
	}, testtime.D5s, testtime.D10ms)

	mu.Lock()
	require.GreaterOrEqual(t, len(received), 1)
	assert.Equal(t, []byte("hello-recv"), received[0])
	require.NoError(t, ackErr, "manual ack should succeed")
	mu.Unlock()
}

// TestConnection_Subscribe_CancelUnsubscribes verifies that the cancel closure
// deregisters the route so subsequent publishes are not delivered.
func TestConnection_Subscribe_CancelUnsubscribes(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)

	ctx, cancelCtx := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancelCtx()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)
	f, err := ns.MintFilter("cancel-cg", "test/cancel/+")
	require.NoError(t, err)

	var mu sync.Mutex
	count := 0
	cancel, subErr := conn.Subscribe(ctx, f, 1, func(_ context.Context, pb *paho.Publish) {
		mu.Lock()
		count++
		mu.Unlock()
		_ = conn.ack(pb)
	})
	require.NoError(t, subErr)

	// Cancel immediately; the route is deregistered. Even if the broker still
	// delivers a stray message, the route registry no longer dispatches it.
	cancel()

	// Confirm the in-memory route registry is empty (deterministic assertion;
	// broker-side unsubscribe timing is not relied upon).
	conn.subMu.RLock()
	require.Empty(t, conn.routes)
	conn.subMu.RUnlock()

	mu.Lock()
	assert.Equal(t, 0, count)
	mu.Unlock()
}

// TestConnection_Subscribe_ResubscribeOnReconnect verifies that onConnectionUp
// re-arms registered routes (session recovery). It exercises resubscribeAll
// directly by simulating a reconnect via onConnectionUp after a subscription is
// registered, then publishes and asserts delivery still works.
func TestConnection_Subscribe_ResubscribeOnReconnect(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)

	ctx, cancelCtx := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancelCtx()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)
	f, err := ns.MintFilter("resub-cg", "test/resub/+")
	require.NoError(t, err)

	var mu sync.Mutex
	var received [][]byte
	cancel, subErr := conn.Subscribe(ctx, f, 1, func(_ context.Context, pb *paho.Publish) {
		mu.Lock()
		received = append(received, append([]byte(nil), pb.Payload...))
		mu.Unlock()
		_ = conn.ack(pb)
	})
	require.NoError(t, subErr)
	defer cancel()

	// Pre-seed a stale resubscribe error to prove a successful resubscribe clears
	// it (F8: Health must not stay degraded after recovery).
	conn.mu.Lock()
	conn.lastResubscribeErr = errors.New("stale-precondition")
	conn.mu.Unlock()

	// Simulate a reconnect: onConnectionUp re-arms every route via resubscribeAll.
	// This is the same path autopaho invokes on a real reconnect. It must not
	// panic / deadlock and must leave the route subscribed.
	conn.onConnectionUp(nil, nil)

	// The route is still registered after resubscribe.
	conn.subMu.RLock()
	require.Len(t, conn.routes, 1)
	conn.subMu.RUnlock()

	// A fully-successful resubscribe pass clears lastResubscribeErr → Health nil.
	require.NoError(t, conn.Health(ctx), "successful resubscribe must clear the degraded Health signal")

	// Delivery still works after the resubscribe.
	pt, err := ns.Mint("test/resub/x")
	require.NoError(t, err)
	_, err = conn.Publish(ctx, pt, []byte("after-reconnect"), publishOpts{QoS: 1})
	require.NoError(t, err)

	testwait.External(t, "message-after-resubscribe", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) >= 1
	}, testtime.D5s, testtime.D10ms)

	mu.Lock()
	require.GreaterOrEqual(t, len(received), 1)
	mu.Unlock()
}
