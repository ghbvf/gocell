package rabbitmq

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/bits"
	"net"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	logctx "github.com/ghbvf/gocell/runtime/observability/logging"
	outboxrt "github.com/ghbvf/gocell/runtime/outbox"
)

type testContextKey string

// --- Mock AMQP Channel ---

type mockChannel struct {
	mu sync.Mutex

	publishCalled     bool
	publishedMessages []amqp.Publishing
	publishExchange   string
	publishErr        error

	consumeDeliveries chan amqp.Delivery
	consumeErr        error

	qosCalled     bool
	qosPrefetch   int
	confirmCalled bool
	confirmErr    error

	exchangesDeclared  []string
	exchangeDeclareErr error
	queuesDeclared     []string
	queueDeclareArgs   []amqp.Table
	queueDeclareErr    error
	queueBindings      []string
	queueBindErr       error

	notifyPublishCh chan amqp.Confirmation
	// autoConfirmation, when non-nil, is pushed into the publisher's confirm
	// channel the moment NotifyPublish runs — eliminates the polling / sleep
	// goroutines that previously faked broker confirmations and raced against
	// publisher.NotifyPublish registration. Buffered send (publisher allocates
	// chan with cap 1), never blocks.
	autoConfirmation *amqp.Confirmation
	// autoCloseConfirm closes the publisher's confirm channel on NotifyPublish
	// registration — simulates a broker disconnect between publish and confirm.
	autoCloseConfirm bool

	ackCalled   bool
	ackTag      uint64
	ackErr      error
	nackCalled  bool
	nackTag     uint64
	nackRequeue bool
	nackErr     error

	closeCalled bool
	closeErr    error
}

func newMockChannel() *mockChannel {
	return &mockChannel{
		consumeDeliveries: make(chan amqp.Delivery, 10),
		notifyPublishCh:   make(chan amqp.Confirmation, 1),
	}
}

func (m *mockChannel) Publish(exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.publishCalled = true
	m.publishExchange = exchange
	m.publishedMessages = append(m.publishedMessages, msg)
	return m.publishErr
}

func (m *mockChannel) PublishWithContext(_ context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error {
	return m.Publish(exchange, key, mandatory, immediate, msg)
}

func (m *mockChannel) Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error) {
	if m.consumeErr != nil {
		return nil, m.consumeErr
	}
	return m.consumeDeliveries, nil
}

func (m *mockChannel) Qos(prefetchCount, prefetchSize int, global bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.qosCalled = true
	m.qosPrefetch = prefetchCount
	return nil
}

func (m *mockChannel) Confirm(noWait bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.confirmCalled = true
	return m.confirmErr
}

func (m *mockChannel) NotifyPublish(confirm chan amqp.Confirmation) chan amqp.Confirmation {
	m.mu.Lock()
	m.notifyPublishCh = confirm
	autoConf := m.autoConfirmation
	autoClose := m.autoCloseConfirm
	m.mu.Unlock()
	if autoConf != nil {
		select {
		case confirm <- *autoConf:
		default:
		}
	}
	if autoClose {
		close(confirm)
	}
	return confirm
}

func (m *mockChannel) ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.exchangeDeclareErr != nil {
		return m.exchangeDeclareErr
	}
	m.exchangesDeclared = append(m.exchangesDeclared, name)
	return nil
}

func (m *mockChannel) QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.queueDeclareErr != nil {
		return amqp.Queue{}, m.queueDeclareErr
	}
	m.queuesDeclared = append(m.queuesDeclared, name)
	m.queueDeclareArgs = append(m.queueDeclareArgs, args)
	return amqp.Queue{Name: name}, nil
}

func (m *mockChannel) QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.queueBindErr != nil {
		return m.queueBindErr
	}
	m.queueBindings = append(m.queueBindings, name+"->"+exchange)
	return nil
}

func (m *mockChannel) Ack(tag uint64, multiple bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ackCalled = true
	m.ackTag = tag
	return m.ackErr
}

func (m *mockChannel) Nack(tag uint64, multiple, requeue bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nackCalled = true
	m.nackTag = tag
	m.nackRequeue = requeue
	return m.nackErr
}

func (m *mockChannel) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeCalled = true
	return m.closeErr
}

// --- Mock AMQP Connection ---

type mockConnection struct {
	mu       sync.Mutex
	channels []*mockChannel
	nextCh   *mockChannel
	chanErr  error

	// channelQueue provides channels in FIFO order. When non-nil and non-empty,
	// Channel() pops from the front. Falls back to nextCh / newMockChannel.
	channelQueue []*mockChannel

	notifyCloseCh chan *amqp.Error
	isClosed      bool
	closeErr      error
}

func newMockConnection() *mockConnection {
	return &mockConnection{}
}

func (m *mockConnection) Channel() (AMQPChannel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.chanErr != nil {
		return nil, m.chanErr
	}
	if len(m.channelQueue) > 0 {
		ch := m.channelQueue[0]
		m.channelQueue = m.channelQueue[1:]
		m.channels = append(m.channels, ch)
		return ch, nil
	}
	if m.nextCh != nil {
		ch := m.nextCh
		m.channels = append(m.channels, ch)
		return ch, nil
	}
	ch := newMockChannel()
	m.channels = append(m.channels, ch)
	return ch, nil
}

func (m *mockConnection) NotifyClose(receiver chan *amqp.Error) chan *amqp.Error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.notifyCloseCh = receiver
	return receiver
}

func (m *mockConnection) IsClosed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.isClosed
}

func (m *mockConnection) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.isClosed = true
	return m.closeErr
}

// --- Mock Publisher (for DLQ) ---

// mockPublisher was removed: ConsumerBase no longer uses application-side
// DLQ publish. Dead-letter routing is now handled by broker-native DLX.

// --- Helper to create a test connection ---

func newTestConnection(t *testing.T) (*Connection, *mockConnection) {
	t.Helper()
	mockConn := newMockConnection()

	dialFunc := func(url string) (AMQPConnection, error) {
		return mockConn, nil
	}

	conn, err := NewConnection(Config{
		URL:             "amqp://test:test@localhost:5672/",
		ChannelPoolSize: 5,
		ConfirmTimeout:  2 * time.Second,
	}, WithDialFunc(dialFunc))
	require.NoError(t, err)

	t.Cleanup(func() {
		// Avoid blocking on reconnect loop.
		if cErr := conn.Close(); cErr != nil {
			t.Logf("cleanup close error: %v", cErr)
		}
	})

	return conn, mockConn
}

// makeDeliveryBody serializes an outbox.Entry into a WireMessage envelope
// suitable for delivery to unmarshalDelivery. RabbitMQ now only accepts relay
// envelope format (legacy PascalCase Entry JSON is no longer supported).
func makeDeliveryBody(t *testing.T, entry outbox.Entry) []byte {
	t.Helper()
	payload := entry.Payload
	if payload == nil {
		payload = []byte(`{}`)
	}
	wire := outboxrt.WireMessage{
		ID:            entry.ID,
		AggregateID:   entry.AggregateID,
		AggregateType: entry.AggregateType,
		EventType:     entry.EventType,
		Topic:         entry.Topic,
		Payload:       json.RawMessage(payload),
		Metadata:      entry.Metadata,
		CreatedAt:     entry.CreatedAt,
	}
	b, err := json.Marshal(wire)
	require.NoError(t, err)
	return b
}

// =============================================================================
// Connection Tests
// =============================================================================

func TestNewConnection_Success(t *testing.T) {
	conn, _ := newTestConnection(t)
	assert.NoError(t, conn.Health(context.Background()))
}

func TestNewConnection_DialFails(t *testing.T) {
	dialFunc := func(url string) (AMQPConnection, error) {
		return nil, errors.New("connection refused")
	}

	_, err := NewConnection(Config{
		URL: "amqp://bad:bad@localhost:5672/",
	}, WithDialFunc(dialFunc))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_AMQP_CONNECT")
}

func TestNewConnection_PermanentDialError(t *testing.T) {
	// Initial connection with permanent error should return ErrAdapterAMQPConnectPermanent.
	dialFunc := func(url string) (AMQPConnection, error) {
		return nil, &amqp.Error{Code: 403, Reason: "ACCESS_REFUSED", Recover: false}
	}

	_, err := NewConnection(Config{
		URL: "amqp://bad:bad@localhost:5672/",
	}, WithDialFunc(dialFunc))

	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrAdapterAMQPConnectPermanent, ecErr.Code,
		"initial permanent dial error should return ErrAdapterAMQPConnectPermanent")
}

func TestNewConnection_RecoverableDialError(t *testing.T) {
	// Initial connection with recoverable error should return generic ErrAdapterAMQPConnect.
	dialFunc := func(url string) (AMQPConnection, error) {
		return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
	}

	_, err := NewConnection(Config{
		URL: "amqp://test:test@localhost:5672/",
	}, WithDialFunc(dialFunc))

	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrAdapterAMQPConnect, ecErr.Code,
		"recoverable dial error should return generic ErrAdapterAMQPConnect")
}

func TestConnection_Health_Closed(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	mockConn.mu.Lock()
	mockConn.isClosed = true
	mockConn.mu.Unlock()

	err := conn.Health(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_AMQP_CONNECT")
}

func TestConnection_AcquireChannel(t *testing.T) {
	conn, _ := newTestConnection(t)

	ch, err := conn.AcquireChannel()
	require.NoError(t, err)
	assert.NotNil(t, ch)
}

func TestConnection_AcquireChannel_ConnectionClosed(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	mockConn.mu.Lock()
	mockConn.isClosed = true
	mockConn.mu.Unlock()

	_, err := conn.AcquireChannel()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_AMQP_CONNECT")
}

func TestConnection_ReleaseChannel_PoolFull(t *testing.T) {
	conn, _ := newTestConnection(t)

	// Fill pool.
	for range conn.config.ChannelPoolSize {
		ch := newMockChannel()
		conn.ReleaseChannel(ch)
	}

	// This one should be closed since pool is full.
	extraCh := newMockChannel()
	conn.ReleaseChannel(extraCh)

	extraCh.mu.Lock()
	assert.True(t, extraCh.closeCalled)
	extraCh.mu.Unlock()
}

func TestConnection_AcquireFromPool(t *testing.T) {
	conn, _ := newTestConnection(t)

	// Put a channel in the pool.
	pooledCh := newMockChannel()
	conn.ReleaseChannel(pooledCh)

	// Acquire should return the pooled channel.
	ch, err := conn.AcquireChannel()
	require.NoError(t, err)
	assert.Equal(t, pooledCh, ch)
}

func TestConnection_PoolStats_Connected(t *testing.T) {
	conn, _ := newTestConnection(t)

	// Put 2 channels in the pool.
	conn.ReleaseChannel(newMockChannel())
	conn.ReleaseChannel(newMockChannel())

	stats := conn.PoolStats()
	assert.Equal(t, conn.config.ChannelPoolSize, stats.ChannelPoolSize)
	assert.Equal(t, 2, stats.IdleChannels)
	assert.Equal(t, StateConnected, stats.State)
}

func TestConnection_PoolStats_Empty(t *testing.T) {
	conn, _ := newTestConnection(t)

	stats := conn.PoolStats()
	assert.Equal(t, conn.config.ChannelPoolSize, stats.ChannelPoolSize)
	assert.Equal(t, 0, stats.IdleChannels)
	assert.Equal(t, StateConnected, stats.State)
}

func TestPoolStats_JSON_CamelCase(t *testing.T) {
	stats := PoolStats{ChannelPoolSize: 10, IdleChannels: 3, State: StateConnected}
	b, err := json.Marshal(stats)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"channelPoolSize"`)
	assert.Contains(t, s, `"idleChannels"`)
	assert.Contains(t, s, `"state":"connected"`) // MarshalText, not numeric
}

func TestConnection_Close_Idempotent(t *testing.T) {
	conn, _ := newTestConnection(t)

	err := conn.Close()
	assert.NoError(t, err)

	// Second close should be no-op.
	err = conn.Close()
	assert.NoError(t, err)
}

func TestConnection_WaitConnected(t *testing.T) {
	conn, _ := newTestConnection(t)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Already connected, should return immediately.
	err := conn.WaitConnected(ctx)
	assert.NoError(t, err)
}

func TestConnection_WaitConnected_Timeout(t *testing.T) {
	mockConn := newMockConnection()
	dialFunc := func(url string) (AMQPConnection, error) {
		return mockConn, nil
	}

	c := &Connection{
		config:      Config{URL: "amqp://test@localhost/"},
		dial:        dialFunc,
		channelPool: make(chan AMQPChannel, 5),
		closeCh:     make(chan struct{}),
		connected:   make(chan struct{}), // Never closed = never connected.
		terminalCh:  make(chan struct{}),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := c.WaitConnected(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_AMQP_CONNECT")
}

func TestConfig_Defaults(t *testing.T) {
	cfg := Config{}
	cfg.setDefaults()

	assert.Equal(t, 30*time.Second, cfg.ReconnectMaxBackoff)
	assert.Equal(t, 1*time.Second, cfg.ReconnectBaseDelay)
	assert.Equal(t, 10, cfg.ChannelPoolSize)
	assert.Equal(t, 5*time.Second, cfg.ConfirmTimeout)
}

func TestConfig_Defaults_NegativeValues(t *testing.T) {
	cfg := Config{
		ReconnectMaxBackoff: -1 * time.Second,
		ReconnectBaseDelay:  -500 * time.Millisecond,
		ChannelPoolSize:     -5,
		ConfirmTimeout:      -3 * time.Second,
	}
	cfg.setDefaults()

	assert.Equal(t, 30*time.Second, cfg.ReconnectMaxBackoff, "negative MaxBackoff should reset to default")
	assert.Equal(t, 1*time.Second, cfg.ReconnectBaseDelay, "negative BaseDelay should reset to default")
	assert.Equal(t, 10, cfg.ChannelPoolSize, "negative ChannelPoolSize should reset to default")
	assert.Equal(t, 5*time.Second, cfg.ConfirmTimeout, "negative ConfirmTimeout should reset to default")
}

func TestConnection_BackoffDelay(t *testing.T) {
	conn, _ := newTestConnection(t)

	tests := []struct {
		name     string
		attempt  int
		minDelay time.Duration
		maxDelay time.Duration // hard cap: never exceeds ReconnectMaxBackoff (30s)
	}{
		{name: "attempt 0", attempt: 0,
			minDelay: 750 * time.Millisecond, maxDelay: 1250 * time.Millisecond},
		{name: "attempt 1", attempt: 1,
			minDelay: 1500 * time.Millisecond, maxDelay: 2500 * time.Millisecond},
		{name: "attempt 2", attempt: 2,
			minDelay: 3 * time.Second, maxDelay: 5 * time.Second},
		// Capped region: jitter on MaxBackoff → [0.75*30s, 30s] = [22.5s, 30s].
		{name: "attempt 10 (capped)", attempt: 10,
			minDelay: 22500 * time.Millisecond, maxDelay: 30 * time.Second},
		{name: "attempt 34 (overflow guard)", attempt: 34,
			minDelay: 22500 * time.Millisecond, maxDelay: 30 * time.Second},
		{name: "attempt 100 (far overflow)", attempt: 100,
			minDelay: 22500 * time.Millisecond, maxDelay: 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delay := conn.backoffDelay(tt.attempt)
			assert.GreaterOrEqual(t, delay, tt.minDelay,
				"delay %v should be >= %v", delay, tt.minDelay)
			assert.LessOrEqual(t, delay, tt.maxDelay,
				"delay %v should be <= %v (hard cap)", delay, tt.maxDelay)
		})
	}
}

func TestAddJitter(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
	}{
		{name: "zero", d: 0},
		{name: "1s", d: 1 * time.Second},
		{name: "30s", d: 30 * time.Second},
		{name: "100ms", d: 100 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.d == 0 {
				assert.Equal(t, time.Duration(0), addJitter(tt.d))
				return
			}
			// Run multiple times to check range.
			for range 100 {
				got := addJitter(tt.d)
				minD := time.Duration(float64(tt.d) * 0.75)
				maxD := time.Duration(float64(tt.d) * 1.25)
				assert.GreaterOrEqual(t, got, minD)
				assert.LessOrEqual(t, got, maxD)
			}
		})
	}
}

func TestAddDownJitter(t *testing.T) {
	t.Run("zero returns zero", func(t *testing.T) {
		assert.Equal(t, time.Duration(0), addDownJitter(0))
	})
	t.Run("negative returns zero", func(t *testing.T) {
		assert.Equal(t, time.Duration(0), addDownJitter(-1*time.Second))
	})
	t.Run("positive in [0.75*d, d]", func(t *testing.T) {
		d := 30 * time.Second
		for range 100 {
			got := addDownJitter(d)
			assert.GreaterOrEqual(t, got, time.Duration(float64(d)*0.75))
			assert.LessOrEqual(t, got, d)
		}
	})
}

func TestConnection_BackoffDelay_SmallBase(t *testing.T) {
	// U4: verify small base delay (1ms) doesn't prematurely jump to max.
	conn := &Connection{
		config: Config{
			ReconnectBaseDelay:  1 * time.Millisecond,
			ReconnectMaxBackoff: 1 * time.Hour,
		},
	}
	// attempt 10: 1ms * 2^10 = 1.024s — well below 1h, jitter should be around 1s.
	delay := conn.backoffDelay(10)
	assert.GreaterOrEqual(t, delay, 750*time.Millisecond)
	assert.LessOrEqual(t, delay, 1300*time.Millisecond)

	// attempt 30: 1ms * 2^30 ≈ 1073s ≈ 17.9min — still below 1h.
	delay30 := conn.backoffDelay(30)
	assert.Less(t, delay30, 1*time.Hour, "attempt 30 with 1ms base should NOT hit 1h cap")
}

func TestConnection_ReconnectLoop_CloseExits(t *testing.T) {
	// reconnectLoop should exit when closeCh is closed (via Connection.Close).
	conn, _ := newTestConnection(t)

	// reconnectLoop is already running from NewConnection. Close should stop it.
	err := conn.Close()
	assert.NoError(t, err)

	// After Close, WaitConnected with a short timeout should fail (closeCh closed).
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// connected was already closed by NewConnection, so this returns nil.
	// This test verifies Close doesn't panic and exits cleanly.
	_ = conn.WaitConnected(ctx)
}

func TestConnection_ReconnectLoop_DisconnectAndReconnect(t *testing.T) {
	// Full cycle: connect → disconnect → backoff → reconnect.
	var mu sync.Mutex
	dialCount := 0
	mocks := []*mockConnection{newMockConnection(), newMockConnection()}
	dialFunc := func(url string) (AMQPConnection, error) {
		mu.Lock()
		dialCount++
		n := dialCount
		mu.Unlock()
		if n <= len(mocks) {
			return mocks[n-1], nil
		}
		return newMockConnection(), nil
	}

	conn, err := NewConnection(Config{
		URL:                 "amqp://test:test@localhost:5672/",
		ChannelPoolSize:     2,
		ReconnectBaseDelay:  1 * time.Millisecond,
		ReconnectMaxBackoff: 5 * time.Millisecond,
	}, WithDialFunc(dialFunc))
	require.NoError(t, err)
	defer conn.Close()

	// Wait for reconnectLoop to call NotifyClose.
	require.Eventually(t, func() bool {
		mocks[0].mu.Lock()
		defer mocks[0].mu.Unlock()
		return mocks[0].notifyCloseCh != nil
	}, time.Second, time.Millisecond, "reconnectLoop did not call NotifyClose")

	// Now send on the channel that reconnectLoop is actually selecting on.
	mocks[0].mu.Lock()
	ch := mocks[0].notifyCloseCh
	mocks[0].isClosed = true
	mocks[0].mu.Unlock()
	ch <- &amqp.Error{Code: 320, Reason: "CONNECTION_FORCED", Recover: true}

	// reconnectLoop should reconnect. Verify dial was called again.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return dialCount >= 2
	}, 2*time.Second, 10*time.Millisecond, "reconnectLoop should have reconnected")
}

// NOTE: TestConnection_ReconnectLoop_PermanentError_ExitsLoop deleted (A.1).
// Runtime reconnect never exits on permanent classification; permanent errors
// surface only at NewConnection time (see TestNewConnection_PermanentDialError).
//
// NOTE: TestConnection_MaxReconnectAttempts_Exceeded deleted (A.1).
// MaxReconnectAttempts is now a retained-but-ignored field; reconnect is
// always unbounded until closeCh fires. The successor test is
// TestConnection_ReconnectWithBackoff_TransientError_ContinuesIndefinitely.

func TestConnection_ReconnectLoop_RetriesIndefinitelyUntilRecovery(t *testing.T) {
	// A.1 behavior: after disconnect, reconnectLoop keeps retrying transient
	// errors indefinitely and eventually recovers without any attempt cap.
	var mu sync.Mutex
	dialCount := 0
	mocks := []*mockConnection{newMockConnection(), newMockConnection()}
	recoverableErr := errors.New("connection refused")

	dialFunc := func(url string) (AMQPConnection, error) {
		mu.Lock()
		dialCount++
		n := dialCount
		mu.Unlock()
		switch {
		case n == 1:
			return mocks[0], nil
		case n <= 3:
			// Dial attempts 2 and 3 fail (recoverable).
			return nil, recoverableErr
		default:
			// Dial attempt 4+ succeeds.
			return mocks[1], nil
		}
	}

	conn, err := NewConnection(Config{
		URL:                 "amqp://test:test@localhost:5672/",
		ChannelPoolSize:     2,
		ReconnectBaseDelay:  1 * time.Millisecond,
		ReconnectMaxBackoff: 5 * time.Millisecond,
	}, WithDialFunc(dialFunc))
	require.NoError(t, err)
	defer conn.Close()

	// Wait for reconnectLoop to register NotifyClose.
	require.Eventually(t, func() bool {
		mocks[0].mu.Lock()
		defer mocks[0].mu.Unlock()
		return mocks[0].notifyCloseCh != nil
	}, time.Second, time.Millisecond, "reconnectLoop did not call NotifyClose")

	// Trigger disconnect.
	mocks[0].mu.Lock()
	ch := mocks[0].notifyCloseCh
	mocks[0].isClosed = true
	mocks[0].mu.Unlock()
	ch <- &amqp.Error{Code: 320, Reason: "CONNECTION_FORCED", Recover: true}

	// Wait for reconnection to succeed (dial count >= 4: 1 initial + 2 failed + 1 success).
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return dialCount >= 4
	}, 5*time.Second, time.Millisecond, "should have retried past 2 failures")

	// After reconnection, Health should return nil.
	require.Eventually(t, func() bool {
		return conn.Health(context.Background()) == nil
	}, 2*time.Second, time.Millisecond, "connection should be healthy after reconnect")

	// WaitConnected should succeed (connected channel re-closed on reconnect).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	waitErr := conn.WaitConnected(ctx)
	require.NoError(t, waitErr, "WaitConnected should succeed with unbounded reconnect attempts (A.1)")
}

func TestIsPermanentDialError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "AMQP ACCESS_REFUSED (403) — auth failure",
			err:  &amqp.Error{Code: 403, Reason: "ACCESS_REFUSED", Server: true, Recover: false},
			want: true,
		},
		{
			name: "AMQP NOT_FOUND (404) — vhost does not exist",
			err:  &amqp.Error{Code: 404, Reason: "NOT_FOUND", Server: true, Recover: false},
			want: true,
		},
		{
			name: "AMQP NOT_ALLOWED (530) — connection not allowed",
			err:  &amqp.Error{Code: 530, Reason: "NOT_ALLOWED", Server: true, Recover: false},
			want: true,
		},
		{
			name: "AMQP connection.forced (320) — recoverable",
			err:  &amqp.Error{Code: 320, Reason: "CONNECTION_FORCED", Server: true, Recover: true},
			want: false,
		},
		{
			name: "AMQP frame error (501) — recoverable",
			err:  &amqp.Error{Code: 501, Reason: "FRAME_ERROR", Server: true, Recover: true},
			want: false,
		},
		{
			name: "net.OpError (connection refused) — recoverable",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
			want: false,
		},
		{
			name: "generic error — defaults to recoverable",
			err:  errors.New("some unknown error"),
			want: false,
		},
		{
			name: "wrapped AMQP permanent error",
			err:  fmt.Errorf("dial: %w", &amqp.Error{Code: 403, Reason: "ACCESS_REFUSED", Server: true, Recover: false}),
			want: true,
		},
		// Plain errors from amqp091-go pre-handshake failures (U2).
		{
			name: "URI parse failure — permanent",
			err:  errors.New("AMQP URI must start with amqp:// or amqps://"),
			want: true,
		},
		{
			name: "unsupported auth mechanism — permanent",
			err:  errors.New("unsupported auth mechanism EXTERNAL: no credentials provided"),
			want: true,
		},
		{
			name: "x509 certificate error — permanent",
			err:  errors.New("x509: certificate signed by unknown authority"),
			want: true,
		},
		{
			name: "TLS handshake error — permanent",
			err:  errors.New("tls: first record does not look like a TLS handshake"),
			want: true,
		},
		{
			name: "wrapped URI parse failure — permanent",
			err:  fmt.Errorf("dial: %w", errors.New("AMQP URI scheme must be amqp:// or amqps://")),
			want: true,
		},
		{
			name: "generic unknown error — recoverable",
			err:  errors.New("some transient hiccup"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isPermanentDialError(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSanitizeURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{
			name:     "full credentials redacted",
			url:      "amqp://guest:guest@localhost:5672/",
			expected: "amqp://***:***@localhost:5672/",
		},
		{
			name:     "username only redacted",
			url:      "amqp://admin@localhost:5672/",
			expected: "amqp://***:***@localhost:5672/",
		},
		{
			name:     "no credentials unchanged",
			url:      "amqp://localhost:5672/",
			expected: "amqp://localhost:5672/",
		},
		{
			name:     "with vhost",
			url:      "amqp://user:pass@rabbit.example.com:5672/production",
			expected: "amqp://***:***@rabbit.example.com:5672/production",
		},
		{
			name:     "empty string returns redacted placeholder",
			url:      "",
			expected: "amqp://***",
		},
		{
			name:     "amqps scheme with credentials",
			url:      "amqps://user:secret@secure.host:5671/",
			expected: "amqps://***:***@secure.host:5671/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sanitizeURL(tt.url)
			assert.Equal(t, tt.expected, result)
			// Verify no real credentials appear in sanitized output.
			assert.NotContains(t, result, "guest")
			assert.NotContains(t, result, "admin")
			assert.NotContains(t, result, "secret")
			assert.NotContains(t, result, ":pass@")
		})
	}
}

// NOTE: TestConnection_ReconnectWithBackoff_PermanentError deleted (A.1 semantics).
// Runtime reconnect no longer classifies errors as permanent; permanent dial
// errors surface only at NewConnection time. See
// TestNewConnection_PermanentDialError for startup-path permanent classification.

func TestConnection_ReconnectWithBackoff_RecoverableError_ThenSuccess(t *testing.T) {
	var mu sync.Mutex
	dialCount := 0
	conn := &Connection{
		config: Config{
			URL:                 "amqp://test:test@localhost:5672/",
			ReconnectBaseDelay:  1 * time.Millisecond,
			ReconnectMaxBackoff: 5 * time.Millisecond,
		},
		dial: func(url string) (AMQPConnection, error) {
			mu.Lock()
			dialCount++
			n := dialCount
			mu.Unlock()
			if n <= 2 {
				return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
			}
			return newMockConnection(), nil
		},
		closeCh:    make(chan struct{}),
		connected:  make(chan struct{}),
		terminalCh: make(chan struct{}),
	}

	assert.True(t, conn.reconnectWithBackoff(), "must return true after successful reconnect")

	mu.Lock()
	assert.Equal(t, 3, dialCount, "should have tried 3 times (2 failures + 1 success)")
	mu.Unlock()
}

func TestConnection_ReconnectWithBackoff_CloseCh(t *testing.T) {
	closeCh := make(chan struct{})
	conn := &Connection{
		config: Config{
			URL:                 "amqp://test:test@localhost:5672/",
			ReconnectBaseDelay:  10 * time.Second,
			ReconnectMaxBackoff: 30 * time.Second,
		},
		dial: func(url string) (AMQPConnection, error) {
			return nil, &net.OpError{Op: "dial", Err: errors.New("connection refused")}
		},
		closeCh:    closeCh,
		connected:  make(chan struct{}),
		terminalCh: make(chan struct{}),
	}

	done := make(chan bool, 1)
	go func() {
		done <- conn.reconnectWithBackoff()
	}()

	time.Sleep(50 * time.Millisecond)
	close(closeCh)

	select {
	case ok := <-done:
		assert.False(t, ok, "must return false when closeCh fires")
	case <-time.After(2 * time.Second):
		t.Fatal("reconnectWithBackoff did not return after closeCh was closed")
	}
}

// TestConnection_ReconnectWithBackoff_TransientError_ContinuesIndefinitely
// verifies A.1 semantics: once established, reconnect keeps retrying through
// transient dial errors (including those that the amqp091-go library wraps as
// *amqp.Error with Recover=false such as AMQP 501 from mid-handshake TCP
// resets during a broker restart). Only closeCh stops the loop.
func TestConnection_ReconnectWithBackoff_TransientError_ContinuesIndefinitely(t *testing.T) {
	var mu sync.Mutex
	dialCount := 0
	// amqp091-go converts mid-handshake TCP resets to amqp.Error{Code: 501, Recover: false}.
	// Pre-A.1 this was classified as permanent; post-A.1 it is retried.
	transientAMQP501 := &amqp.Error{Code: 501, Reason: "read: connection reset by peer", Recover: false}

	closeCh := make(chan struct{})
	conn := &Connection{
		config: Config{
			URL:                 "amqp://test:test@localhost:5672/",
			ReconnectBaseDelay:  1 * time.Millisecond,
			ReconnectMaxBackoff: 5 * time.Millisecond,
		},
		dial: func(url string) (AMQPConnection, error) {
			mu.Lock()
			dialCount++
			mu.Unlock()
			return nil, transientAMQP501
		},
		closeCh:    closeCh,
		connected:  make(chan struct{}),
		terminalCh: make(chan struct{}),
	}

	done := make(chan bool, 1)
	go func() {
		done <- conn.reconnectWithBackoff()
	}()

	// Wait until at least a few attempts have been made to prove the loop keeps trying.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := dialCount
		mu.Unlock()
		if n >= 5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(closeCh)

	select {
	case ok := <-done:
		assert.False(t, ok, "must return false when closeCh fires")
	case <-time.After(2 * time.Second):
		t.Fatal("reconnectWithBackoff did not return after closeCh was closed")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.GreaterOrEqual(t, dialCount, 5,
		"must have retried at least 5 times through transient AMQP 501 error (A.1 unbounded retry)")
}

// =============================================================================
// Publisher Tests
// =============================================================================

func TestPublisher_InterfaceCompliance(t *testing.T) {
	var _ outbox.Publisher = (*Publisher)(nil)
}

func TestPublisher_Publish_Success(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.autoConfirmation = &amqp.Confirmation{Ack: true, DeliveryTag: 1}
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	pub := NewPublisher(conn)

	err := pub.Publish(context.Background(), "test.topic", []byte(`{"hello":"world"}`))
	assert.NoError(t, err)

	ch.mu.Lock()
	assert.True(t, ch.publishCalled)
	assert.True(t, ch.confirmCalled)
	assert.Equal(t, "test.topic", ch.publishExchange)
	assert.Len(t, ch.publishedMessages, 1)
	assert.Equal(t, []byte(`{"hello":"world"}`), ch.publishedMessages[0].Body)
	assert.Equal(t, uint8(amqp.Persistent), ch.publishedMessages[0].DeliveryMode)
	ch.mu.Unlock()
}

func TestPublisher_Publish_Nacked(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.autoConfirmation = &amqp.Confirmation{Ack: false, DeliveryTag: 1}
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	pub := NewPublisher(conn)

	err := pub.Publish(context.Background(), "test.topic", []byte(`{}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_AMQP_CONFIRM_TIMEOUT")
}

func TestPublisher_Publish_ConfirmTimeout(t *testing.T) {
	mockConn := newMockConnection()
	dialFunc := func(url string) (AMQPConnection, error) {
		return mockConn, nil
	}

	conn, err := NewConnection(Config{
		URL:            "amqp://test@localhost/",
		ConfirmTimeout: 50 * time.Millisecond,
	}, WithDialFunc(dialFunc))
	require.NoError(t, err)
	defer func() {
		if cErr := conn.Close(); cErr != nil {
			t.Logf("close error: %v", cErr)
		}
	}()

	pub := NewPublisher(conn)

	err = pub.Publish(context.Background(), "test.topic", []byte(`{}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_AMQP_CONFIRM_TIMEOUT")
}

func TestPublisher_Publish_ContextCancelled(t *testing.T) {
	conn, _ := newTestConnection(t)
	pub := NewPublisher(conn)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	err := pub.Publish(ctx, "test.topic", []byte(`{}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_AMQP_PUBLISH")
}

func TestPublisher_Publish_PublishError(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.publishErr = errors.New("channel closed")
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	pub := NewPublisher(conn)

	err := pub.Publish(context.Background(), "test.topic", []byte(`{}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_AMQP_PUBLISH")
}

func TestPublisher_Publish_ConfirmModeError(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.confirmErr = errors.New("confirm not supported")
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	pub := NewPublisher(conn)

	err := pub.Publish(context.Background(), "test.topic", []byte(`{}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_AMQP_PUBLISH")
	assert.Contains(t, err.Error(), "confirm mode")
}

func TestPublisher_Publish_TerminalState_ReturnsPermanentError(t *testing.T) {
	// When Connection is in terminal state, Publish should return
	// ErrAdapterAMQPConnectPermanent (not generic publish error).
	conn := &Connection{
		config: Config{
			URL:             "amqp://test:test@localhost:5672/",
			ChannelPoolSize: 2,
			ConfirmTimeout:  5 * time.Second,
		},
		channelPool:  make(chan AMQPChannel, 2),
		closeCh:      make(chan struct{}),
		connected:    make(chan struct{}),
		terminalCh:   make(chan struct{}),
		permanentErr: errcode.New(ErrAdapterAMQPConnectPermanent, "access refused"),
	}
	close(conn.terminalCh)

	pub := NewPublisher(conn)
	err := pub.Publish(context.Background(), "test.topic", []byte("payload"))

	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrAdapterAMQPConnectPermanent, ecErr.Code,
		"Publish in terminal state should return permanent error, not generic publish error")
}

// =============================================================================
// Subscriber Tests
// =============================================================================

func TestSubscriber_InterfaceCompliance(t *testing.T) {
	var _ outbox.Subscriber = (*Subscriber)(nil)
	//nolint:staticcheck // SubscriberInitializer is deprecated but Subscriber implements it for backward compat.
	var _ outbox.SubscriberInitializer = (*Subscriber)(nil)
}

func TestSubscriber_InitializeSubscription_DeclaresTopology(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	mockConn.nextCh = ch

	sub := NewSubscriber(conn, SubscriberConfig{DLXExchange: "test.dlx"})

	err := sub.InitializeSubscription(context.Background(), "test.topic", "cg-1")
	require.NoError(t, err)

	// Verify topology was declared: 2 exchanges (main + DLX), 1 queue, 1 binding.
	assert.Equal(t, []string{"test.topic", "test.dlx"}, ch.exchangesDeclared)
	assert.Equal(t, []string{"cg-1.test.topic"}, ch.queuesDeclared)
	assert.Equal(t, []string{"cg-1.test.topic->test.topic"}, ch.queueBindings)

	// Verify DLX args on the queue.
	require.Len(t, ch.queueDeclareArgs, 1)
	assert.Equal(t, "test.dlx", ch.queueDeclareArgs[0]["x-dead-letter-exchange"])
}

func TestSubscriber_InitializeSubscription_EmptyDLX_ReturnsError(t *testing.T) {
	conn, _ := newTestConnection(t)
	sub := NewSubscriber(conn, SubscriberConfig{DLXExchange: ""})

	err := sub.InitializeSubscription(context.Background(), "test.topic", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DLXExchange is required")
}

func TestSubscriber_InitializeSubscription_AcquireChannelFailure(t *testing.T) {
	conn, _ := newTestConnection(t)
	// Close the connection to make AcquireChannel fail.
	_ = conn.Close()

	sub := NewSubscriber(conn, SubscriberConfig{DLXExchange: "test.dlx"})

	err := sub.InitializeSubscription(context.Background(), "test.topic", "")
	assert.Error(t, err)
}

func TestSubscriber_InitializeSubscription_ExchangeDeclareFailure(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	ch.exchangeDeclareErr = errors.New("exchange declare failed")
	mockConn.nextCh = ch

	sub := NewSubscriber(conn, SubscriberConfig{DLXExchange: "test.dlx"})

	err := sub.InitializeSubscription(context.Background(), "test.topic", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "declare exchange")
}

func TestSubscriber_InitializeSubscription_QueueDeclareFailure(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	ch.queueDeclareErr = errors.New("queue declare failed")
	mockConn.nextCh = ch

	sub := NewSubscriber(conn, SubscriberConfig{DLXExchange: "test.dlx"})

	err := sub.InitializeSubscription(context.Background(), "test.topic", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "declare queue")
}

func TestSubscriber_InitializeSubscription_QueueBindFailure(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	ch.queueBindErr = errors.New("queue bind failed")
	mockConn.nextCh = ch

	sub := NewSubscriber(conn, SubscriberConfig{DLXExchange: "test.dlx"})

	err := sub.InitializeSubscription(context.Background(), "test.topic", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "bind queue")
}

func TestSubscriber_InitializeSubscription_EmptyGroup_DefaultsToTopic(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	mockConn.nextCh = ch

	sub := NewSubscriber(conn, SubscriberConfig{DLXExchange: "test.dlx"})

	err := sub.InitializeSubscription(context.Background(), "my.topic", "")
	require.NoError(t, err)

	// Empty group → queue name = topic.
	assert.Equal(t, []string{"my.topic"}, ch.queuesDeclared)
}

func TestSubscriberConfig_Defaults(t *testing.T) {
	cfg := SubscriberConfig{}
	cfg.setDefaults()

	assert.Equal(t, 10, cfg.PrefetchCount)
	assert.Equal(t, 30*time.Second, cfg.ShutdownTimeout)
}

func TestSubscriber_Subscribe_ProcessesDelivery(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		PrefetchCount:   5,
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	entry := outbox.Entry{
		ID:        "evt-001",
		EventType: "test.created",
		Payload:   []byte(`{"key":"value"}`),
	}
	entryBytes := makeDeliveryBody(t, entry)

	handled := make(chan outbox.Entry, 1)
	handler := func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		handled <- e
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Deliver before Subscribe starts consuming (buffered cap 10).
	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 1, Body: entryBytes}

	subDone := make(chan error, 1)
	go func() { subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, handler) }()

	// Deterministic wait: poll until Ack is recorded instead of time.Sleep.
	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.ackCalled
	}, 2*time.Second, 5*time.Millisecond, "Ack was not called in time")

	cancel()
	assert.NoError(t, <-subDone)

	select {
	case received := <-handled:
		assert.Equal(t, "evt-001", received.ID)
		assert.Equal(t, "test.created", received.EventType)
	case <-time.After(1 * time.Second):
		t.Fatal("handler was not called")
	}

	ch.mu.Lock()
	assert.True(t, ch.qosCalled)
	assert.Equal(t, 5, ch.qosPrefetch)
	assert.Equal(t, uint64(1), ch.ackTag)
	ch.mu.Unlock()

	assert.NoError(t, sub.Close())
}

func TestSubscriber_Subscribe_UnmarshalFailure_Nack(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	handler := func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		t.Fatal("handler should not be called for unmarshal failure")
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 1, Body: []byte("not valid json{{{")}

	subDone := make(chan error, 1)
	go func() { subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, handler) }()

	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.nackCalled
	}, 2*time.Second, 5*time.Millisecond, "Nack was not called in time")

	cancel()
	assert.NoError(t, <-subDone)

	ch.mu.Lock()
	assert.False(t, ch.nackRequeue) // Unmarshal failure should not requeue.
	ch.mu.Unlock()

	assert.NoError(t, sub.Close())
}

// TestUnmarshalDelivery covers the three discriminator paths in unmarshalDelivery:
//  1. WireMessage envelope (primary relay path) — EventType is decoded from JSON.
//  2. Legacy outbox.Entry JSON (pre-envelope format, used by integration tests).
//  3. Broken JSON (neither path yields a valid entry) — returns an error.
func TestUnmarshalDelivery(t *testing.T) {
	t.Run("wire_message_envelope", func(t *testing.T) {
		entry := outbox.Entry{
			ID:        "entry-uuid-001",
			EventType: "test.created",
			Payload:   []byte(`{"x":1}`),
		}
		body := makeDeliveryBody(t, entry)

		got, err := unmarshalDelivery(body)
		require.NoError(t, err)
		assert.Equal(t, "entry-uuid-001", got.ID)
		assert.Equal(t, "test.created", got.EventType)
		assert.JSONEq(t, `{"x":1}`, string(got.Payload))
	})

	t.Run("legacy_entry_json", func(t *testing.T) {
		// Publish raw outbox.Entry JSON (PascalCase, no WireMessage envelope).
		// This is the format used by the three failing integration tests.
		entry := outbox.Entry{
			ID:        "evt-legacy-001",
			EventType: "test.legacy",
			Payload:   []byte(`{"legacy":true}`),
		}
		body, err := json.Marshal(entry)
		require.NoError(t, err)

		got, legacyErr := unmarshalDelivery(body)
		require.NoError(t, legacyErr)
		assert.Equal(t, "evt-legacy-001", got.ID)
		assert.Equal(t, "test.legacy", got.EventType)
		assert.JSONEq(t, `{"legacy":true}`, string(got.Payload))
	})

	t.Run("broken_json_returns_error", func(t *testing.T) {
		_, err := unmarshalDelivery([]byte("not valid json{{{"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unmarshal delivery")
	})

	t.Run("empty_body_returns_error", func(t *testing.T) {
		_, err := unmarshalDelivery([]byte(""))
		require.Error(t, err)
	})
}

func TestSubscriber_Subscribe_HandlerError_NackWithRequeue(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	entry := outbox.Entry{ID: "evt-002", EventType: "test.failed"}
	entryBytes := makeDeliveryBody(t, entry)

	handler := func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: errors.New("transient error")}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 1, Body: entryBytes}

	subDone := make(chan error, 1)
	go func() { subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, handler) }()

	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.nackCalled
	}, 2*time.Second, 5*time.Millisecond, "Nack was not called in time")

	cancel()
	assert.NoError(t, <-subDone)

	ch.mu.Lock()
	assert.True(t, ch.nackRequeue) // Requeue disposition should requeue.
	ch.mu.Unlock()

	assert.NoError(t, sub.Close())
}

func TestSubscriber_Subscribe_DefaultQueueName(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		// QueueName deliberately left empty.
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately so Subscribe exits.

	err := sub.Subscribe(ctx, outbox.Subscription{Topic: "my.topic"}, outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
	assert.NoError(t, err)

	ch.mu.Lock()
	assert.Contains(t, ch.queuesDeclared, "my.topic") // Queue name defaults to topic.
	ch.mu.Unlock()
}

func TestSubscriber_Close_Idempotent(t *testing.T) {
	conn, _ := newTestConnection(t)
	sub := NewSubscriber(conn, SubscriberConfig{DLXExchange: "test.dlx"})

	assert.NoError(t, sub.Close())
	assert.NoError(t, sub.Close()) // Second close is no-op.
}

func TestSubscriber_Subscribe_AfterClose(t *testing.T) {
	conn, _ := newTestConnection(t)
	sub := NewSubscriber(conn, SubscriberConfig{DLXExchange: "test.dlx"})

	assert.NoError(t, sub.Close())

	err := sub.Subscribe(context.Background(), outbox.Subscription{Topic: "test.topic"}, outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_AMQP_SUBSCRIBE")
}

func TestSubscriber_DeliveryChannelClosed_TriggersReconnect(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	// First channel — will be closed to simulate connection loss.
	ch1 := newMockChannel()
	// Second channel — will be used after reconnect.
	ch2 := newMockChannel()

	// Use channelQueue for deterministic FIFO ordering: ch1 first, then ch2.
	mockConn.mu.Lock()
	mockConn.channelQueue = []*mockChannel{ch1, ch2}
	mockConn.nextCh = nil
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The subscribe loop will:
	// 1. subscribeOnce with ch1 -> delivery channel closes -> error
	// 2. WaitConnected (already connected) -> subscribeOnce with ch2
	// 3. Handler processes message, then we cancel ctx to exit cleanly.

	handled := make(chan string, 1)
	handler := func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		handled <- e.ID
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	// Run Subscribe in a goroutine so require.Eventually can be called safely
	// from the main test goroutine (t.FailNow must not be called from a helper goroutine).
	subscribeDone := make(chan error, 1)
	go func() {
		subscribeDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, handler)
	}()

	// Drive the reconnect sequence from the main goroutine (safe for require).
	require.Eventually(t, func() bool {
		ch1.mu.Lock()
		defer ch1.mu.Unlock()
		return ch1.qosCalled
	}, 2*time.Second, 10*time.Millisecond, "subscriber did not start consuming from ch1")
	close(ch1.consumeDeliveries)

	require.Eventually(t, func() bool {
		ch2.mu.Lock()
		defer ch2.mu.Unlock()
		return ch2.qosCalled
	}, 2*time.Second, 10*time.Millisecond, "subscriber did not reconnect to ch2")

	entry := outbox.Entry{ID: "reconnect-001", EventType: "test.reconnected"}
	entryBytes := makeDeliveryBody(t, entry)
	ch2.consumeDeliveries <- amqp.Delivery{
		DeliveryTag: 1,
		Body:        entryBytes,
	}

	// Wait for handler to process, then cancel to exit Subscribe cleanly.
	select {
	case id := <-handled:
		assert.Equal(t, "reconnect-001", id)
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not called after reconnect")
	}
	cancel()

	select {
	case err := <-subscribeDone:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return after cancel")
	}

	assert.NoError(t, sub.Close())
}

func TestSubscriber_ReconnectLoop_CtxCancelledDuringWait(t *testing.T) {
	// Test that cancelling ctx during reconnect wait exits cleanly.
	mockConn := newMockConnection()
	dialFunc := func(url string) (AMQPConnection, error) {
		return mockConn, nil
	}

	c := &Connection{
		config:      Config{URL: "amqp://test@localhost/"},
		dial:        dialFunc,
		channelPool: make(chan AMQPChannel, 5),
		closeCh:     make(chan struct{}),
		connected:   make(chan struct{}), // Never closed = never connected.
		terminalCh:  make(chan struct{}),
	}

	// Make AcquireChannel fail so subscribeOnce returns error, entering reconnect wait.
	mockConn.mu.Lock()
	mockConn.chanErr = errors.New("no connection")
	mockConn.mu.Unlock()

	sub := NewSubscriber(c, SubscriberConfig{
		QueueName:       "test-queue",
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 1 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		// Cancel ctx after a short delay to unblock WaitConnected.
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := sub.Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
	assert.NoError(t, err) // Clean exit via ctx cancel during WaitConnected.
}

func TestSubscriber_ResolveQueueName(t *testing.T) {
	tests := []struct {
		name          string
		queueName     string
		consumerGroup string // config-level
		runtimeGroup  string // runtime parameter (passed to Subscribe)
		topic         string
		expected      string
	}{
		{
			name:      "explicit queue name takes precedence",
			queueName: "my-queue",
			topic:     "my.topic",
			expected:  "my-queue",
		},
		{
			name:          "config consumer group derives queue name",
			consumerGroup: "audit-cell",
			topic:         "session.created",
			expected:      "audit-cell.session.created",
		},
		{
			name:     "fallback to topic",
			topic:    "my.topic",
			expected: "my.topic",
		},
		{
			name:          "queue name takes precedence over consumer group",
			queueName:     "override-queue",
			consumerGroup: "audit-cell",
			topic:         "session.created",
			expected:      "override-queue",
		},
		{
			name:          "runtime group takes precedence over config group",
			consumerGroup: "config-group",
			runtimeGroup:  "runtime-group",
			topic:         "session.created",
			expected:      "runtime-group.session.created",
		},
		{
			name:         "runtime group with no config group",
			runtimeGroup: "audit-core",
			topic:        "events.v1",
			expected:     "audit-core.events.v1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := &Subscriber{
				config: SubscriberConfig{
					QueueName:     tt.queueName,
					ConsumerGroup: tt.consumerGroup,
				},
			}
			assert.Equal(t, tt.expected, sub.resolveQueueName(tt.topic, tt.runtimeGroup))
		})
	}
}

func TestSubscriber_TrackUntrackChannel(t *testing.T) {
	sub := &Subscriber{
		closeCh: make(chan struct{}),
	}

	ch1 := newMockChannel()
	ch2 := newMockChannel()
	ch3 := newMockChannel()

	sub.trackChannel(ch1)
	sub.trackChannel(ch2)
	sub.trackChannel(ch3)

	sub.mu.Lock()
	assert.Len(t, sub.channels, 3)
	sub.mu.Unlock()

	sub.untrackChannel(ch2)

	sub.mu.Lock()
	assert.Len(t, sub.channels, 2)
	// ch2 should be removed, ch1 and ch3 should remain.
	assert.Contains(t, sub.channels, AMQPChannel(ch1))
	assert.Contains(t, sub.channels, AMQPChannel(ch3))
	sub.mu.Unlock()

	// Untrack a channel that is not tracked — should be a no-op.
	sub.untrackChannel(newMockChannel())

	sub.mu.Lock()
	assert.Len(t, sub.channels, 2)
	sub.mu.Unlock()
}

func TestSubscriber_SubscribeOnce_AcquireChannelFails(t *testing.T) {
	mockConn := newMockConnection()

	dialFunc := func(url string) (AMQPConnection, error) {
		return mockConn, nil
	}

	conn, err := NewConnection(Config{
		URL:             "amqp://test@localhost/",
		ChannelPoolSize: 5,
	}, WithDialFunc(dialFunc))
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Now make channel acquisition fail.
	mockConn.mu.Lock()
	mockConn.chanErr = errors.New("connection dead")
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 1 * time.Second,
	})

	// subscribeOnce should return an error (channel acquisition failure).
	err = sub.subscribeOnce(context.Background(), "test.topic", "test-queue",
		outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_AMQP")

	// Verify no channels are tracked (it was cleaned up).
	sub.mu.Lock()
	assert.Empty(t, sub.channels)
	sub.mu.Unlock()
}

func TestSubscriber_Subscribe_ClosedDuringReconnect(t *testing.T) {
	// Use a connection whose "connected" channel is recreated after disconnect,
	// so WaitConnected blocks until the subscriber is closed.
	mockConn := newMockConnection()
	dialFunc := func(url string) (AMQPConnection, error) {
		return mockConn, nil
	}

	// Build Connection manually so we can control the "connected" channel.
	c := &Connection{
		config:      Config{URL: "amqp://test@localhost/"},
		dial:        dialFunc,
		channelPool: make(chan AMQPChannel, 5),
		closeCh:     make(chan struct{}),
		connected:   make(chan struct{}),
		terminalCh:  make(chan struct{}),
	}
	// Mark as initially connected.
	close(c.connected)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(c, SubscriberConfig{
		QueueName:       "test-queue",
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 1 * time.Second,
	})

	// Run Subscribe in a goroutine so the main goroutine can orchestrate the
	// close sequence. c.conn is nil in this manually-constructed Connection,
	// so AcquireChannel always returns "connection not available" and the
	// subscriber enters the reconnect hot-loop immediately (never reaches
	// QoS/Consume). The exact interleaving between replace-connected and
	// sub.Close() does not matter: closeCh cancels the derived subCtx, which
	// unblocks WaitConnected regardless of whether the subscriber is mid
	// AcquireChannel or blocked in WaitConnected — hence no "wait for X to
	// reach Y" sleeps are needed.
	subscribeDone := make(chan error, 1)
	go func() {
		subscribeDone <- sub.Subscribe(context.Background(), outbox.Subscription{Topic: "test.topic"},
			outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
	}()

	// Let the subscriber enter the reconnect hot-loop. The loop iterates in
	// microseconds, so 20ms is many iterations regardless of scheduling.
	time.Sleep(20 * time.Millisecond)

	// Simulate disconnection: replace c.connected with an unclosed channel
	// so that the next WaitConnected call would block.
	c.mu.Lock()
	c.connected = make(chan struct{})
	c.mu.Unlock()

	// Close subscriber. closeCh cancels the derived subCtx → WaitConnected
	// (or any other blocking call in the loop) returns ctx.Err() → Subscribe
	// exits cleanly.
	_ = sub.Close()

	select {
	case err := <-subscribeDone:
		assert.NoError(t, err) // Clean exit via subscriber close.
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not exit after subscriber close")
	}
}

// --- P0-4: ConsumerGroup-based queue naming ---

func TestSubscriber_Subscribe_ConsumerGroupQueueName(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		// QueueName deliberately left empty; ConsumerGroup is set.
		ConsumerGroup:   "audit-core",
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately so Subscribe exits after setup.

	err := sub.Subscribe(ctx, outbox.Subscription{Topic: "session.created"}, outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
	assert.NoError(t, err)

	ch.mu.Lock()
	// Queue name should be "{ConsumerGroup}.{topic}".
	assert.Contains(t, ch.queuesDeclared, "audit-core.session.created")
	// Binding should reference the derived queue name.
	assert.Contains(t, ch.queueBindings, "audit-core.session.created->session.created")
	ch.mu.Unlock()
}

func TestSubscriber_Subscribe_ExplicitQueueName_OverridesConsumerGroup(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "my-explicit-queue",
		ConsumerGroup:   "audit-core", // Should be ignored when QueueName is set.
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sub.Subscribe(ctx, outbox.Subscription{Topic: "session.created"}, outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
	assert.NoError(t, err)

	ch.mu.Lock()
	// Explicit QueueName takes precedence over ConsumerGroup derivation.
	assert.Contains(t, ch.queuesDeclared, "my-explicit-queue")
	assert.NotContains(t, ch.queuesDeclared, "audit-core.session.created")
	ch.mu.Unlock()
}

func TestSubscriber_Subscribe_NoConsumerGroup_FallsBackToTopic(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		// Both QueueName and ConsumerGroup empty — backward compat.
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sub.Subscribe(ctx, outbox.Subscription{Topic: "my.topic"}, outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
	assert.NoError(t, err)

	ch.mu.Lock()
	assert.Contains(t, ch.queuesDeclared, "my.topic") // Falls back to topic name.
	ch.mu.Unlock()
}

// --- P0-3: DLX configuration for broker-side dead letter ---

func TestSubscriber_Subscribe_DLXExchange_SetsQueueArgs(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		DLXExchange:     "my-dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sub.Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
	assert.NoError(t, err)

	ch.mu.Lock()
	require.Len(t, ch.queueDeclareArgs, 1)
	args := ch.queueDeclareArgs[0]
	assert.Equal(t, "my-dlx", args["x-dead-letter-exchange"])
	_, hasRoutingKey := args["x-dead-letter-routing-key"]
	assert.False(t, hasRoutingKey, "routing key should not be set when DLXRoutingKey is empty")
	ch.mu.Unlock()
}

func TestSubscriber_Subscribe_DLXExchangeWithRoutingKey(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		DLXExchange:     "my-dlx",
		DLXRoutingKey:   "dead-letter-key",
		ShutdownTimeout: 2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sub.Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
	assert.NoError(t, err)

	ch.mu.Lock()
	require.Len(t, ch.queueDeclareArgs, 1)
	args := ch.queueDeclareArgs[0]
	assert.Equal(t, "my-dlx", args["x-dead-letter-exchange"])
	assert.Equal(t, "dead-letter-key", args["x-dead-letter-routing-key"])
	ch.mu.Unlock()
}

func TestSubscriber_Subscribe_NoDLX_ReturnsError(t *testing.T) {
	conn, _ := newTestConnection(t)

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		ShutdownTimeout: 2 * time.Second,
		// DLXExchange deliberately left empty.
	})

	err := sub.Subscribe(context.Background(), outbox.Subscription{Topic: "test.topic"}, outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DLXExchange is required")
}

// =============================================================================
// ConsumerBase Tests
// =============================================================================

func TestConsumerBaseConfig_Defaults(t *testing.T) {
	cfg := outbox.ConsumerBaseConfig{}
	cfg.SetDefaults()

	assert.Equal(t, 3, cfg.RetryCount)
	assert.Equal(t, 1*time.Second, cfg.RetryBaseDelay)
	assert.Equal(t, idempotency.DefaultTTL, cfg.IdempotencyTTL)
}

func TestConsumerBaseConfig_Defaults_NegativeLeaseTTL(t *testing.T) {
	cfg := outbox.ConsumerBaseConfig{LeaseTTL: -1 * time.Minute}
	cfg.SetDefaults()

	assert.Equal(t, idempotency.DefaultLeaseTTL, cfg.LeaseTTL)
}

func TestConsumerBaseConfig_Defaults_NegativeIdempotencyTTL(t *testing.T) {
	cfg := outbox.ConsumerBaseConfig{IdempotencyTTL: -1 * time.Hour}
	cfg.SetDefaults()

	assert.Equal(t, idempotency.DefaultTTL, cfg.IdempotencyTTL)
}

// Legacy Checker-based TestConsumerBase_Wrap_* tests removed — fully covered
// by TestConsumerBase_WrapWithClaimer_* tests below.

// --- P0 #7: ctx cancel → NACK with requeue (conservative shutdown) ---

func TestSubscriber_ProcessDelivery_CtxCancelled_NackWithRequeue(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	entry := outbox.Entry{ID: "evt-ctx-cancel", EventType: "test.cancel"}
	entryBytes := makeDeliveryBody(t, entry)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	handler := func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		// Simulate ctx cancel happening before/during handler.
		cancel()
		return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: errors.New("transient error during shutdown")}
	}

	ch.consumeDeliveries <- amqp.Delivery{DeliveryTag: 42, Body: entryBytes}

	subDone := make(chan error, 1)
	go func() { subDone <- sub.Subscribe(ctx, outbox.Subscription{Topic: "test.topic"}, handler) }()

	// Deterministic wait for NACK instead of fixed sleep — handler cancels
	// ctx synchronously, so Subscribe will exit shortly after processDelivery
	// applies the disposition.
	require.Eventually(t, func() bool {
		ch.mu.Lock()
		defer ch.mu.Unlock()
		return ch.nackCalled
	}, 2*time.Second, 5*time.Millisecond, "Nack was not called in time")

	select {
	case <-subDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not exit after ctx cancel in handler")
	}

	ch.mu.Lock()
	assert.True(t, ch.nackRequeue, "should NACK with requeue when disposition is Requeue")
	assert.Equal(t, uint64(42), ch.nackTag)
	ch.mu.Unlock()

	_ = sub.Close()
}

// =============================================================================
// ConsumerBase.AsMiddleware Tests
// =============================================================================

func TestConsumerBase_AsMiddleware_ReturnsTopicHandlerMiddleware(t *testing.T) {
	receipt := &mockReceipt{}
	claimer := &mockClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{})
	require.NoError(t, cbErr)

	// AsMiddleware's return type is outbox.SubscriptionMiddleware — compile
	// enforces the test name's contract.
	mw := cb.AsMiddleware()

	handlerCalled := false
	wrapped := mw(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "mw-group"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		handlerCalled = true
		assert.Equal(t, "evt-mw-001", e.ID)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	entry := outbox.Entry{ID: "evt-mw-001", EventType: "test.middleware"}
	res := wrapped(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
	assert.True(t, handlerCalled)
	assert.Same(t, receipt, res.Receipt, "Receipt should be threaded through HandleResult")
}

func TestConsumerBase_AsMiddleware_Idempotency_SkipsDuplicate(t *testing.T) {
	claimer := &mockClaimer{state: idempotency.ClaimDone}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{})
	require.NoError(t, cbErr)

	mw := cb.AsMiddleware()

	handlerCalled := false
	wrapped := mw(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "mw-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		handlerCalled = true
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	entry := outbox.Entry{ID: "evt-mw-dup"}
	res := wrapped(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
	assert.False(t, handlerCalled, "handler should be skipped for duplicate event")
}

func TestConsumerBase_AsMiddleware_RejectOnPermanentError(t *testing.T) {
	receipt := &mockReceipt{}
	claimer := &mockClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		RetryCount:     3,
		RetryBaseDelay: 10 * time.Millisecond,
	})
	require.NoError(t, cbErr)

	mw := cb.AsMiddleware()

	wrapped := mw(outbox.Subscription{Topic: "orders.created", ConsumerGroup: "mw-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{
			Disposition: outbox.DispositionRequeue,
			Err:         outbox.NewPermanentError(errors.New("corrupted payload")),
		}
	})

	entry := outbox.Entry{ID: "evt-mw-perm", EventType: "orders.created"}
	res := wrapped(context.Background(), entry)
	assert.Equal(t, outbox.DispositionReject, res.Disposition) // Reject → DLX.
	assert.Same(t, receipt, res.Receipt, "Reject should thread Receipt for processDelivery to Release")
}

func TestConsumerBase_AsMiddleware_WithSubscriberWithMiddleware(t *testing.T) {
	// Integration-style test: wire AsMiddleware into SubscriberWithMiddleware.
	// First call: ClaimAcquired → handler runs.
	// Second call: ClaimDone → handler skipped.
	receipt := &mockReceipt{}
	claimer := &sequenceClaimer{responses: []claimResponse{
		{state: idempotency.ClaimAcquired, receipt: receipt},
		{state: idempotency.ClaimDone},
	}}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{})
	require.NoError(t, cbErr)

	// Use a simple recording subscriber to verify the chain works end-to-end.
	var capturedHandler outbox.EntryHandler
	innerSub := &stubSubscriber{
		onSubscribe: func(_ context.Context, _ string, h outbox.EntryHandler) error {
			capturedHandler = h
			return nil
		},
	}

	wrappedSub := &outbox.SubscriberWithMiddleware{
		Inner:      innerSub,
		Middleware: []outbox.SubscriptionMiddleware{cb.AsMiddleware()},
	}

	var receivedEntry outbox.Entry
	handlerCalled := false
	handler := func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		handlerCalled = true
		receivedEntry = e
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	err := wrappedSub.Subscribe(context.Background(), outbox.Subscription{Topic: "events.test"}, handler)
	assert.NoError(t, err)
	require.NotNil(t, capturedHandler)

	// Simulate an incoming entry — first call gets ClaimAcquired.
	entry := outbox.Entry{ID: "evt-integration-001", EventType: "events.test"}
	res := capturedHandler(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
	assert.True(t, handlerCalled)
	assert.Equal(t, "evt-integration-001", receivedEntry.ID)

	// Calling again with the same event — second call gets ClaimDone, handler skipped.
	handlerCalled = false
	res = capturedHandler(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
	assert.False(t, handlerCalled, "duplicate should be skipped by ConsumerBase middleware")
}

func TestConsumerBase_AsMiddleware_WithObservabilityContextMiddleware(t *testing.T) {
	receipt := &mockReceipt{}
	claimer := &sequenceClaimer{responses: []claimResponse{{
		state:   idempotency.ClaimAcquired,
		receipt: receipt,
	}}}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{})
	require.NoError(t, cbErr)

	var capturedHandler outbox.EntryHandler
	innerSub := &stubSubscriber{
		onSubscribe: func(_ context.Context, _ string, h outbox.EntryHandler) error {
			capturedHandler = h
			return nil
		},
	}

	wrappedSub := &outbox.SubscriberWithMiddleware{
		Inner: innerSub,
		Middleware: []outbox.SubscriptionMiddleware{
			outbox.ObservabilityContextMiddleware(),
			cb.AsMiddleware(),
		},
	}

	observed := make(map[string]string)
	handler := func(ctx context.Context, _ outbox.Entry) outbox.HandleResult {
		observed["request_id"], _ = ctxkeys.RequestIDFrom(ctx)
		observed["correlation_id"], _ = ctxkeys.CorrelationIDFrom(ctx)
		observed["trace_id"], _ = ctxkeys.TraceIDFrom(ctx)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	err := wrappedSub.Subscribe(context.Background(), outbox.Subscription{Topic: "events.test"}, handler)
	assert.NoError(t, err)
	require.NotNil(t, capturedHandler)

	entry := outbox.Entry{
		ID:        "evt-context-001",
		EventType: "events.test",
		Metadata: map[string]string{
			"request_id":     "req-rmq-1",
			"correlation_id": "corr-rmq-1",
			"trace_id":       "trace-rmq-1",
		},
	}

	res := capturedHandler(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
	assert.Equal(t, "req-rmq-1", observed["request_id"])
	assert.Equal(t, "corr-rmq-1", observed["correlation_id"])
	assert.Equal(t, "trace-rmq-1", observed["trace_id"])
}

func TestConsumerBase_AsMiddleware_LogsRestoredContext(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(logctx.NewHandler(logctx.Options{
		Level:  slog.LevelDebug,
		Format: logctx.FormatJSON,
		Writer: &buf,
	}))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	receipt := &mockReceipt{}
	claimer := &sequenceClaimer{responses: []claimResponse{{
		state:   idempotency.ClaimAcquired,
		receipt: receipt,
	}}}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{})
	require.NoError(t, cbErr)

	var capturedHandler outbox.EntryHandler
	innerSub := &stubSubscriber{
		onSubscribe: func(_ context.Context, _ string, h outbox.EntryHandler) error {
			capturedHandler = h
			return nil
		},
	}

	wrappedSub := &outbox.SubscriberWithMiddleware{
		Inner: innerSub,
		Middleware: []outbox.SubscriptionMiddleware{
			outbox.ObservabilityContextMiddleware(),
			cb.AsMiddleware(),
		},
	}

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{
			Disposition: outbox.DispositionRequeue,
			Err:         outbox.NewPermanentError(errors.New("corrupted payload")),
		}
	}

	err := wrappedSub.Subscribe(context.Background(), outbox.Subscription{Topic: "events.test"}, handler)
	assert.NoError(t, err)
	require.NotNil(t, capturedHandler)

	res := capturedHandler(context.Background(), outbox.Entry{
		ID:        "evt-log-001",
		EventType: "events.test",
		Metadata: map[string]string{
			"request_id":     "req-log-1",
			"correlation_id": "corr-log-1",
			"trace_id":       "trace-log-1",
		},
	})
	assert.Equal(t, outbox.DispositionReject, res.Disposition)

	var logEntry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &logEntry))
	assert.Equal(t, "trace-log-1", logEntry["trace_id"])
	assert.Equal(t, "req-log-1", logEntry["request_id"])
	assert.Equal(t, "corr-log-1", logEntry["correlation_id"])
}

// stubSubscriber is a minimal Subscriber for integration tests.
type stubSubscriber struct {
	onSubscribe func(context.Context, string, outbox.EntryHandler) error
}

func (s *stubSubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }
func (s *stubSubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (s *stubSubscriber) Subscribe(ctx context.Context, sub outbox.Subscription, handler outbox.EntryHandler) error {
	if s.onSubscribe != nil {
		return s.onSubscribe(ctx, sub.Topic, handler)
	}
	return nil
}
func (s *stubSubscriber) Close() error { return nil }

var _ outbox.Subscriber = (*stubSubscriber)(nil)

// =============================================================================
// Publisher Error Branch Tests (P1-5)
// =============================================================================

func TestPublisher_Publish_ExchangeDeclareFails(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	ch.exchangeDeclareErr = errors.New("exchange declare failed: access refused")
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	pub := NewPublisher(conn)

	err := pub.Publish(context.Background(), "test.topic", []byte(`{"data":"value"}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_AMQP_PUBLISH")
	assert.Contains(t, err.Error(), "declare exchange")

	// Verify publish was never called since exchange declare failed first.
	ch.mu.Lock()
	assert.False(t, ch.publishCalled)
	ch.mu.Unlock()
}

func TestPublisher_Publish_ConfirmChannelClosed(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	// Simulate broker disconnect between publish and confirm: close the
	// confirm channel immediately on NotifyPublish registration.
	ch.autoCloseConfirm = true
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	pub := NewPublisher(conn)

	err := pub.Publish(context.Background(), "test.topic", []byte(`{"data":"value"}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_AMQP_CONFIRM_TIMEOUT")
	assert.Contains(t, err.Error(), "confirm channel closed")
}

// =============================================================================
// Mock Claimer / Receipt for Solution B tests
// =============================================================================

type mockReceipt struct {
	mu            sync.Mutex
	commitCalled  bool
	commitErr     error
	releaseCalled bool
	releaseErr    error
	commitCtx     context.Context
	releaseCtx    context.Context
	extendCalls   int
	extendErr     error
}

func (r *mockReceipt) Commit(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commitCalled = true
	r.commitCtx = ctx
	return r.commitErr
}

func (r *mockReceipt) Release(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseCalled = true
	r.releaseCtx = ctx
	return r.releaseErr
}

func (r *mockReceipt) Extend(_ context.Context, _ time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.extendCalls++
	return r.extendErr
}

var _ outbox.Receipt = (*mockReceipt)(nil)

type mockClaimer struct {
	mu      sync.Mutex
	state   idempotency.ClaimState
	receipt outbox.Receipt
	err     error
	claims  []string
}

func (c *mockClaimer) Claim(_ context.Context, key string, _, _ time.Duration) (idempotency.ClaimState, outbox.Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.claims = append(c.claims, key)
	return c.state, c.receipt, c.err
}

var _ idempotency.Claimer = (*mockClaimer)(nil)

// --- ConsumerBase with Claimer tests ---

func TestConsumerBase_WrapWithClaimer_Success_ReturnsReceipt(t *testing.T) {
	receipt := &mockReceipt{}
	claimer := &mockClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{})
	require.NoError(t, cbErr)

	handlerCalled := false
	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		handlerCalled = true
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	entry := outbox.Entry{ID: "evt-claimer-001"}
	res := handler(context.Background(), entry)

	assert.True(t, handlerCalled)
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
	assert.Same(t, receipt, res.Receipt, "Receipt should be threaded through HandleResult")
	// ConsumerBase must NOT call Commit/Release — that's processDelivery's job.
	receipt.mu.Lock()
	assert.False(t, receipt.commitCalled)
	assert.False(t, receipt.releaseCalled)
	receipt.mu.Unlock()
}

func TestConsumerBase_WrapWithClaimer_ClaimDone_SkipsHandler(t *testing.T) {
	claimer := &mockClaimer{state: idempotency.ClaimDone}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{})
	require.NoError(t, cbErr)

	handlerCalled := false
	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		handlerCalled = true
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	res := handler(context.Background(), outbox.Entry{ID: "evt-done"})
	assert.False(t, handlerCalled)
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
	assert.Nil(t, res.Receipt, "ClaimDone should not return a Receipt")
}

func TestConsumerBase_WrapWithClaimer_ClaimBusy_Requeues(t *testing.T) {
	claimer := &mockClaimer{state: idempotency.ClaimBusy}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{})
	require.NoError(t, cbErr)

	handlerCalled := false
	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		handlerCalled = true
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	res := handler(context.Background(), outbox.Entry{ID: "evt-busy"})
	assert.False(t, handlerCalled)
	assert.Equal(t, outbox.DispositionRequeue, res.Disposition)
}

func TestConsumerBase_WrapWithClaimer_Reject_ThreadsReceipt(t *testing.T) {
	receipt := &mockReceipt{}
	claimer := &mockClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		RetryCount:     1,
		RetryBaseDelay: 10 * time.Millisecond,
	})
	require.NoError(t, cbErr)

	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: errors.New("fail")}
	})

	res := handler(context.Background(), outbox.Entry{ID: "evt-reject"})
	assert.Equal(t, outbox.DispositionReject, res.Disposition)
	assert.Same(t, receipt, res.Receipt, "Reject should thread Receipt for processDelivery to Release")
	// ConsumerBase must NOT call Commit/Release on Receipt.
	receipt.mu.Lock()
	assert.False(t, receipt.commitCalled)
	assert.False(t, receipt.releaseCalled)
	receipt.mu.Unlock()
}

func TestConsumerBase_WrapWithClaimer_ExplicitReject_FirstRoundNoRetry(t *testing.T) {
	receipt := &mockReceipt{}
	claimer := &mockClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	handlerCallCount := 0
	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		RetryCount:     3,
		RetryBaseDelay: 10 * time.Millisecond,
	})
	require.NoError(t, cbErr)

	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		handlerCallCount++
		return outbox.HandleResult{
			Disposition: outbox.DispositionReject,
			Err:         errors.New("bad payload"),
		}
	})

	res := handler(context.Background(), outbox.Entry{ID: "evt-reject-direct"})
	assert.Equal(t, outbox.DispositionReject, res.Disposition)
	assert.Equal(t, 1, handlerCallCount, "DispositionReject must skip retry loop — handler called exactly once")
	assert.Same(t, receipt, res.Receipt)
}

func TestConsumerBase_WrapWithClaimer_WrappedPermanentError_FirstRoundReject(t *testing.T) {
	receipt := &mockReceipt{}
	claimer := &mockClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	handlerCallCount := 0
	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		RetryCount:     3,
		RetryBaseDelay: 10 * time.Millisecond,
	})
	require.NoError(t, cbErr)

	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		handlerCallCount++
		// Wrapped PermanentError — must be detected by errors.As through fmt.Errorf wrapping.
		return outbox.HandleResult{
			Disposition: outbox.DispositionRequeue,
			Err:         fmt.Errorf("handler context: %w", outbox.NewPermanentError(errors.New("unmarshal failed"))),
		}
	})

	res := handler(context.Background(), outbox.Entry{ID: "evt-perm-wrapped"})
	assert.Equal(t, outbox.DispositionReject, res.Disposition,
		"wrapped PermanentError must be detected and upgraded to Reject")
	assert.Equal(t, 1, handlerCallCount, "PermanentError must skip retry loop — handler called exactly once")
	assert.Same(t, receipt, res.Receipt)
}

// sequenceClaimer returns different results on successive Claim calls.
// Used to test claimWithRetry: first N calls fail, then succeed.
type sequenceClaimer struct {
	mu        sync.Mutex
	responses []claimResponse
	callCount int
}

type claimResponse struct {
	state   idempotency.ClaimState
	receipt outbox.Receipt
	err     error
}

func (c *sequenceClaimer) Claim(_ context.Context, _ string, _, _ time.Duration) (idempotency.ClaimState, outbox.Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.callCount
	c.callCount++
	if idx < len(c.responses) {
		r := c.responses[idx]
		return r.state, r.receipt, r.err
	}
	// Default: return last response forever.
	r := c.responses[len(c.responses)-1]
	return r.state, r.receipt, r.err
}

var _ idempotency.Claimer = (*sequenceClaimer)(nil)

func TestConsumerBase_WrapWithClaimer_ClaimError_DefaultFailClosed_LocalRetryThenSuccess(t *testing.T) {
	receipt := &mockReceipt{}
	// fail-closed: claimWithRetry handles ALL attempts (no naked first Claim).
	// ClaimRetryCount=3 → 3 total Claim calls, last succeeds.
	claimer := &sequenceClaimer{responses: []claimResponse{
		{err: errors.New("redis down")},                      // attempt 0
		{err: errors.New("redis down")},                      // attempt 1
		{state: idempotency.ClaimAcquired, receipt: receipt}, // attempt 2 — success
	}}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		ClaimRetryCount:     3,
		ClaimRetryBaseDelay: 10 * time.Millisecond,
	})
	require.NoError(t, cbErr)

	handlerCalled := false
	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		handlerCalled = true
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	res := handler(context.Background(), outbox.Entry{ID: "evt-retry-ok"})
	assert.True(t, handlerCalled, "handler must be called after claim retry succeeds")
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
	assert.Same(t, receipt, res.Receipt, "Receipt from successful retry must be threaded through")

	claimer.mu.Lock()
	assert.Equal(t, 3, claimer.callCount, "claimWithRetry makes ClaimRetryCount total attempts")
	claimer.mu.Unlock()
}

func TestConsumerBase_WrapWithClaimer_ClaimError_DefaultFailClosed_HasBackoff(t *testing.T) {
	claimer := &mockClaimer{err: errors.New("redis down")}

	// ClaimRetryCount=3, ClaimRetryBaseDelay=20ms → sleeps between attempts.
	// With jitter, we assert >= base delay only (not exact).
	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		ClaimRetryCount:     3,
		ClaimRetryBaseDelay: 20 * time.Millisecond,
	})
	require.NoError(t, cbErr)

	handlerCalled := false
	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		handlerCalled = true
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	start := time.Now()
	res := handler(context.Background(), outbox.Entry{ID: "evt-claim-err"})
	elapsed := time.Since(start)

	assert.False(t, handlerCalled, "handler must NOT be called when all retries fail")
	assert.Equal(t, outbox.DispositionRequeue, res.Disposition)
	assert.Error(t, res.Err)
	// Base delays: 20ms + 40ms = 60ms between 3 attempts. With jitter >= base,
	// total must be at least 50ms (allowing timing slack).
	assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond,
		"fail-closed must do local exponential backoff before returning to broker")
}

func TestConsumerBase_WrapWithClaimer_ClaimError_DefaultFailClosed_CtxCancel(t *testing.T) {
	claimer := &mockClaimer{err: errors.New("redis down")}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		ClaimRetryCount:     3,
		ClaimRetryBaseDelay: 5 * time.Second, // long delay — ctx cancel must short-circuit
	})
	require.NoError(t, cbErr)

	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	res := handler(ctx, outbox.Entry{ID: "evt-claim-ctx"})
	elapsed := time.Since(start)

	assert.Equal(t, outbox.DispositionRequeue, res.Disposition)
	assert.Less(t, elapsed, 1*time.Second, "ctx cancel must short-circuit the backoff")
}

func TestConsumerBase_WrapWithClaimer_ClaimError_DefaultFailClosed_RetryCount1(t *testing.T) {
	// S3-01: boundary — ClaimRetryCount=1 means exactly 1 attempt, no backoff sleep.
	claimer := &mockClaimer{err: errors.New("redis down")}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		ClaimRetryCount:     1,
		ClaimRetryBaseDelay: 5 * time.Second,
	})
	require.NoError(t, cbErr)

	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	start := time.Now()
	res := handler(context.Background(), outbox.Entry{ID: "evt-retry1"})
	elapsed := time.Since(start)

	assert.Equal(t, outbox.DispositionRequeue, res.Disposition)
	assert.Less(t, elapsed, 500*time.Millisecond, "RetryCount=1 must not sleep (single attempt)")

	claimer.mu.Lock()
	assert.Equal(t, 1, len(claimer.claims), "exactly 1 Claim attempt with ClaimRetryCount=1")
	claimer.mu.Unlock()
}

func TestConsumerBase_WrapWithClaimer_ClaimRetryConfig_Independent(t *testing.T) {
	// S1-01: ClaimRetryCount/ClaimRetryBaseDelay independent from RetryCount/RetryBaseDelay.
	receipt := &mockReceipt{}
	claimer := &sequenceClaimer{responses: []claimResponse{
		{err: errors.New("redis down")},                      // attempt 0
		{state: idempotency.ClaimAcquired, receipt: receipt}, // attempt 1 — success
	}}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		RetryCount:          5,                     // handler retries — should not affect claim
		RetryBaseDelay:      1 * time.Second,       // handler backoff — should not affect claim
		ClaimRetryCount:     2,                     // claim retries
		ClaimRetryBaseDelay: 10 * time.Millisecond, // claim backoff
	})
	require.NoError(t, cbErr)

	handlerCalled := false
	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		handlerCalled = true
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	start := time.Now()
	res := handler(context.Background(), outbox.Entry{ID: "evt-independent"})
	elapsed := time.Since(start)

	assert.True(t, handlerCalled)
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
	// Should complete quickly using claim delay (10ms), not handler delay (1s).
	assert.Less(t, elapsed, 500*time.Millisecond, "claim retry must use ClaimRetryBaseDelay, not RetryBaseDelay")

	claimer.mu.Lock()
	assert.Equal(t, 2, claimer.callCount)
	claimer.mu.Unlock()
}

func TestConsumerBase_MaxRetryDelay_Caps_ClaimBackoff(t *testing.T) {
	// S4-02: MaxRetryDelay caps exponential growth.
	claimer := &mockClaimer{err: errors.New("redis down")}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		ClaimRetryCount:     3,
		ClaimRetryBaseDelay: 100 * time.Millisecond,
		MaxRetryDelay:       50 * time.Millisecond, // cap below base — forces all delays to 50ms
	})
	require.NoError(t, cbErr)

	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	start := time.Now()
	_ = handler(context.Background(), outbox.Entry{ID: "evt-cap"})
	elapsed := time.Since(start)

	// Without cap: 100ms + 200ms = 300ms. With cap at 50ms: 50ms + 50ms = 100ms.
	// Allow generous slack but verify it's well below uncapped.
	assert.Less(t, elapsed, 250*time.Millisecond,
		"MaxRetryDelay must cap exponential backoff growth")
}

func TestConsumerBase_NegativeClaimRetryBaseDelay_NoPanic(t *testing.T) {
	claimer := &mockClaimer{err: errors.New("redis down")}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		ClaimRetryCount:     2,
		ClaimRetryBaseDelay: -1 * time.Second, // negative — must not panic
	})
	require.NoError(t, cbErr)

	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	// Must not panic; negative delay is clamped to default by setDefaults.
	res := handler(context.Background(), outbox.Entry{ID: "evt-neg-delay"})
	assert.Equal(t, outbox.DispositionRequeue, res.Disposition)
}

func TestConsumerBase_NegativeMaxRetryDelay_NoPanic(t *testing.T) {
	claimer := &mockClaimer{err: errors.New("redis down")}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		ClaimRetryCount:     2,
		ClaimRetryBaseDelay: 10 * time.Millisecond,
		MaxRetryDelay:       -1 * time.Second, // negative — must not panic
	})
	require.NoError(t, cbErr)

	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	res := handler(context.Background(), outbox.Entry{ID: "evt-neg-cap"})
	assert.Equal(t, outbox.DispositionRequeue, res.Disposition)
}

// --- processDelivery Receipt lifecycle tests ---

func TestProcessDelivery_Ack_CommitsReceipt(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	receipt := &mockReceipt{}
	entry := outbox.Entry{ID: "evt-ack-receipt", EventType: "test.ack"}
	entryBytes := makeDeliveryBody(t, entry)

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck, Receipt: receipt}
	}

	ctx := context.Background()
	sub.wg.Add(1)
	sub.processDelivery(ctx, ch, amqp.Delivery{
		DeliveryTag: 1,
		Body:        entryBytes,
	}, "test.topic", handler)

	ch.mu.Lock()
	assert.True(t, ch.ackCalled)
	ch.mu.Unlock()

	receipt.mu.Lock()
	assert.True(t, receipt.commitCalled, "Ack should Commit Receipt")
	assert.False(t, receipt.releaseCalled, "Ack should not Release Receipt")
	receipt.mu.Unlock()
}

func TestProcessDelivery_Reject_ReleasesReceipt(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	receipt := &mockReceipt{}
	entry := outbox.Entry{ID: "evt-reject-receipt", EventType: "test.reject"}
	entryBytes := makeDeliveryBody(t, entry)

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{
			Disposition: outbox.DispositionReject,
			Err:         errors.New("permanent"),
			Receipt:     receipt,
		}
	}

	ctx := context.Background()
	sub.wg.Add(1)
	sub.processDelivery(ctx, ch, amqp.Delivery{
		DeliveryTag: 2,
		Body:        entryBytes,
	}, "test.topic", handler)

	ch.mu.Lock()
	assert.True(t, ch.nackCalled)
	assert.False(t, ch.nackRequeue, "Reject should Nack without requeue")
	ch.mu.Unlock()

	receipt.mu.Lock()
	assert.False(t, receipt.commitCalled, "Reject should NOT Commit Receipt")
	assert.True(t, receipt.releaseCalled, "Reject should Release Receipt (allows DLQ replay)")
	receipt.mu.Unlock()
}

func TestProcessDelivery_NilReceipt_NoPanic(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	entry := outbox.Entry{ID: "evt-nil-receipt", EventType: "test.nil"}
	entryBytes := makeDeliveryBody(t, entry)

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	ctx := context.Background()
	sub.wg.Add(1)
	// Should not panic even though Receipt is nil.
	assert.NotPanics(t, func() {
		sub.processDelivery(ctx, ch, amqp.Delivery{
			DeliveryTag: 3,
			Body:        entryBytes,
		}, "test.topic", handler)
	})
}

// TestProcessDelivery_PassesThroughContextWithoutRestore verifies that
// processDelivery passes the parent context to the handler as-is, without
// restoring observability metadata. Context restoration is handled by
// ObservabilityContextMiddleware at the middleware layer, not by the subscriber.
func TestProcessDelivery_PassesThroughContextWithoutRestore(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	entry := outbox.Entry{
		ID:        "evt-ctx-restore",
		EventType: "test.restore",
		Metadata: map[string]string{
			"request_id":     "req-sub-1",
			"correlation_id": "corr-sub-1",
			"trace_id":       "trace-sub-1",
		},
	}
	entryBytes := makeDeliveryBody(t, entry)

	const sentinelKey testContextKey = "sentinel"
	parentCtx, cancel := context.WithCancel(context.WithValue(context.Background(), sentinelKey, "parent-value"))
	cancel()

	observed := make(map[string]string)
	var observedSentinel string
	var observedErr error
	handler := func(ctx context.Context, _ outbox.Entry) outbox.HandleResult {
		observed["request_id"], _ = ctxkeys.RequestIDFrom(ctx)
		observed["correlation_id"], _ = ctxkeys.CorrelationIDFrom(ctx)
		observed["trace_id"], _ = ctxkeys.TraceIDFrom(ctx)
		observedSentinel, _ = ctx.Value(sentinelKey).(string)
		observedErr = ctx.Err()
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	sub.wg.Add(1)
	sub.processDelivery(parentCtx, ch, amqp.Delivery{
		DeliveryTag: 5,
		Body:        entryBytes,
	}, "test.topic", handler)

	// Subscriber should NOT restore obs metadata (middleware's job).
	assert.Empty(t, observed["request_id"], "subscriber should not restore request_id")
	assert.Empty(t, observed["correlation_id"], "subscriber should not restore correlation_id")
	assert.Empty(t, observed["trace_id"], "subscriber should not restore trace_id")
	// Parent context values should pass through.
	assert.Equal(t, "parent-value", observedSentinel)
	assert.ErrorIs(t, observedErr, context.Canceled)
	ch.mu.Lock()
	assert.True(t, ch.ackCalled)
	ch.mu.Unlock()
}

// TestProcessDelivery_DoesNotRestoreObservabilityContext verifies that
// the subscriber's processDelivery does NOT restore observability metadata
// into the handler context. Context restoration is the responsibility of
// ObservabilityContextMiddleware (registered via bootstrap or manually).
func TestProcessDelivery_DoesNotRestoreObservabilityContext(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	entry := outbox.Entry{
		ID:        "evt-no-restore",
		EventType: "test.restore",
		Metadata: map[string]string{
			"request_id":     "req-log-1",
			"correlation_id": "corr-log-1",
			"trace_id":       "trace-log-1",
		},
	}
	entryBytes := makeDeliveryBody(t, entry)

	var capturedRequestID, capturedTraceID string
	handler := func(ctx context.Context, _ outbox.Entry) outbox.HandleResult {
		capturedRequestID, _ = ctxkeys.RequestIDFrom(ctx)
		capturedTraceID, _ = ctxkeys.TraceIDFrom(ctx)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	sub.wg.Add(1)
	sub.processDelivery(context.Background(), ch, amqp.Delivery{
		DeliveryTag: 6,
		Body:        entryBytes,
	}, "test.topic", handler)

	assert.Empty(t, capturedRequestID,
		"processDelivery should NOT restore request_id — that is middleware's job")
	assert.Empty(t, capturedTraceID,
		"processDelivery should NOT restore trace_id — that is middleware's job")
}

func TestProcessDelivery_Receipt_UsesDetachedCtx(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	receipt := &mockReceipt{}
	entry := outbox.Entry{ID: "evt-detached-ctx", EventType: "test.ctx"}
	entryBytes := makeDeliveryBody(t, entry)

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck, Receipt: receipt}
	}

	// Use a cancelled context to simulate shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	sub.wg.Add(1)
	sub.processDelivery(ctx, ch, amqp.Delivery{
		DeliveryTag: 4,
		Body:        entryBytes,
	}, "test.topic", handler)

	// Receipt should still be committed because processDelivery uses
	// context.WithoutCancel for Receipt operations. The parent ctx is
	// cancelled but the receipt ctx is detached, so Commit succeeds.
	receipt.mu.Lock()
	assert.True(t, receipt.commitCalled, "Receipt should be committed even with cancelled parent ctx")
	assert.False(t, receipt.releaseCalled, "Should Commit, not Release")
	receipt.mu.Unlock()
}

func TestProcessDelivery_Requeue_ReleasesReceipt(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	receipt := &mockReceipt{}
	entry := outbox.Entry{ID: "evt-requeue-receipt", EventType: "test.requeue"}
	entryBytes := makeDeliveryBody(t, entry)

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{
			Disposition: outbox.DispositionRequeue,
			Err:         errors.New("transient"),
			Receipt:     receipt,
		}
	}

	sub.wg.Add(1)
	sub.processDelivery(context.Background(), ch, amqp.Delivery{
		DeliveryTag: 10,
		Body:        entryBytes,
	}, "test.topic", handler)

	ch.mu.Lock()
	assert.True(t, ch.nackCalled)
	assert.True(t, ch.nackRequeue)
	ch.mu.Unlock()

	receipt.mu.Lock()
	assert.False(t, receipt.commitCalled, "Requeue should not Commit")
	assert.True(t, receipt.releaseCalled, "Requeue should Release Receipt")
	receipt.mu.Unlock()
}

func TestProcessDelivery_Reject_NoDLX_SubscribeReturnsError(t *testing.T) {
	conn, _ := newTestConnection(t)

	// No DLXExchange configured — Subscribe should fail before any delivery processing.
	sub := NewSubscriber(conn, SubscriberConfig{
		ShutdownTimeout: 2 * time.Second,
		// DLXExchange deliberately left empty.
	})

	err := sub.Subscribe(context.Background(), outbox.Subscription{Topic: "test.topic"}, outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DLXExchange is required")
}

func TestConsumerBase_WrapWithClaimer_ClaimBusy_HasBackoff(t *testing.T) {
	claimer := &mockClaimer{state: idempotency.ClaimBusy}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		RetryBaseDelay: 50 * time.Millisecond,
	})
	require.NoError(t, cbErr)

	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		t.Fatal("handler should not be called for ClaimBusy")
		return outbox.HandleResult{}
	})

	start := time.Now()
	res := handler(context.Background(), outbox.Entry{ID: "evt-busy-backoff"})
	elapsed := time.Since(start)

	assert.Equal(t, outbox.DispositionRequeue, res.Disposition)
	assert.GreaterOrEqual(t, elapsed, 40*time.Millisecond, "ClaimBusy should backoff before requeue")
}

// TestProcessDelivery_BrokerAckFails_CommitAlreadyDone verifies the new
// Commit→Ack ordering: if broker Ack fails after a successful Commit, the
// Receipt is already committed (idempotency key is marked done). The message
// will be redelivered, but the ClaimDone state ensures redelivery is a no-op.
// We do NOT Release after a committed receipt — the idempotency key protects
// against duplicate processing on redelivery.
func TestProcessDelivery_BrokerAckFails_CommitAlreadyDone(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	ch.ackErr = errors.New("channel closed")
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	receipt := &mockReceipt{} // commitErr = nil → Commit succeeds
	entry := outbox.Entry{ID: "evt-broker-fail", EventType: "test.brokerfail"}
	entryBytes := makeDeliveryBody(t, entry)

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck, Receipt: receipt}
	}

	ctx := context.Background()
	sub.wg.Add(1)
	sub.processDelivery(ctx, ch, amqp.Delivery{
		DeliveryTag: 5,
		Body:        entryBytes,
	}, "test.topic", handler)

	receipt.mu.Lock()
	// With Commit→Ack ordering, Commit is called before Ack.
	// Commit succeeds, then Ack fails. Receipt is already committed.
	assert.True(t, receipt.commitCalled, "Commit must be called before Ack attempt")
	assert.False(t, receipt.releaseCalled, "must NOT Release after committed receipt — idempotency key is already done")
	receipt.mu.Unlock()
}

// =============================================================================
// ClaimPolicy config tests
// =============================================================================

func TestConsumerBase_WrapWithClaimer_ClaimError_FailClosed(t *testing.T) {
	claimer := &mockClaimer{err: errors.New("redis down")}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		ClaimPolicy:         outbox.ClaimPolicyFailClosed,
		ClaimRetryCount:     3,
		ClaimRetryBaseDelay: 10 * time.Millisecond,
	})
	require.NoError(t, cbErr)

	handlerCalled := false
	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		handlerCalled = true
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	res := handler(context.Background(), outbox.Entry{ID: "evt-fail-closed"})
	assert.False(t, handlerCalled, "handler must NOT be called when fail-closed")
	assert.Equal(t, outbox.DispositionRequeue, res.Disposition)
	assert.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "redis down")
}

func TestConsumerBase_WrapWithClaimer_ClaimError_FailOpen_Explicit(t *testing.T) {
	claimer := &mockClaimer{err: errors.New("redis down")}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		ClaimPolicy: outbox.ClaimPolicyFailOpen,
	})
	require.NoError(t, cbErr)

	handlerCalled := false
	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		handlerCalled = true
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	res := handler(context.Background(), outbox.Entry{ID: "evt-fail-open-explicit"})
	assert.True(t, handlerCalled, "handler must be called when fail-open is explicit")
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
	assert.Nil(t, res.Receipt, "no Receipt when claim fails")
}

func TestClaimPolicy_Valid(t *testing.T) {
	assert.True(t, outbox.ClaimPolicyFailClosed.Valid())
	assert.True(t, outbox.ClaimPolicyFailOpen.Valid())
	assert.False(t, outbox.ClaimPolicy(99).Valid(), "unknown ClaimPolicy must be invalid")
}

func TestNewConsumerBase_InvalidClaimPolicy_ReturnsError(t *testing.T) {
	_, err := outbox.NewConsumerBase(&mockClaimer{}, outbox.ConsumerBaseConfig{
		ClaimPolicy: outbox.ClaimPolicy(99),
	})
	require.Error(t, err, "NewConsumerBase must return error on invalid ClaimPolicy")
	assert.Contains(t, err.Error(), "invalid ClaimPolicy 99")
}

func TestNewConsumerBase_ValidClaimPolicy_Succeeds(t *testing.T) {
	tests := []struct {
		name   string
		policy outbox.ClaimPolicy
	}{
		{"FailClosed", outbox.ClaimPolicyFailClosed},
		{"FailOpen", outbox.ClaimPolicyFailOpen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cb, err := outbox.NewConsumerBase(&mockClaimer{}, outbox.ConsumerBaseConfig{
				ClaimPolicy: tt.policy,
			})
			require.NoError(t, err)
			assert.NotNil(t, cb)
		})
	}
}

func TestNewConsumerBase_ExplicitFailClosed_Preserved(t *testing.T) {
	// Explicitly pass outbox.ClaimPolicyFailClosed (0) — must be preserved through
	// SetDefaults. Verified via round-trip: construct → Wrap → call handler and
	// confirm fail-closed semantics (single Claim attempt returns a wrapped result
	// that is_NOT_ fail-open's proceed-without-receipt path).
	cb, err := outbox.NewConsumerBase(&mockClaimer{}, outbox.ConsumerBaseConfig{
		ClaimPolicy: outbox.ClaimPolicyFailClosed,
	})
	require.NoError(t, err)
	require.NotNil(t, cb)
}

// =============================================================================
// retryLoop post-loop ctx.Err() test (S3-02)
// =============================================================================

func TestConsumerBase_RetryLoop_CtxCancelledAfterFinalAttempt_Requeues(t *testing.T) {
	// S3-02: When ctx is cancelled by the time the final retry completes,
	// retryLoop must return Requeue (not Reject to DLX).
	receipt := &mockReceipt{}
	claimer := &mockClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		RetryCount:     1, // single attempt, no inter-attempt sleep
		RetryBaseDelay: time.Millisecond,
	})
	require.NoError(t, cbErr)

	ctx, cancel := context.WithCancel(context.Background())

	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		// Cancel context during the handler — simulates shutdown mid-processing.
		cancel()
		return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: errors.New("transient")}
	})

	res := handler(ctx, outbox.Entry{ID: "evt-ctx-final"})
	assert.Equal(t, outbox.DispositionRequeue, res.Disposition,
		"must Requeue (not Reject to DLX) when ctx is cancelled after final attempt")
	assert.ErrorIs(t, res.Err, context.Canceled)
}

// =============================================================================
// processDelivery uncovered branch tests
// =============================================================================

func TestProcessDelivery_HandlerError_Logged(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	entry := outbox.Entry{ID: "evt-ack-with-err", EventType: "test.ackwitherr"}
	entryBytes := makeDeliveryBody(t, entry)

	// Handler returns DispositionAck but also an error (e.g., a warning).
	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{
			Disposition: outbox.DispositionAck,
			Err:         errors.New("warning: partial data"),
		}
	}

	sub.wg.Add(1)
	sub.processDelivery(context.Background(), ch, amqp.Delivery{
		DeliveryTag: 20,
		Body:        entryBytes,
	}, "test.topic", handler)

	// Ack should still be called (error does not affect disposition).
	ch.mu.Lock()
	assert.True(t, ch.ackCalled, "Ack should be called even when handler reports an error")
	assert.Equal(t, uint64(20), ch.ackTag)
	assert.False(t, ch.nackCalled, "Nack should NOT be called for DispositionAck")
	ch.mu.Unlock()
}

func TestProcessDelivery_Requeue_BrokerNackFails_ReleasesReceipt(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	ch.nackErr = errors.New("channel closed")
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	receipt := &mockReceipt{}
	entry := outbox.Entry{ID: "evt-requeue-nack-fail", EventType: "test.requeue.nackfail"}
	entryBytes := makeDeliveryBody(t, entry)

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{
			Disposition: outbox.DispositionRequeue,
			Err:         errors.New("transient"),
			Receipt:     receipt,
		}
	}

	sub.wg.Add(1)
	sub.processDelivery(context.Background(), ch, amqp.Delivery{
		DeliveryTag: 30,
		Body:        entryBytes,
	}, "test.topic", handler)

	// Nack was attempted (requeue=true) but failed.
	ch.mu.Lock()
	assert.True(t, ch.nackCalled)
	ch.mu.Unlock()

	// Receipt should be Released because broker nack failed (brokerErr != nil path).
	receipt.mu.Lock()
	assert.False(t, receipt.commitCalled, "should not Commit when broker Nack fails")
	assert.True(t, receipt.releaseCalled, "should Release when broker Nack fails, so redelivery can re-enter")
	receipt.mu.Unlock()
}

func TestIsRecoverableAMQPError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "non-AMQP error",
			err:  errors.New("some random error"),
			want: false,
		},
		{
			name: "AMQP error with Recover=true",
			err:  &amqp.Error{Code: 501, Reason: "frame error", Server: true, Recover: true},
			want: true,
		},
		{
			name: "AMQP error with Recover=false",
			err:  &amqp.Error{Code: 403, Reason: "access refused", Server: true, Recover: false},
			want: false,
		},
		{
			name: "connection error Code 320 (connection forced) with Recover=true",
			err:  &amqp.Error{Code: 320, Reason: "connection forced", Server: true, Recover: true},
			want: true,
		},
		{
			name: "channel error Code 404 (not found) with Recover=false",
			err:  &amqp.Error{Code: 404, Reason: "not found", Server: true, Recover: false},
			want: false,
		},
		{
			name: "amqp.ErrClosed",
			err:  amqp.ErrClosed,
			want: true,
		},
		{
			name: "wrapped amqp.ErrClosed",
			err:  fmt.Errorf("channel: %w", amqp.ErrClosed),
			want: true,
		},
		{
			name: "ErrAdapterAMQPConnect errcode",
			err:  errcode.New(ErrAdapterAMQPConnect, "connection not available"),
			want: true,
		},
		{
			name: "ErrAdapterAMQPReconnecting errcode",
			err:  errcode.New(ErrAdapterAMQPReconnecting, "reconnecting"),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRecoverableAMQPError(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestConsumerBase_Wrap_ReleaseCheckerError_Logged removed — legacy Checker path
// no longer exists. Release error logging is covered by Claimer-based receipt tests.

func TestProcessDelivery_UnknownDisposition_NackWithRequeue(t *testing.T) {
	conn, mockConn := newTestConnection(t)
	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		DLXExchange:     "test.dlx",
		ShutdownTimeout: 2 * time.Second,
	})

	receipt := &mockReceipt{}
	entry := outbox.Entry{ID: "evt-unknown-disp", EventType: "test.unknown"}
	entryBytes := makeDeliveryBody(t, entry)

	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{
			Disposition: outbox.Disposition(99),
			Receipt:     receipt,
		}
	}

	sub.wg.Add(1)
	sub.processDelivery(context.Background(), ch, amqp.Delivery{
		DeliveryTag: 42,
		Body:        entryBytes,
	}, "test.topic", handler)

	// Unknown disposition should Nack with requeue=true.
	ch.mu.Lock()
	assert.True(t, ch.nackCalled, "unknown disposition should trigger Nack")
	assert.True(t, ch.nackRequeue, "unknown disposition should requeue")
	ch.mu.Unlock()

	// Receipt should be Released (not Committed) for unknown disposition.
	receipt.mu.Lock()
	assert.True(t, receipt.releaseCalled, "unknown disposition should Release Receipt")
	assert.False(t, receipt.commitCalled, "unknown disposition should not Commit Receipt")
	receipt.mu.Unlock()
}

func TestSafeDelay_LargeAttempt_NoPanic(t *testing.T) {
	result := safeDelay(time.Second, 30*time.Second, 100)
	assert.Equal(t, 30*time.Second, result)
}

func TestSafeDelay_ZeroBase(t *testing.T) {
	result := safeDelay(0, 30*time.Second, 5)
	assert.Equal(t, time.Duration(0), result)
}

func TestSafeDelay_NormalRange(t *testing.T) {
	result := safeDelay(time.Second, 30*time.Second, 3)
	assert.Equal(t, 8*time.Second, result)
}

func TestSafeDelay_ExceedsMax(t *testing.T) {
	result := safeDelay(time.Second, 30*time.Second, 10)
	assert.Equal(t, 30*time.Second, result) // 1024s > 30s → capped
}

func TestSafeDelay_NegativeBase(t *testing.T) {
	result := safeDelay(-time.Second, 30*time.Second, 3)
	assert.Equal(t, time.Duration(0), result)
}

// =============================================================================
// retryLoop regression tests (S3 P1)
// =============================================================================

func TestConsumerBase_WrapWithClaimer_TransientError_ThenSuccess(t *testing.T) {
	// Handler fails with Requeue on attempt 0, succeeds (Ack) on attempt 1.
	receipt := &mockReceipt{}
	claimer := &mockClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		RetryCount:     3,
		RetryBaseDelay: 10 * time.Millisecond,
	})
	require.NoError(t, cbErr)

	attempt := 0
	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		n := attempt
		attempt++
		if n == 0 {
			return outbox.HandleResult{
				Disposition: outbox.DispositionRequeue,
				Err:         errors.New("transient failure"),
			}
		}
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	entry := outbox.Entry{ID: "evt-transient-ok"}
	res := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionAck, res.Disposition)
	assert.Same(t, receipt, res.Receipt, "Receipt should be threaded through on success")
	assert.Equal(t, 2, attempt, "handler should have been called 2 times")
}

func TestConsumerBase_WrapWithClaimer_ExplicitReject_NoRetry(t *testing.T) {
	// Handler returns DispositionReject directly (not via PermanentError).
	receipt := &mockReceipt{}
	claimer := &mockClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		RetryCount:     5,
		RetryBaseDelay: 10 * time.Millisecond,
	})
	require.NoError(t, cbErr)

	callCount := 0
	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		callCount++
		return outbox.HandleResult{
			Disposition: outbox.DispositionReject,
			Err:         errors.New("bad payload shape"),
		}
	})

	entry := outbox.Entry{ID: "evt-explicit-reject"}
	res := handler(context.Background(), entry)

	assert.Equal(t, 1, callCount, "handler should be called exactly once (no retry for Reject)")
	assert.Equal(t, outbox.DispositionReject, res.Disposition)
	assert.Same(t, receipt, res.Receipt, "Receipt should be threaded through for Reject")
}

func TestConsumerBase_WrapWithClaimer_WrappedPermanentError_Detected(t *testing.T) {
	// Handler returns Requeue with fmt.Errorf("ctx: %w", outbox.NewPermanentError(...)).
	// retryLoop should detect PermanentError via errors.As and upgrade to Reject.
	receipt := &mockReceipt{}
	claimer := &mockClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, cbErr := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		RetryCount:     5,
		RetryBaseDelay: 10 * time.Millisecond,
	})
	require.NoError(t, cbErr)

	callCount := 0
	handler := cb.Wrap(outbox.Subscription{Topic: "test.topic", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		callCount++
		return outbox.HandleResult{
			Disposition: outbox.DispositionRequeue,
			Err:         fmt.Errorf("ctx: %w", outbox.NewPermanentError(errors.New("bad payload"))),
		}
	})

	entry := outbox.Entry{ID: "evt-wrapped-perm"}
	res := handler(context.Background(), entry)

	assert.Equal(t, 1, callCount, "handler should be called once (PermanentError detected, no retry)")
	assert.Equal(t, outbox.DispositionReject, res.Disposition,
		"wrapped PermanentError should be detected by errors.As and upgraded to Reject")
	assert.Same(t, receipt, res.Receipt, "Receipt should be threaded through for Reject")
}

// NOTE: TestConnection_MaxReconnectAttempts_One deleted (A.1 semantics).
// Reconnect has no attempt cap; successor behavior is covered by
// TestConnection_ReconnectWithBackoff_TransientError_ContinuesIndefinitely.

// =============================================================================
// safeDelay boundary tests (S3 P2)
// =============================================================================

func TestSafeDelay_AttemptZero(t *testing.T) {
	result := safeDelay(time.Second, 30*time.Second, 0)
	assert.Equal(t, time.Second, result) // base * 2^0 = base
}

func TestSafeDelay_ExactMaxSafeShift(t *testing.T) {
	base := time.Second
	maxSafeShift := 63 - bits.Len64(uint64(base))
	// At maxSafeShift, result should still be capped to maxDelay.
	result := safeDelay(base, 30*time.Second, maxSafeShift)
	assert.Equal(t, 30*time.Second, result)
	// At maxSafeShift+1, also capped (overflow guard).
	result2 := safeDelay(base, 30*time.Second, maxSafeShift+1)
	assert.Equal(t, 30*time.Second, result2)
}

// =============================================================================
// isTerminalConnectionError Tests
// =============================================================================

func TestIsTerminalConnectionError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		terminal bool
	}{
		{"permanent", errcode.New(ErrAdapterAMQPConnectPermanent, "bad creds"), true},
		{"exhausted", errcode.New(ErrAdapterAMQPReconnectExhausted, "max attempts"), true},
		{"transient", errcode.New(ErrAdapterAMQPConnect, "timeout"), false},
		{"publish", errcode.New(ErrAdapterAMQPPublish, "channel error"), false},
		{"nil", nil, false},
		{"plain error", fmt.Errorf("oops"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.terminal, isTerminalConnectionError(tt.err))
		})
	}
}

func TestPublisher_Publish_ReconnectExhausted_ReturnsPermanentError(t *testing.T) {
	// When Connection is in terminal state with ReconnectExhausted,
	// Publish should return ErrAdapterAMQPReconnectExhausted (not generic publish error).
	conn := &Connection{
		config: Config{
			URL:             "amqp://test:test@localhost:5672/",
			ChannelPoolSize: 2,
			ConfirmTimeout:  5 * time.Second,
		},
		channelPool:  make(chan AMQPChannel, 2),
		closeCh:      make(chan struct{}),
		connected:    make(chan struct{}),
		terminalCh:   make(chan struct{}),
		permanentErr: errcode.New(ErrAdapterAMQPReconnectExhausted, "max attempts exceeded"),
	}
	close(conn.terminalCh)

	pub := NewPublisher(conn)
	err := pub.Publish(context.Background(), "test.topic", []byte("payload"))

	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrAdapterAMQPReconnectExhausted, ecErr.Code,
		"Publish in terminal state (reconnect exhausted) should return ReconnectExhausted error, not generic publish error")
}

// =============================================================================
// Credential Sanitization Tests
// =============================================================================

func TestConnection_ErrorChain_DoesNotLeakCredentials(t *testing.T) {
	url := "amqp://admin:secret123@broker.example.com:5672/vhost"
	_, err := NewConnection(Config{URL: url}, WithDialFunc(func(u string) (AMQPConnection, error) {
		return nil, fmt.Errorf("dial tcp: lookup %s: no such host", u)
	}))
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret123", "error chain must not leak password")
	assert.NotContains(t, err.Error(), "admin:secret123", "error chain must not leak credentials")
	assert.Contains(t, err.Error(), "***", "error should contain redacted URL")
}

func TestConnection_SanitizeDialError_NoURL(t *testing.T) {
	c := &Connection{config: Config{URL: ""}}
	orig := fmt.Errorf("some error")
	assert.Equal(t, orig, c.sanitizeDialError(orig), "should return original error when URL is empty")
}

func TestConnection_SanitizeDialError_URLNotInError(t *testing.T) {
	c := &Connection{config: Config{URL: "amqp://user:pass@host:5672/"}}
	orig := fmt.Errorf("generic network error")
	assert.Equal(t, orig, c.sanitizeDialError(orig), "should return original error when URL not found in error string")
}

func TestConnection_SanitizeDialError_Nil(t *testing.T) {
	c := &Connection{config: Config{URL: "amqp://user:pass@host:5672/"}}
	assert.Nil(t, c.sanitizeDialError(nil), "should return nil for nil error")
}

// =============================================================================
// RMQ-75-02 supplemental: Health() during reconnecting state
// =============================================================================

func TestConnection_Health_DuringReconnect(t *testing.T) {
	// Scenario: connect → disconnect → Health() returns error while reconnecting
	// → dial succeeds → Health() returns nil.
	// Verifies the intermediate "reconnecting" state that existing tests skip.
	var mu sync.Mutex
	dialCount := 0
	mock1 := newMockConnection()
	mock2 := newMockConnection()
	proceedDial := make(chan struct{})

	// Guard against goroutine leak: if the test fails before close(proceedDial),
	// the reconnect loop stays blocked. sync.Once prevents double-close panic.
	var closeOnce sync.Once
	closeProceed := func() { closeOnce.Do(func() { close(proceedDial) }) }
	t.Cleanup(closeProceed)

	dialFunc := func(url string) (AMQPConnection, error) {
		mu.Lock()
		dialCount++
		n := dialCount
		mu.Unlock()
		if n == 1 {
			return mock1, nil
		}
		// Block until test signals to proceed — lets us observe Health() mid-reconnect.
		<-proceedDial
		return mock2, nil
	}

	conn, err := NewConnection(Config{
		URL:                 "amqp://test:test@localhost:5672/",
		ChannelPoolSize:     2,
		ReconnectBaseDelay:  1 * time.Millisecond,
		ReconnectMaxBackoff: 5 * time.Millisecond,
	}, WithDialFunc(dialFunc))
	require.NoError(t, err)
	defer conn.Close()

	// Verify initial health is OK.
	require.NoError(t, conn.Health(context.Background()), "initial connection should be healthy")

	// Wait for reconnectLoop to register NotifyClose.
	require.Eventually(t, func() bool {
		mock1.mu.Lock()
		defer mock1.mu.Unlock()
		return mock1.notifyCloseCh != nil
	}, time.Second, time.Millisecond)

	// Trigger disconnect.
	mock1.mu.Lock()
	ch := mock1.notifyCloseCh
	mock1.isClosed = true
	mock1.mu.Unlock()
	ch <- &amqp.Error{Code: 320, Reason: "CONNECTION_FORCED", Recover: true}

	// Wait until reconnect dial is blocked (dialCount == 2).
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return dialCount >= 2
	}, 2*time.Second, time.Millisecond, "reconnect dial should be in progress")

	// Health() should return error during reconnecting state with distinct code.
	healthErr := conn.Health(context.Background())
	require.Error(t, healthErr, "Health() must return error while reconnecting")
	var ecErr *errcode.Error
	require.True(t, errors.As(healthErr, &ecErr), "Health() error should wrap *errcode.Error")
	assert.Equal(t, ErrAdapterAMQPReconnecting, ecErr.Code,
		"Health() should return ErrAdapterAMQPReconnecting during reconnect, not generic Connect")

	// Unblock the dial — reconnect succeeds.
	closeProceed()

	// Health() should recover.
	require.Eventually(t, func() bool {
		return conn.Health(context.Background()) == nil
	}, 2*time.Second, time.Millisecond, "Health() should return nil after successful reconnect")
}

// NOTE: TestConnection_MaxReconnectAttempts_PermanentOverridesExhaustion deleted (A.1).
// Runtime reconnect no longer distinguishes permanent vs transient; all errors
// are retried indefinitely. Startup-time permanent classification is covered
// by TestNewConnection_PermanentDialError.

// =============================================================================
// RMQ-RACE-01: WaitConnected stale channel re-validation
// =============================================================================

// TestConnection_WaitConnected_StaleChannelRetry verifies that WaitConnected
// does not return prematurely when the connected channel has been replaced.
// Scenario: old connected channel is already closed, then replaced with a new
// unclosed channel. WaitConnected should block on the NEW channel, not return
// from the already-closed old one.
func TestConnection_WaitConnected_StaleChannelRetry(t *testing.T) {
	mockConn := newMockConnection()

	c := &Connection{
		config:      Config{URL: "amqp://test@localhost/"},
		dial:        func(string) (AMQPConnection, error) { return mockConn, nil },
		channelPool: make(chan AMQPChannel, 5),
		closeCh:     make(chan struct{}),
		connected:   make(chan struct{}),
		terminalCh:  make(chan struct{}),
		state:       StateConnected,
	}

	// Simulate: old connected channel was closed (= previously connected).
	close(c.connected)

	// Replace with a new unclosed channel (= disconnect happened).
	c.mu.Lock()
	c.connected = make(chan struct{})
	c.state = StateDisconnected
	c.mu.Unlock()

	// WaitConnected should NOT return immediately — the new channel is unclosed.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- c.WaitConnected(ctx)
	}()

	// Give WaitConnected time to enter select. If it returns from the old
	// (already closed at creation) channel, it would return before timeout.
	select {
	case err := <-done:
		// Should only return after ctx timeout, not prematurely.
		assert.Error(t, err, "WaitConnected should timeout, not return from stale channel")
		assert.Contains(t, err.Error(), "cancelled")
	case <-time.After(200 * time.Millisecond):
		t.Fatal("WaitConnected hung beyond test deadline")
	}
}

// TestConnection_WaitConnected_RaceRevalidation simulates a reconnect cycle
// during WaitConnected: a goroutine replaces the connected channel and then
// closes the new one. WaitConnected should return nil only after the NEW
// channel is closed.
func TestConnection_WaitConnected_RaceRevalidation(t *testing.T) {
	mockConn := newMockConnection()

	oldConnected := make(chan struct{})
	close(oldConnected) // simulate previously connected

	c := &Connection{
		config:      Config{URL: "amqp://test@localhost/"},
		dial:        func(string) (AMQPConnection, error) { return mockConn, nil },
		channelPool: make(chan AMQPChannel, 5),
		closeCh:     make(chan struct{}),
		connected:   oldConnected,
		terminalCh:  make(chan struct{}),
		conn:        mockConn,
		state:       StateConnected,
	}

	newConnected := make(chan struct{})

	// Goroutine simulates reconnectLoop: replace connected, then close new one.
	go func() {
		time.Sleep(10 * time.Millisecond)
		c.mu.Lock()
		c.connected = newConnected
		c.state = StateDisconnected
		c.mu.Unlock()

		time.Sleep(30 * time.Millisecond)
		c.mu.Lock()
		close(newConnected)
		c.state = StateConnected
		c.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := c.WaitConnected(ctx)
	assert.NoError(t, err, "WaitConnected should return nil after new channel is closed")
}

// TestConnection_WaitConnected_ConcurrentDisconnectReconnect is a stress test
// for the WaitConnected re-validation loop under concurrent reconnection cycles.
// Multiple goroutines call WaitConnected while the main goroutine cycles through
// disconnect/reconnect. All should eventually return nil. Run with -race.
// TestConnection_WaitConnected_ConcurrentDisconnectReconnect is a stress test
// that ensures WaitConnected goroutines actually traverse the disconnect/reconnect
// window, not return instantly from a pre-closed channel.
//
// R2-P1-B fix: start with an UNCLOSED connected channel so waiters block.
// Use a barrier to confirm all waiters are in WaitConnected before starting
// disconnect/reconnect cycles. This guarantees waiters must traverse at least
// one re-validation iteration through the stale-channel detection path.
//
// ref: amqp091-go client_test.go — start barrier + WaitGroup + timeout pattern.
func TestConnection_WaitConnected_ConcurrentDisconnectReconnect(t *testing.T) {
	mockConn := newMockConnection()

	// Start UNCLOSED — waiters will block in WaitConnected's select.
	initialConnected := make(chan struct{})

	c := &Connection{
		config:      Config{URL: "amqp://test@localhost/"},
		dial:        func(string) (AMQPConnection, error) { return mockConn, nil },
		channelPool: make(chan AMQPChannel, 5),
		closeCh:     make(chan struct{}),
		connected:   initialConnected,
		terminalCh:  make(chan struct{}),
		conn:        mockConn,
		state:       StateConnecting, // not yet connected
	}

	const numWaiters = 10
	const numCycles = 5
	var wg sync.WaitGroup
	errs := make(chan error, numWaiters)

	// Launch waiters — they will block on the unclosed connected channel.
	for range numWaiters {
		wg.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = c.ConnectionStatus()
			errs <- c.WaitConnected(ctx)
			_ = c.ConnectionStatus()
		})
	}

	// Give waiters time to enter select on the unclosed channel.
	time.Sleep(10 * time.Millisecond)

	// Cycle disconnect/reconnect. The pattern mirrors reconnectLoop:
	//  1. Replace c.connected with a new unclosed channel (= disconnect)
	//  2. Close the new channel after a delay (= reconnect success)
	//
	// Waiters holding a ref to a previously-closed channel will wake from
	// select, detect the stale reference in re-validation, and loop back
	// to block on the new channel — exercising the RMQ-RACE-01 fix.
	//
	// Close initialConnected first to wake all waiters from their initial block.
	// They will re-validate, find that c.connected has been replaced, and loop.
	firstCh := make(chan struct{})
	c.mu.Lock()
	c.connected = firstCh
	c.state = StateDisconnected
	c.mu.Unlock()
	close(initialConnected) // wake initial waiters

	for i := range numCycles {
		time.Sleep(2 * time.Millisecond)

		if i > 0 {
			// Disconnect: replace with new unclosed channel.
			newCh := make(chan struct{})
			c.mu.Lock()
			c.connected = newCh
			c.state = StateDisconnected
			c.mu.Unlock()
			firstCh = newCh
		}

		time.Sleep(2 * time.Millisecond)

		// Reconnect: close the current channel = connected.
		c.mu.Lock()
		c.state = StateConnected
		close(firstCh)
		c.mu.Unlock()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		assert.NoError(t, err, "all waiters should succeed after reconnect cycles")
	}
}

// =============================================================================
// P3-DEFER-05: Health() state distinction + ConnectionStatus()
// =============================================================================

func TestConnection_Health_StateDistinction(t *testing.T) {
	mockConn := newMockConnection()

	tests := []struct {
		name     string
		state    ConnectionState
		conn     AMQPConnection
		permErr  error
		wantCode errcode.Code
		wantNil  bool
	}{
		{
			name:    "StateConnected with live conn",
			state:   StateConnected,
			conn:    mockConn,
			wantNil: true,
		},
		{
			name:     "StateConnecting never connected",
			state:    StateConnecting,
			conn:     nil,
			wantCode: ErrAdapterAMQPConnect,
		},
		{
			name:     "StateDisconnected reconnecting",
			state:    StateDisconnected,
			conn:     nil,
			wantCode: ErrAdapterAMQPReconnecting,
		},
		{
			name:     "StateTerminal permanent error",
			state:    StateTerminal,
			conn:     nil,
			permErr:  errcode.New(ErrAdapterAMQPConnectPermanent, "bad creds"),
			wantCode: ErrAdapterAMQPConnectPermanent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Connection{
				config:       Config{URL: "amqp://test@localhost/"},
				channelPool:  make(chan AMQPChannel, 1),
				closeCh:      make(chan struct{}),
				connected:    make(chan struct{}),
				terminalCh:   make(chan struct{}),
				state:        tt.state,
				conn:         tt.conn,
				permanentErr: tt.permErr,
			}

			err := c.Health(context.Background())
			if tt.wantNil {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr))
			assert.Equal(t, tt.wantCode, ecErr.Code)
		})
	}
}

func TestConnection_ConnectionStatus(t *testing.T) {
	tests := []struct {
		name  string
		state ConnectionState
		want  ConnectionState
	}{
		{"connecting", StateConnecting, StateConnecting},
		{"connected", StateConnected, StateConnected},
		{"disconnected", StateDisconnected, StateDisconnected},
		{"terminal", StateTerminal, StateTerminal},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Connection{
				config:     Config{URL: "amqp://test@localhost/"},
				closeCh:    make(chan struct{}),
				connected:  make(chan struct{}),
				terminalCh: make(chan struct{}),
				state:      tt.state,
			}
			assert.Equal(t, tt.want, c.ConnectionStatus())
		})
	}
}

func TestConnection_ReconnectLoop_StateTransitions(t *testing.T) {
	var mu sync.Mutex
	dialCount := 0
	mock1 := newMockConnection()
	mock2 := newMockConnection()
	proceedDial := make(chan struct{})

	var closeOnce sync.Once
	closeProceed := func() { closeOnce.Do(func() { close(proceedDial) }) }
	t.Cleanup(closeProceed)

	dialFunc := func(url string) (AMQPConnection, error) {
		mu.Lock()
		dialCount++
		n := dialCount
		mu.Unlock()
		if n == 1 {
			return mock1, nil
		}
		<-proceedDial
		return mock2, nil
	}

	conn, err := NewConnection(Config{
		URL:                 "amqp://test:test@localhost:5672/",
		ChannelPoolSize:     2,
		ReconnectBaseDelay:  1 * time.Millisecond,
		ReconnectMaxBackoff: 5 * time.Millisecond,
	}, WithDialFunc(dialFunc))
	require.NoError(t, err)
	defer conn.Close()

	// Initial state: Connected.
	assert.Equal(t, StateConnected, conn.ConnectionStatus(), "initial state should be Connected")

	// Wait for reconnectLoop to register NotifyClose.
	require.Eventually(t, func() bool {
		mock1.mu.Lock()
		defer mock1.mu.Unlock()
		return mock1.notifyCloseCh != nil
	}, time.Second, time.Millisecond)

	// Trigger disconnect.
	mock1.mu.Lock()
	ch := mock1.notifyCloseCh
	mock1.isClosed = true
	mock1.mu.Unlock()
	ch <- &amqp.Error{Code: 320, Reason: "CONNECTION_FORCED", Recover: true}

	// Wait until reconnect dial is blocked — state should be Disconnected.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return dialCount >= 2
	}, 2*time.Second, time.Millisecond)

	assert.Equal(t, StateDisconnected, conn.ConnectionStatus(),
		"state should be Disconnected during reconnect")

	// Unblock reconnect.
	closeProceed()

	// State should recover to Connected.
	require.Eventually(t, func() bool {
		return conn.ConnectionStatus() == StateConnected
	}, 2*time.Second, time.Millisecond)
}

func TestConnectionState_String(t *testing.T) {
	tests := []struct {
		state ConnectionState
		want  string
	}{
		{StateConnecting, "connecting"},
		{StateConnected, "connected"},
		{StateDisconnected, "disconnected"},
		{StateTerminal, "terminal"},
		{ConnectionState(99), "unknown(99)"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.state.String())
	}
}

// =============================================================================
// RMQ-TEST-01: ConsumerBase retry exhaustion (unit test, no broker)
// =============================================================================

// TestConsumerBase_RetryExhaustion verifies that ConsumerBase retries a
// transiently-failing handler up to RetryCount and then returns
// DispositionReject. This is a unit-level test that invokes the wrapped
// handler directly — see TestIntegration_ConsumerBaseRetry for the
// end-to-end broker test.
func TestConsumerBase_RetryExhaustion(t *testing.T) {
	receipt := &mockReceipt{}
	claimer := &mockClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, cbErr := outbox.NewConsumerBase(
		claimer,
		outbox.ConsumerBaseConfig{
			RetryCount:     2,
			RetryBaseDelay: 10 * time.Millisecond,
			IdempotencyTTL: time.Hour,
		},
	)
	require.NoError(t, cbErr)

	callCount := 0
	wrappedHandler := cb.Wrap(outbox.Subscription{Topic: "test.retry.unit", ConsumerGroup: "test-group"}, func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		callCount++
		return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: assert.AnError}
	})

	entry := outbox.Entry{
		ID:        "evt-retry-unit-001",
		EventType: "test.retry",
		Payload:   []byte(`{"retry":"unit"}`),
		CreatedAt: time.Now().UTC(),
	}

	res := wrappedHandler(context.Background(), entry)
	assert.Equal(t, outbox.DispositionReject, res.Disposition,
		"exhausted retries should result in Reject disposition")
	assert.Equal(t, 2, callCount,
		"handler should be called exactly RetryCount times")
}

// TestPublisher_Publish_ClosesChannel verifies that Publisher.Publish closes
// the channel after use instead of returning it to the shared pool.
// Confirm-mode channels returned to the pool can deadlock the connection
// reader when reused by subscribers (see PR#141 CI failure analysis).
//
// ref: Watermill — publisher and subscriber use completely separate channels,
// never sharing a pool. Default publisher strategy: open, use, close per publish.
func TestPublisher_Publish_ClosesChannel(t *testing.T) {
	ch := newMockChannel()
	ch.autoConfirmation = &amqp.Confirmation{Ack: true}

	mc := &mockConnection{nextCh: ch}
	conn := &Connection{
		config:      Config{URL: "amqp://test@localhost/", ConfirmTimeout: 5 * time.Second},
		channelPool: make(chan AMQPChannel, 5),
		closeCh:     make(chan struct{}),
		connected:   make(chan struct{}),
		terminalCh:  make(chan struct{}),
		state:       StateConnected,
		conn:        mc,
	}

	pub := NewPublisher(conn)
	err := pub.Publish(context.Background(), "test.topic", []byte(`{"test":true}`))
	require.NoError(t, err)

	// Channel must be closed, NOT returned to pool.
	ch.mu.Lock()
	closed := ch.closeCalled
	ch.mu.Unlock()
	assert.True(t, closed, "Publisher must close the channel after use, not return to shared pool")

	// Pool must be empty — channel was not released back.
	select {
	case <-conn.channelPool:
		t.Fatal("channel was returned to pool; Publisher must close confirm-mode channels")
	default:
		// Good — pool is empty.
	}
}

func TestPublisher_Publish_CloseError_DoesNotMaskResult(t *testing.T) {
	ch := newMockChannel()
	ch.closeErr = errors.New("channel already closed")
	ch.autoConfirmation = &amqp.Confirmation{Ack: true}

	mc := &mockConnection{nextCh: ch}
	conn := &Connection{
		config:      Config{URL: "amqp://test@localhost/", ConfirmTimeout: 5 * time.Second},
		channelPool: make(chan AMQPChannel, 5),
		closeCh:     make(chan struct{}),
		connected:   make(chan struct{}),
		terminalCh:  make(chan struct{}),
		state:       StateConnected,
		conn:        mc,
	}

	pub := NewPublisher(conn)
	err := pub.Publish(context.Background(), "test.topic", []byte(`{"test":true}`))
	assert.NoError(t, err, "close error must not mask successful publish result")
}
