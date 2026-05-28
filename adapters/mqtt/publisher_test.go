package mqtt

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	mqttserver "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	mqttpackets "github.com/mochi-mqtt/server/v2/packets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// ---------------------------------------------------------------------------
// TestPublisher_ImplementsOutboxPublisher — compile-time interface assertion
// ---------------------------------------------------------------------------

// The var-level assertion in publisher.go already covers this; this test
// provides explicit runtime documentation.
func TestPublisher_ImplementsOutboxPublisher(t *testing.T) {
	t.Parallel()
	var _ outbox.Publisher = (*Publisher)(nil)
}

// ---------------------------------------------------------------------------
// NewPublisher construction tests
// ---------------------------------------------------------------------------

func TestNewPublisher_NilConnection(t *testing.T) {
	t.Parallel()
	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)

	_, err = NewPublisher(clock.Real(), nil, ns)
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidConfig, ec.Code)
}

func TestNewPublisher_ZeroNamespace(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	_, err = NewPublisher(clock.Real(), conn, TopicNamespace{})
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTInvalidTopicNamespace, ec.Code)
}

// ---------------------------------------------------------------------------
// Publish tests
// ---------------------------------------------------------------------------

func TestPublisher_Publish_Success(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)

	spy := &spyCollector{}
	pub, err := NewPublisher(clk, conn, ns, WithPublisherCollector(spy))
	require.NoError(t, err)
	defer pub.Close(context.Background()) //nolint:errcheck // test cleanup

	err = pub.Publish(ctx, "test/hello", []byte("payload"))
	require.NoError(t, err)

	spy.mu.Lock()
	defer spy.mu.Unlock()
	assert.Equal(t, 1, spy.successCount, "RecordPublishSuccess should be called once")
	assert.Equal(t, 0, spy.failureCount, "RecordPublishFailure should not be called on success")
}

func TestPublisher_Publish_TopicOutsideNamespace(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("ns1")
	require.NoError(t, err)

	spy := &spyCollector{}
	pub, err := NewPublisher(clk, conn, ns, WithPublisherCollector(spy))
	require.NoError(t, err)
	defer pub.Close(context.Background()) //nolint:errcheck // test cleanup

	err = pub.Publish(ctx, "ns2/x", []byte("payload"))
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTTopicOutsideNamespace, ec.Code)

	spy.mu.Lock()
	defer spy.mu.Unlock()
	assert.Equal(t, 0, spy.successCount)
	assert.Equal(t, 1, spy.failureCount)
	assert.Equal(t, PublishFailureTopicOutsideNamespace, spy.lastFailureReason)
}

func TestPublisher_Publish_PayloadTooLarge(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)
	cfg.MaximumPacketSize = 10
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)

	spy := &spyCollector{}
	pub, err := NewPublisher(clk, conn, ns, WithPublisherCollector(spy))
	require.NoError(t, err)
	defer pub.Close(context.Background()) //nolint:errcheck // test cleanup

	// 20-byte payload exceeds MaximumPacketSize=10.
	err = pub.Publish(ctx, "test/large", make([]byte, 20))
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTPayloadTooLarge, ec.Code)

	spy.mu.Lock()
	defer spy.mu.Unlock()
	assert.Equal(t, 1, spy.failureCount)
	assert.Equal(t, PublishFailurePayloadTooLarge, spy.lastFailureReason)
}

func TestPublisher_Publish_AfterClose(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)

	spy := &spyCollector{}
	pub, err := NewPublisher(clk, conn, ns, WithPublisherCollector(spy))
	require.NoError(t, err)

	// Close publisher first.
	require.NoError(t, pub.Close(ctx))

	err = pub.Publish(ctx, "test/x", []byte("payload"))
	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTClosed, ec.Code)

	spy.mu.Lock()
	defer spy.mu.Unlock()
	assert.Equal(t, 1, spy.failureCount)
	assert.Equal(t, PublishFailureClosed, spy.lastFailureReason)
}

func TestPublisher_Publish_ContextCanceled(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)

	spy := &spyCollector{}
	pub, err := NewPublisher(clk, conn, ns, WithPublisherCollector(spy))
	require.NoError(t, err)
	defer pub.Close(context.Background()) //nolint:errcheck // test cleanup

	canceledCtx, cancelFn := context.WithCancel(context.Background())
	cancelFn()

	err = pub.Publish(canceledCtx, "test/cancel", []byte("payload"))
	require.Error(t, err)

	spy.mu.Lock()
	defer spy.mu.Unlock()
	assert.Equal(t, 1, spy.failureCount, "RecordPublishFailure should be called on context cancel")
}

// ---------------------------------------------------------------------------
// Close tests
// ---------------------------------------------------------------------------

func TestPublisher_Close_Idempotent(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)

	pub, err := NewPublisher(clk, conn, ns)
	require.NoError(t, err)

	assert.NoError(t, pub.Close(ctx), "first Close should succeed")
	assert.NoError(t, pub.Close(ctx), "second Close should be idempotent")
}

func TestPublisher_Close_DoesNotCloseConnection(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)

	pub, err := NewPublisher(clk, conn, ns)
	require.NoError(t, err)

	require.NoError(t, pub.Close(ctx))

	// Connection should still be healthy after publisher is closed.
	assert.NoError(t, conn.Health(ctx), "conn.Health should still be nil after publisher Close")
}

func TestPublisher_Close_DrainsInFlight(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D15s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)

	pub, err := NewPublisher(clk, conn, ns)
	require.NoError(t, err)

	// Fire a publish in the background with a channel barrier to signal when
	// the goroutine is about to enter Publish. The channel barrier guarantees
	// the goroutine has started; any in-flight work will have entered Publish
	// (or will have exited via the closed-path check) by the time Close runs.
	// Both outcomes are valid drain semantics.
	started := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(started) // signal "about to enter Publish"
		_ = pub.Publish(ctx, "test/drain", make([]byte, 5))
	}()
	<-started

	// Close should drain any in-flight publish before returning.
	closeErr := pub.Close(ctx)
	assert.NoError(t, closeErr, "Close should succeed after draining in-flight publishes")

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Concurrent safety
// ---------------------------------------------------------------------------

func TestPublisher_Publish_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D15s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)

	pub, err := NewPublisher(clk, conn, ns)
	require.NoError(t, err)
	defer pub.Close(context.Background()) //nolint:errcheck // test cleanup

	const n = 20
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = pub.Publish(ctx, "test/concurrent", []byte("payload"))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "goroutine %d publish error", i)
	}
}

// ---------------------------------------------------------------------------
// pubackReasonToMetric table test
// ---------------------------------------------------------------------------

func TestPubackReasonToMetric(t *testing.T) {
	t.Parallel()
	// ErrAdapterMQTTPublishNoSubscribers (0x10) is intentionally excluded:
	// the Publish path guards `code != ErrAdapterMQTTPublishNoSubscribers` before
	// calling pubackReasonToMetric, so that code never reaches this mapping.
	// The default branch already covers any future drift. See also
	// pubackReasonToMetric godoc "Caller MUST pre-filter" note.
	tests := []struct {
		code   errcode.Code
		reason PublishFailureReason
	}{
		{ErrAdapterMQTTPublishRateLimited, PublishFailureRateLimited},
		{ErrAdapterMQTTPublishRejected, PublishFailurePublishError},
		{ErrAdapterMQTTPayloadTooLarge, PublishFailurePayloadTooLarge},
		{"UNKNOWN_CODE", PublishFailurePublishError},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(string(tc.code), func(t *testing.T) {
			t.Parallel()
			got := pubackReasonToMetric(tc.code)
			assert.Equal(t, tc.reason, got)
		})
	}
}

// ---------------------------------------------------------------------------
// 7c: 0x10 NoMatchingSubscribers success path
// ---------------------------------------------------------------------------

// noMatchingSubscribersHook is a mochi hook that returns
// packets.CodeNoMatchingSubscribers for every QoS 1 PUBLISH, causing the
// broker to send PUBACK with ReasonCode 0x10.
type noMatchingSubscribersHook struct {
	mqttserver.HookBase
}

func (h *noMatchingSubscribersHook) ID() string { return "no-matching-subscribers" }
func (h *noMatchingSubscribersHook) Provides(b byte) bool {
	return b == mqttserver.OnPublish
}

func (h *noMatchingSubscribersHook) OnPublish(_ *mqttserver.Client, pk mqttpackets.Packet) (mqttpackets.Packet, error) {
	if pk.FixedHeader.Qos > 0 {
		// Return CodeNoMatchingSubscribers as the error; mochi will build a
		// PUBACK with ReasonCode 0x10 for MQTT v5 QoS 1 clients.
		return pk, mqttpackets.CodeNoMatchingSubscribers
	}
	return pk, nil
}

// startBrokerWithNoSubscribersHook starts a broker that always returns PUBACK
// 0x10 for QoS 1 publishes, simulating a topic with no active subscribers.
func startBrokerWithNoSubscribersHook(t *testing.T) (addr string, stop func()) {
	t.Helper()
	srv := mqttserver.New(&mqttserver.Options{InlineClient: false})
	require.NoError(t, srv.AddHook(new(auth.AllowHook), nil), "add allow hook")
	require.NoError(t, srv.AddHook(new(noMatchingSubscribersHook), nil), "add no-subscribers hook")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "listen random port")
	addr = ln.Addr().String()
	require.NoError(t, ln.Close())

	tcp := listeners.NewTCP(listeners.Config{ID: "no-sub-tcp", Address: addr})
	require.NoError(t, srv.AddListener(tcp), "add listener")
	go func() { _ = srv.Serve() }()

	testwait.External(t, "no-sub-broker-ready", func() bool {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, testtime.D2s, testtime.D10ms)

	return addr, func() { _ = srv.Close() }
}

// TestPublisher_Publish_NoMatchingSubscribers_Success verifies that PUBACK 0x10
// (NoMatchingSubscribers) is treated as success: Publisher.Publish returns nil,
// RecordPublishSuccess is called once, and RecordPublishFailure is not called.
func TestPublisher_Publish_NoMatchingSubscribers_Success(t *testing.T) {
	t.Parallel()
	addr, stop := startBrokerWithNoSubscribersHook(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)

	spy := &spyCollector{}
	pub, err := NewPublisher(clk, conn, ns, WithPublisherCollector(spy))
	require.NoError(t, err)
	defer pub.Close(context.Background()) //nolint:errcheck // test cleanup

	err = pub.Publish(ctx, "test/nosub", []byte("payload"))
	require.NoError(t, err, "PUBACK 0x10 must be treated as success (no error returned)")

	spy.mu.Lock()
	defer spy.mu.Unlock()
	assert.Equal(t, 1, spy.successCount, "RecordPublishSuccess must be called once for PUBACK 0x10")
	assert.Equal(t, 0, spy.failureCount, "RecordPublishFailure must NOT be called for PUBACK 0x10")
}

// ---------------------------------------------------------------------------
// 7d: nil payload + PublishTimeout=0 tests
// ---------------------------------------------------------------------------

// TestPublisher_Publish_NilPayload verifies that nil payload is accepted:
// len(nil) == 0, so the MaximumPacketSize guard passes and the broker accepts
// an empty payload.
func TestPublisher_Publish_NilPayload(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)

	pub, err := NewPublisher(clk, conn, ns)
	require.NoError(t, err)
	defer pub.Close(context.Background()) //nolint:errcheck // test cleanup

	// nil payload: len(nil) == 0, must not trigger payload-too-large guard.
	err = pub.Publish(ctx, "test/nil-payload", nil)
	require.NoError(t, err, "nil payload must be accepted (len(nil)==0)")
}

// TestPublisher_Publish_NoAdapterTimeout verifies that when Config.PublishTimeout
// is 0, the publisher does NOT derive a child ctx and the caller-provided ctx
// deadline is honored as-is. The test publishes with a generous ctx deadline and
// confirms the call succeeds (demonstrating no internal timeout was imposed).
func TestPublisher_Publish_NoAdapterTimeout(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)
	// PublishTimeout = 0: no adapter-imposed timeout; caller ctx governs.
	cfg.PublishTimeout = 0

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)

	pub, err := NewPublisher(clk, conn, ns)
	require.NoError(t, err)
	defer pub.Close(context.Background()) //nolint:errcheck // test cleanup

	// Publish with a caller-provided ctx deadline (not overridden by publisher).
	pubCtx, pubCancel := context.WithTimeout(ctx, testtime.D5s)
	defer pubCancel()

	err = pub.Publish(pubCtx, "test/no-timeout", []byte("payload"))
	require.NoError(t, err, "publish with PublishTimeout=0 must honor caller ctx and succeed")
}

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// spyCollector is a PublisherCollector that records calls for assertions.
type spyCollector struct {
	mu                sync.Mutex
	successCount      int
	failureCount      int
	lastFailureReason PublishFailureReason
	lastAckDuration   time.Duration
}

func (s *spyCollector) RecordPublishSuccess(_ context.Context, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.successCount++
	s.lastAckDuration = d
}

func (s *spyCollector) RecordPublishFailure(_ context.Context, r PublishFailureReason) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failureCount++
	s.lastFailureReason = r
}
