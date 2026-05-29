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
// topicFilterMatches table (pure unit, no broker)
// ---------------------------------------------------------------------------

func TestTopicFilterMatches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		filter string
		topic  string
		want   bool
	}{
		// exact
		{"exact-single-level", "ns", "ns", true},
		{"exact-multi-level", "ns/a/b", "ns/a/b", true},
		{"exact-mismatch", "ns/a/b", "ns/a/c", false},
		{"exact-extra-topic-level", "ns/a", "ns/a/b", false},
		{"exact-extra-filter-level", "ns/a/b", "ns/a", false},
		// "+" single-level wildcard
		{"plus-middle", "ns/+/b", "ns/x/b", true},
		{"plus-head", "+/a/b", "ns/a/b", true},
		{"plus-tail", "ns/a/+", "ns/a/x", true},
		{"plus-must-be-single-level", "ns/+", "ns/a/b", false},
		{"plus-no-level-to-match", "ns/+/b", "ns", false},
		{"plus-empty-level-matches", "ns/+/b", "ns//b", true},
		// "#" multi-level wildcard
		{"hash-tail-matches-deeper", "ns/#", "ns/a/b/c", true},
		{"hash-tail-matches-one", "ns/#", "ns/a", true},
		{"hash-matches-parent-level", "ns/#", "ns", true},
		{"hash-root", "#", "anything/at/all", true},
		{"hash-after-plus", "ns/+/#", "ns/a/b/c", true},
		{"hash-after-plus-needs-plus-level", "ns/+/#", "ns", false},
		// non-match across namespaces
		{"different-namespace", "ns/#", "other/a", false},
		{"different-first-level", "ns/a", "other/a", false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := topicFilterMatches(tc.filter, tc.topic)
			if got != tc.want {
				t.Errorf("topicFilterMatches(%q, %q) = %v, want %v", tc.filter, tc.topic, got, tc.want)
			}
		})
	}
}

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
	c.deregisterRoute(f1.wireFilter)
	c.subMu.RLock()
	require.Len(t, c.routes, 1)
	assert.Equal(t, f2.wireFilter, c.routes[0].filter.wireFilter)
	c.subMu.RUnlock()

	// Deregistering an unknown filter is a no-op.
	c.deregisterRoute("$share/cg/ns/zzz")
	c.subMu.RLock()
	require.Len(t, c.routes, 1)
	c.subMu.RUnlock()

	// Deregister the last one.
	c.deregisterRoute(f2.wireFilter)
	c.subMu.RLock()
	require.Empty(t, c.routes)
	c.subMu.RUnlock()
}

// ---------------------------------------------------------------------------
// onPublishReceived dispatch — pure unit, no broker
// ---------------------------------------------------------------------------

func TestConnection_OnPublishReceived_Dispatch(t *testing.T) {
	t.Parallel()
	ns, err := ParseTopicNamespace("ns")
	require.NoError(t, err)
	f, err := ns.MintFilter("cg", "ns/+/temp")
	require.NoError(t, err)

	var mu sync.Mutex
	var got []string
	dispatch := func(pb *paho.Publish) {
		mu.Lock()
		got = append(got, pb.Topic)
		mu.Unlock()
	}

	c := &Connection{}
	c.registerRoute(mqttRoute{filter: f, dispatch: dispatch})

	// Matching topic — handler invoked, returns matched=true.
	matched, hErr := c.onPublishReceived(paho.PublishReceived{
		Packet: &paho.Publish{Topic: "ns/room1/temp", Payload: []byte("x")},
	})
	require.NoError(t, hErr)
	assert.True(t, matched, "expected matched=true for matching topic")

	// Non-matching topic — no handler call, matched=false.
	matched, hErr = c.onPublishReceived(paho.PublishReceived{
		Packet: &paho.Publish{Topic: "ns/room1/humidity", Payload: []byte("y")},
	})
	require.NoError(t, hErr)
	assert.False(t, matched, "expected matched=false for non-matching topic")

	// Nil packet — defensive no-op.
	matched, hErr = c.onPublishReceived(paho.PublishReceived{Packet: nil})
	require.NoError(t, hErr)
	assert.False(t, matched)

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{"ns/room1/temp"}, got)
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
		assert.Equal(t, ErrAdapterMQTTSubscribe, ec.Code)
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
		assert.Equal(t, ErrAdapterMQTTSubscribe, ec.Code)
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

	// Simulate a reconnect: onConnectionUp re-arms every route via resubscribeAll.
	// This is the same path autopaho invokes on a real reconnect. It must not
	// panic / deadlock and must leave the route subscribed.
	conn.onConnectionUp(nil, nil)

	// The route is still registered after resubscribe.
	conn.subMu.RLock()
	require.Len(t, conn.routes, 1)
	conn.subMu.RUnlock()

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
