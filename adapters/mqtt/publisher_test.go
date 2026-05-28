package mqtt

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
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

	// Fire a publish in the background.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = pub.Publish(ctx, "test/drain", make([]byte, 5))
	}()

	// Allow the goroutine to start before closing.
	time.Sleep(testtime.D10ms)

	// Close should drain the in-flight publish before returning.
	closeErr := pub.Close(ctx)
	assert.NoError(t, closeErr, "Close should succeed after draining in-flight publishes")

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Concurrent safety
// ---------------------------------------------------------------------------

func TestPublisher_Publish_ConcurrentSafe(t *testing.T) {
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
	tests := []struct {
		code   errcode.Code
		reason PublishFailureReason
	}{
		{ErrAdapterMQTTPublishRateLimited, PublishFailureRateLimited},
		{ErrAdapterMQTTPublishRejected, PublishFailurePublishError},
		{ErrAdapterMQTTPayloadTooLarge, PublishFailurePayloadTooLarge},
		{ErrAdapterMQTTPublishNoSubscribers, PublishFailurePublishError},
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
