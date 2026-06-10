package mqtt

import (
	"context"
	"errors"
	"fmt"
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
	cfg := newInternalConfig(t, addr)
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
	cfg := newInternalConfig(t, addr)
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
	cfg := newInternalConfig(t, addr)
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
	cfg := newInternalConfig(t, addr, WithMaximumPacketSize(10))
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
	cfg := newInternalConfig(t, addr)
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
	cfg := newInternalConfig(t, addr)
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
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTPublishCanceled, ec.Code,
		"canceled ctx must wrap ErrAdapterMQTTPublishCanceled")

	spy.mu.Lock()
	defer spy.mu.Unlock()
	assert.Equal(t, 1, spy.failureCount, "RecordPublishFailure should be called on context cancel")
	assert.Equal(t, PublishFailureContextCanceled, spy.lastFailureReason,
		"canceled ctx must record the context_canceled metric reason")
}

// ---------------------------------------------------------------------------
// Close tests
// ---------------------------------------------------------------------------

func TestPublisher_Close_Idempotent(t *testing.T) {
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
	cfg := newInternalConfig(t, addr)
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
	cfg := newInternalConfig(t, addr)
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
	// NOTE: this test cannot deterministically pin the publish to the "mid-flight
	// at conn.Publish" window (scheduler-dependent). The deterministic mid-flight
	// case — where the broker withholds PUBACK so the WaitGroup is provably held
	// across Close — is covered by TestPublisher_Close_TimesOutOnStuckPublish.
}

// TestPublisher_Close_TimesOutOnStuckPublish deterministically exercises the
// Close drain-timeout path (publisher.go select on ctx.Done): a broker hook
// withholds the QoS-1 PUBACK so a publish is provably in-flight (WaitGroup
// held), then Close is invoked with an already-canceled ctx. Close must return
// ErrAdapterMQTTPublisherCloseTimeout, and — crucially — the in-flight publish
// goroutine must NOT be killed: releasing the hook lets it complete, proving
// Close-timeout is a bounded wait, not a cancellation.
func TestPublisher_Close_TimesOutOnStuckPublish(t *testing.T) {
	t.Parallel()
	hook := &blockingPubAckHook{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	addr, stop := startBrokerWithHook(t, hook)
	defer stop()

	clk := clock.Real()
	// publishTimeout defaults to 0 (no adapter timeout — publish blocks on the
	// withheld PUBACK); newInternalConfig supplies no WithPublishTimeout.
	cfg := newInternalConfig(t, addr)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D15s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)
	pub, err := NewPublisher(clk, conn, ns)
	require.NoError(t, err)

	// Launch a publish the broker will not PUBACK until we release the hook.
	pubDone := make(chan error, 1)
	go func() { pubDone <- pub.Publish(ctx, "test/stuck", []byte("x")) }()

	// Barrier: the broker received the PUBLISH (PUBACK now withheld), so the
	// publisher's WaitGroup is held and Close must drain-wait.
	select {
	case <-hook.entered:
	case <-ctx.Done():
		t.Fatal("publish never reached broker hook")
	}

	// Close with an already-canceled ctx: drain cannot complete → timeout.
	canceledCtx, cancelFn := context.WithCancel(context.Background())
	cancelFn()
	closeErr := pub.Close(canceledCtx)
	require.Error(t, closeErr)
	var ec *errcode.Error
	require.True(t, errors.As(closeErr, &ec))
	assert.Equal(t, ErrAdapterMQTTPublisherCloseTimeout, ec.Code,
		"Close with canceled ctx and an in-flight publish must time out")

	// Release the broker: the in-flight goroutine must still complete (Close
	// timeout did not cancel it).
	close(hook.release)
	select {
	case perr := <-pubDone:
		assert.NoError(t, perr, "released in-flight publish should complete successfully")
	case <-ctx.Done():
		t.Fatal("in-flight publish goroutine did not complete after broker release")
	}
}

// ---------------------------------------------------------------------------
// classifyPublishErr / wrapPublishErr — pure-function table-driven coverage
// (no broker; exercises every branch including the generic-error path that the
// broker-backed tests do not reach).
// ---------------------------------------------------------------------------

func TestClassifyPublishErr(t *testing.T) {
	t.Parallel()

	// deadline-exceeded publishCtx: the first branch keys off publishCtx.Err().
	deadlineCtx, dcancel := context.WithTimeout(context.Background(), 0)
	defer dcancel()
	<-deadlineCtx.Done() // ensure Err() == DeadlineExceeded

	canceledCtx, ccancel := context.WithCancel(context.Background())
	ccancel()
	<-canceledCtx.Done()

	bg := context.Background()
	wrapCanceled := fmt.Errorf("wrap: %w", context.Canceled)
	wrapDeadline := fmt.Errorf("wrap: %w", context.DeadlineExceeded)

	// Cases distinguish the caller's own ctx from the adapter PublishTimeout child:
	// caller-driven abort → context_canceled; only-adapter-timeout → puback_timeout.
	tests := []struct {
		name       string
		callerCtx  context.Context
		publishCtx context.Context
		err        error
		want       PublishFailureReason
	}{
		{"adapter-timeout-only", bg, deadlineCtx, errors.New("transport"), PublishFailurePubAckTimeout},
		{"caller-canceled", canceledCtx, canceledCtx, wrapCanceled, PublishFailureContextCanceled},
		{"caller-deadline", deadlineCtx, deadlineCtx, wrapDeadline, PublishFailureContextCanceled},
		{"generic-transport-err", bg, bg, errors.New("connection reset"), PublishFailurePublishError},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := classifyPublishErr(tc.callerCtx, tc.publishCtx, tc.err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestWrapPublishErr(t *testing.T) {
	t.Parallel()

	deadlineCtx, dcancel := context.WithTimeout(context.Background(), 0)
	defer dcancel()
	<-deadlineCtx.Done()
	canceledCtx, ccancel := context.WithCancel(context.Background())
	ccancel()
	<-canceledCtx.Done()
	bg := context.Background()

	tests := []struct {
		name       string
		callerCtx  context.Context
		publishCtx context.Context
		err        error
		wantCode   errcode.Code
	}{
		{"caller-canceled", canceledCtx, canceledCtx, fmt.Errorf("wrap: %w", context.Canceled), ErrAdapterMQTTPublishCanceled},
		{"adapter-timeout-only", bg, deadlineCtx, errors.New("transport"), ErrAdapterMQTTPubAckTimeout},
		{"generic", bg, bg, errors.New("connection reset by peer"), ErrAdapterMQTTPublishFailed},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := wrapPublishErr(tc.callerCtx, tc.publishCtx, tc.err)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec))
			assert.Equal(t, tc.wantCode, ec.Code)
			assert.Equal(t, errcode.KindUnavailable, ec.Kind)
			assert.ErrorIs(t, err, tc.err, "wrapPublishErr must preserve the cause chain")
		})
	}
}

func TestWrapPublishErr_GenericTransportRedactsCause(t *testing.T) {
	t.Parallel()

	raw := errors.New("mqtt write failed: password=hunter2 token=abc123 host=broker")
	err := wrapPublishErr(context.Background(), context.Background(), raw)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, ErrAdapterMQTTPublishFailed, ec.Code)
	assert.NotContains(t, err.Error(), "hunter2")
	assert.NotContains(t, err.Error(), "abc123")
	assert.Contains(t, err.Error(), "<REDACTED>")
	assert.False(t, errors.Is(err, raw),
		"redacted generic transport errors intentionally break the raw cause chain")
}

// ---------------------------------------------------------------------------
// Concurrent safety
// ---------------------------------------------------------------------------

func TestPublisher_Publish_ConcurrentSafe(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(t, addr)
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
		{ErrAdapterMQTTPublishNotAuthorized, PublishFailureNotAuthorized},
		{ErrAdapterMQTTPublishPayloadFormatInvalid, PublishFailurePayloadFormatInvalid},
		{ErrAdapterMQTTPublishRejected, PublishFailureRejected},
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

// blockingPubAckHook withholds the QoS-1 PUBACK: mochi invokes OnPublish
// synchronously before writing the PUBACK (server.go processPublish), so
// blocking here provably holds a publish in-flight on the client side. `entered`
// is signaled once when the first QoS>0 PUBLISH arrives; OnPublish then blocks
// until `release` is closed. Used by TestPublisher_Close_TimesOutOnStuckPublish.
type blockingPubAckHook struct {
	mqttserver.HookBase
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *blockingPubAckHook) ID() string           { return "blocking-puback" }
func (h *blockingPubAckHook) Provides(b byte) bool { return b == mqttserver.OnPublish }

func (h *blockingPubAckHook) OnPublish(_ *mqttserver.Client, pk mqttpackets.Packet) (mqttpackets.Packet, error) {
	if pk.FixedHeader.Qos > 0 {
		h.once.Do(func() { close(h.entered) })
		<-h.release
	}
	return pk, nil
}

// startBrokerWithHook starts a dedicated in-process mochi broker with the given
// hook (plus AllowHook), on a fresh random port. Mirrors
// startBrokerWithNoSubscribersHook but parameterized over the hook so callers
// can inject custom PUBACK behavior.
func startBrokerWithHook(t *testing.T, h mqttserver.Hook) (addr string, stop func()) {
	t.Helper()
	srv := mqttserver.New(&mqttserver.Options{InlineClient: false})
	require.NoError(t, srv.AddHook(new(auth.AllowHook), nil), "add allow hook")
	require.NoError(t, srv.AddHook(h, nil), "add custom hook")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "listen random port")
	addr = ln.Addr().String()
	require.NoError(t, ln.Close())

	tcp := listeners.NewTCP(listeners.Config{ID: "hook-tcp", Address: addr})
	require.NoError(t, srv.AddListener(tcp), "add listener")
	go func() { _ = srv.Serve() }()

	testwait.External(t, "hook-broker-ready", func() bool {
		c, derr := net.Dial("tcp", addr)
		if derr != nil {
			return false
		}
		_ = c.Close()
		return true
	}, testtime.D2s, testtime.D10ms)

	return addr, func() { CloseBrokerSafely(t, srv) }
}

// rejectingPubAckHook returns a broker-rejection PUBACK reason code (>= 0x80)
// for every QoS-1 PUBLISH. mochi builds a PUBACK carrying that code, which paho
// surfaces via the ERROR return of Publish (paho/client.go:923) — NOT via a
// nil-error response. This is the real path on which broker reason codes reach
// the adapter; the test below uses it to prove Publisher.Publish routes those
// errors through classifyPubackReason rather than collapsing them to a generic
// publish failure.
type rejectingPubAckHook struct {
	mqttserver.HookBase
	code byte
}

func (h *rejectingPubAckHook) ID() string           { return "rejecting-puback" }
func (h *rejectingPubAckHook) Provides(b byte) bool { return b == mqttserver.OnPublish }

func (h *rejectingPubAckHook) OnPublish(_ *mqttserver.Client, pk mqttpackets.Packet) (mqttpackets.Packet, error) {
	if pk.FixedHeader.Qos > 0 {
		return pk, mqttpackets.Code{Code: h.code, Reason: "rejected by test hook"}
	}
	return pk, nil
}

// TestPublisher_Publish_BrokerRejectedPUBACK_RealPath is the recurrence guard for
// the dead-classifyPubackReason bug: paho returns reason codes >= 0x80 via the
// error channel, so a naive `if err != nil { wrapPublishErr }` would collapse
// every broker rejection into ErrAdapterMQTTPublishFailed and never reach
// classifyPubackReason. This test drives a real broker that returns each
// rejection reason and asserts the DEDICATED errcode + metric reason surface —
// exercising the integrated paho path, not classifyPubackReason in isolation.
func TestPublisher_Publish_BrokerRejectedPUBACK_RealPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		code       byte
		wantCode   errcode.Code
		wantReason PublishFailureReason
	}{
		{"not-authorized-0x87", 0x87, ErrAdapterMQTTPublishNotAuthorized, PublishFailureNotAuthorized},
		{"quota-exceeded-0x97", 0x97, ErrAdapterMQTTPublishRateLimited, PublishFailureRateLimited},
		{"payload-format-invalid-0x99", 0x99, ErrAdapterMQTTPublishPayloadFormatInvalid, PublishFailurePayloadFormatInvalid},
		{"unspecified-0x80", 0x80, ErrAdapterMQTTPublishRejected, PublishFailureRejected},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			addr, stop := startBrokerWithHook(t, &rejectingPubAckHook{code: tc.code})
			defer stop()

			clk := clock.Real()
			cfg := newInternalConfig(t, addr)
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

			err = pub.Publish(ctx, "test/rejected", []byte("payload"))
			require.Error(t, err)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec))
			assert.Equal(t, tc.wantCode, ec.Code,
				"broker PUBACK 0x%02x must surface its dedicated errcode via classifyPubackReason, not generic", tc.code)

			spy.mu.Lock()
			defer spy.mu.Unlock()
			assert.Equal(t, 1, spy.failureCount)
			assert.Equal(t, tc.wantReason, spy.lastFailureReason)
			assert.Equal(t, 0, spy.successCount)
		})
	}
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

	return addr, func() { CloseBrokerSafely(t, srv) }
}

// TestPublisher_Publish_NoMatchingSubscribers_Success verifies that PUBACK 0x10
// (NoMatchingSubscribers) is treated as success: Publisher.Publish returns nil,
// RecordPublishSuccess is called once, and RecordPublishFailure is not called.
func TestPublisher_Publish_NoMatchingSubscribers_Success(t *testing.T) {
	t.Parallel()
	addr, stop := startBrokerWithNoSubscribersHook(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(t, addr)
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
	cfg := newInternalConfig(t, addr)
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
	// publishTimeout defaults to 0: no adapter-imposed timeout; caller ctx governs.
	// newInternalConfig supplies no WithPublishTimeout.
	cfg := newInternalConfig(t, addr)

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
