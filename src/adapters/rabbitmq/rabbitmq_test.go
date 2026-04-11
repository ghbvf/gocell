package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/ghbvf/gocell/pkg/errcode"
)

// --- Mock AMQP Channel ---

type mockChannel struct {
	mu sync.Mutex

	publishCalled     bool
	publishedMessages []amqp.Publishing
	publishExchange   string
	publishErr        error

	consumeDeliveries chan amqp.Delivery
	consumeErr        error

	qosCalled    bool
	qosPrefetch  int
	confirmCalled bool
	confirmErr    error

	exchangesDeclared  []string
	exchangeDeclareErr error
	queuesDeclared     []string
	queueDeclareArgs   []amqp.Table
	queueBindings      []string

	notifyPublishCh chan amqp.Confirmation

	ackCalled  bool
	ackTag     uint64
	ackErr     error
	nackCalled bool
	nackTag    uint64
	nackRequeue bool
	nackErr    error

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
	defer m.mu.Unlock()
	m.notifyPublishCh = confirm
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
	m.queuesDeclared = append(m.queuesDeclared, name)
	m.queueDeclareArgs = append(m.queueDeclareArgs, args)
	return amqp.Queue{Name: name}, nil
}

func (m *mockChannel) QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	closeErr         error
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

// =============================================================================
// Connection Tests
// =============================================================================

func TestNewConnection_Success(t *testing.T) {
	conn, _ := newTestConnection(t)
	assert.NoError(t, conn.Health())
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

	err := conn.Health()
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

func TestConnection_ReconnectLoop_PermanentError_ExitsLoop(t *testing.T) {
	// Full cycle: connect → disconnect → permanent error → loop exits.
	var mu sync.Mutex
	dialCount := 0
	mock := newMockConnection()
	permanentErr := &amqp.Error{Code: 403, Reason: "ACCESS_REFUSED", Recover: false}

	dialFunc := func(url string) (AMQPConnection, error) {
		mu.Lock()
		dialCount++
		n := dialCount
		mu.Unlock()
		if n == 1 {
			return mock, nil
		}
		return nil, permanentErr
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
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return mock.notifyCloseCh != nil
	}, time.Second, time.Millisecond)

	// Trigger disconnect.
	mock.mu.Lock()
	ch := mock.notifyCloseCh
	mock.isClosed = true
	mock.mu.Unlock()
	ch <- &amqp.Error{Code: 320, Reason: "CONNECTION_FORCED", Recover: true}

	// Wait for permanent error to be hit.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return dialCount >= 2
	}, 2*time.Second, 10*time.Millisecond, "reconnect should have attempted dial")

	// After permanent error, WaitConnected should return the permanent error
	// immediately (not block until ctx timeout).
	require.Eventually(t, func() bool {
		return conn.Health() != nil
	}, 2*time.Second, time.Millisecond, "terminal state should be set")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	waitErr := conn.WaitConnected(ctx)
	require.Error(t, waitErr, "WaitConnected must return error after permanent failure")

	var ecErr *errcode.Error
	require.True(t, errors.As(waitErr, &ecErr), "error should be errcode.Error")
	assert.Equal(t, ErrAdapterAMQPConnectPermanent, ecErr.Code,
		"WaitConnected should return permanent error code, not generic connect error")

	// Health should also reflect terminal state.
	healthErr := conn.Health()
	require.Error(t, healthErr)
	require.True(t, errors.As(healthErr, &ecErr))
	assert.Equal(t, ErrAdapterAMQPConnectPermanent, ecErr.Code)

	// AcquireChannel should also return permanent error (not generic connect error).
	_, acqErr := conn.AcquireChannel()
	require.Error(t, acqErr)
	require.True(t, errors.As(acqErr, &ecErr))
	assert.Equal(t, ErrAdapterAMQPConnectPermanent, ecErr.Code)
}

func TestConnection_MaxReconnectAttempts_Exceeded(t *testing.T) {
	// connect → disconnect → 2 recoverable dial failures → terminal state.
	var mu sync.Mutex
	dialCount := 0
	mock := newMockConnection()
	recoverableErr := errors.New("connection refused")

	dialFunc := func(url string) (AMQPConnection, error) {
		mu.Lock()
		dialCount++
		n := dialCount
		mu.Unlock()
		if n == 1 {
			return mock, nil
		}
		// All subsequent dials fail with a recoverable error.
		return nil, recoverableErr
	}

	conn, err := NewConnection(Config{
		URL:                 "amqp://test:test@localhost:5672/",
		ChannelPoolSize:     2,
		ReconnectBaseDelay:  1 * time.Millisecond,
		ReconnectMaxBackoff: 5 * time.Millisecond,
		MaxReconnectAttempts: 2,
	}, WithDialFunc(dialFunc))
	require.NoError(t, err)
	defer conn.Close()

	// Wait for reconnectLoop to register NotifyClose.
	require.Eventually(t, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return mock.notifyCloseCh != nil
	}, time.Second, time.Millisecond, "reconnectLoop did not call NotifyClose")

	// Trigger disconnect.
	mock.mu.Lock()
	ch := mock.notifyCloseCh
	mock.isClosed = true
	mock.mu.Unlock()
	ch <- &amqp.Error{Code: 320, Reason: "CONNECTION_FORCED", Recover: true}

	// Wait for at least 2 reconnect dial attempts (plus the initial).
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return dialCount >= 3 // 1 initial + 2 reconnect attempts
	}, 2*time.Second, time.Millisecond, "reconnect should have attempted 2 dials")

	// Wait for terminal state (permanentErr set by reconnectLoop).
	require.Eventually(t, func() bool {
		conn.mu.RLock()
		defer conn.mu.RUnlock()
		return conn.permanentErr != nil
	}, 2*time.Second, time.Millisecond, "terminal state should be set after max attempts")

	// WaitConnected should return permanent error.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	waitErr := conn.WaitConnected(ctx)
	require.Error(t, waitErr, "WaitConnected must return error after max attempts exceeded")

	var ecErr *errcode.Error
	require.True(t, errors.As(waitErr, &ecErr), "error should be errcode.Error")
	assert.Equal(t, ErrAdapterAMQPConnectPermanent, ecErr.Code,
		"WaitConnected should return permanent error code")

	// Health should also reflect terminal state.
	healthErr := conn.Health()
	require.Error(t, healthErr)
	require.True(t, errors.As(healthErr, &ecErr))
	assert.Equal(t, ErrAdapterAMQPConnectPermanent, ecErr.Code)
}

func TestConnection_MaxReconnectAttempts_Zero_Unlimited(t *testing.T) {
	// MaxReconnectAttempts=0 (default) → fail twice then succeed → recovers.
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
		MaxReconnectAttempts: 0, // unlimited
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
		return conn.Health() == nil
	}, 2*time.Second, time.Millisecond, "connection should be healthy after reconnect")

	// WaitConnected should succeed (connected channel re-closed on reconnect).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	waitErr := conn.WaitConnected(ctx)
	require.NoError(t, waitErr, "WaitConnected should succeed with unlimited reconnect attempts")
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

func TestConnection_ReconnectWithBackoff_PermanentError(t *testing.T) {
	permanentErr := &amqp.Error{Code: 403, Reason: "ACCESS_REFUSED", Recover: false}

	conn := &Connection{
		config: Config{
			URL:                 "amqp://test:test@localhost:5672/",
			ReconnectBaseDelay:  1 * time.Millisecond,
			ReconnectMaxBackoff: 5 * time.Millisecond,
		},
		dial: func(url string) (AMQPConnection, error) {
			return nil, permanentErr
		},
		closeCh:   make(chan struct{}),
		connected: make(chan struct{}),
		terminalCh: make(chan struct{}),
	}

	ok, err := conn.reconnectWithBackoff()
	assert.False(t, ok, "must return false on permanent dial error")
	assert.Error(t, err, "must return the permanent error")

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrAdapterAMQPConnectPermanent, ecErr.Code)
}

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
		closeCh:   make(chan struct{}),
		connected: make(chan struct{}),
		terminalCh: make(chan struct{}),
	}

	ok, err := conn.reconnectWithBackoff()
	assert.True(t, ok, "must return true after successful reconnect")
	assert.NoError(t, err)

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
		closeCh:   closeCh,
		connected: make(chan struct{}),
		terminalCh: make(chan struct{}),
	}

	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		ok, err := conn.reconnectWithBackoff()
		done <- result{ok, err}
	}()

	time.Sleep(50 * time.Millisecond)
	close(closeCh)

	select {
	case r := <-done:
		assert.False(t, r.ok, "must return false when closeCh fires")
		assert.NoError(t, r.err, "clean shutdown, no permanent error")
	case <-time.After(2 * time.Second):
		t.Fatal("reconnectWithBackoff did not return after closeCh was closed")
	}
}

// =============================================================================
// Publisher Tests
// =============================================================================

func TestPublisher_InterfaceCompliance(t *testing.T) {
	var _ outbox.Publisher = (*Publisher)(nil)
}

func TestPublisher_Publish_Success(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	// Pre-create a mock channel that will send confirmation.
	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	pub := NewPublisher(conn)

	// Send confirmation asynchronously.
	go func() {
		time.Sleep(10 * time.Millisecond)
		ch.mu.Lock()
		notifyCh := ch.notifyPublishCh
		ch.mu.Unlock()
		notifyCh <- amqp.Confirmation{Ack: true, DeliveryTag: 1}
	}()

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
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	pub := NewPublisher(conn)

	go func() {
		time.Sleep(10 * time.Millisecond)
		ch.mu.Lock()
		notifyCh := ch.notifyPublishCh
		ch.mu.Unlock()
		notifyCh <- amqp.Confirmation{Ack: false, DeliveryTag: 1}
	}()

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
			URL:            "amqp://test:test@localhost:5672/",
			ChannelPoolSize: 2,
			ConfirmTimeout: 5 * time.Second,
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
	entryBytes, err := json.Marshal(entry)
	require.NoError(t, err)

	handled := make(chan outbox.Entry, 1)
	handler := func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		handled <- e
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Send delivery then close.
	go func() {
		ch.consumeDeliveries <- amqp.Delivery{
			DeliveryTag: 1,
			Body:        entryBytes,
		}
		// Wait for processing then cancel.
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err = sub.Subscribe(ctx, "test.topic", handler)
	assert.NoError(t, err)

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
	assert.True(t, ch.ackCalled)
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

	go func() {
		ch.consumeDeliveries <- amqp.Delivery{
			DeliveryTag: 1,
			Body:        []byte("not valid json{{{"),
		}
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := sub.Subscribe(ctx, "test.topic", handler)
	assert.NoError(t, err)

	ch.mu.Lock()
	assert.True(t, ch.nackCalled)
	assert.False(t, ch.nackRequeue) // Unmarshal failure should not requeue.
	ch.mu.Unlock()

	assert.NoError(t, sub.Close())
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
	entryBytes, err := json.Marshal(entry)
	require.NoError(t, err)

	handler := func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: errors.New("transient error")}
	}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		ch.consumeDeliveries <- amqp.Delivery{
			DeliveryTag: 1,
			Body:        entryBytes,
		}
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err = sub.Subscribe(ctx, "test.topic", handler)
	assert.NoError(t, err)

	ch.mu.Lock()
	assert.True(t, ch.nackCalled)
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

	err := sub.Subscribe(ctx, "my.topic", outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
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

	err := sub.Subscribe(context.Background(), "test.topic", outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
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

	// The subscribe loop will:
	// 1. subscribeOnce with ch1 -> delivery channel closes -> error
	// 2. WaitConnected (already connected) -> subscribeOnce with ch2
	// 3. Handler processes message, then we cancel ctx to exit cleanly.
	go func() {
		// Close ch1's delivery channel to simulate connection loss.
		time.Sleep(20 * time.Millisecond)
		close(ch1.consumeDeliveries)

		// Let ch2 process one message, then cancel.
		entry := outbox.Entry{ID: "reconnect-001", EventType: "test.reconnected"}
		entryBytes, _ := json.Marshal(entry)
		time.Sleep(100 * time.Millisecond)
		ch2.consumeDeliveries <- amqp.Delivery{
			DeliveryTag: 1,
			Body:        entryBytes,
		}
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	handled := make(chan string, 1)
	handler := func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		handled <- e.ID
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	err := sub.Subscribe(ctx, "test.topic", handler)
	assert.NoError(t, err) // Clean exit via ctx cancel.

	// Verify the handler was called after reconnect.
	select {
	case id := <-handled:
		assert.Equal(t, "reconnect-001", id)
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not called after reconnect")
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

	err := sub.Subscribe(ctx, "test.topic", outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
	assert.NoError(t, err) // Clean exit via ctx cancel during WaitConnected.
}

func TestSubscriber_ResolveQueueName(t *testing.T) {
	tests := []struct {
		name          string
		queueName     string
		consumerGroup string
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
			name:          "consumer group derives queue name",
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sub := &Subscriber{
				config: SubscriberConfig{
					QueueName:     tt.queueName,
					ConsumerGroup: tt.consumerGroup,
				},
			}
			assert.Equal(t, tt.expected, sub.resolveQueueName(tt.topic))
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

	subscribeDone := make(chan error, 1)

	go func() {
		// Close delivery channel to trigger reconnect.
		time.Sleep(20 * time.Millisecond)
		close(ch.consumeDeliveries)

		// Simulate disconnection: re-create the connected channel so WaitConnected blocks.
		time.Sleep(10 * time.Millisecond)
		c.mu.Lock()
		c.connected = make(chan struct{})
		c.mu.Unlock()

		// Close subscriber while WaitConnected is blocking.
		// The derived context in Subscribe should be cancelled by closeCh, unblocking WaitConnected.
		time.Sleep(30 * time.Millisecond)
		_ = sub.Close()
	}()

	go func() {
		subscribeDone <- sub.Subscribe(context.Background(), "test.topic",
			outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
	}()

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

	err := sub.Subscribe(ctx, "session.created", outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
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

	err := sub.Subscribe(ctx, "session.created", outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
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

	err := sub.Subscribe(ctx, "my.topic", outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
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

	err := sub.Subscribe(ctx, "test.topic", outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
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

	err := sub.Subscribe(ctx, "test.topic", outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
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

	err := sub.Subscribe(context.Background(), "test.topic", outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DLXExchange is required")
}

// =============================================================================
// ConsumerBase Tests
// =============================================================================

func TestConsumerBaseConfig_Defaults(t *testing.T) {
	cfg := ConsumerBaseConfig{}
	cfg.setDefaults()

	assert.Equal(t, 3, cfg.RetryCount)
	assert.Equal(t, 1*time.Second, cfg.RetryBaseDelay)
	assert.Equal(t, idempotency.DefaultTTL, cfg.IdempotencyTTL)
}

func TestConsumerBaseConfig_Defaults_NegativeLeaseTTL(t *testing.T) {
	cfg := ConsumerBaseConfig{LeaseTTL: -1 * time.Minute}
	cfg.setDefaults()

	assert.Equal(t, idempotency.DefaultLeaseTTL, cfg.LeaseTTL)
}

func TestConsumerBaseConfig_Defaults_NegativeIdempotencyTTL(t *testing.T) {
	cfg := ConsumerBaseConfig{IdempotencyTTL: -1 * time.Hour}
	cfg.setDefaults()

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
	entryBytes, err := json.Marshal(entry)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	handler := func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		// Simulate ctx cancel happening before/during handler.
		cancel()
		return outbox.HandleResult{Disposition: outbox.DispositionRequeue, Err: errors.New("transient error during shutdown")}
	}

	go func() {
		ch.consumeDeliveries <- amqp.Delivery{
			DeliveryTag: 42,
			Body:        entryBytes,
		}
		// Give time for processing then close deliveries to exit.
		time.Sleep(100 * time.Millisecond)
		close(ch.consumeDeliveries)
	}()

	_ = sub.Subscribe(ctx, "test.topic", handler)

	// Wait briefly for async processing.
	time.Sleep(50 * time.Millisecond)

	ch.mu.Lock()
	assert.True(t, ch.nackCalled, "should NACK the delivery")
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

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup: "mw-group",
	})

	mw := cb.AsMiddleware()

	// mw should be a valid TopicHandlerMiddleware.
	var _ outbox.TopicHandlerMiddleware = mw

	handlerCalled := false
	wrapped := mw("test.topic", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
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

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup: "mw-group",
	})

	mw := cb.AsMiddleware()

	handlerCalled := false
	wrapped := mw("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup:  "mw-group",
		RetryCount:     3,
		RetryBaseDelay: 10 * time.Millisecond,
	})

	mw := cb.AsMiddleware()

	wrapped := mw("orders.created", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup: "integration-group",
	})

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
		Middleware: []outbox.TopicHandlerMiddleware{cb.AsMiddleware()},
	}

	var receivedEntry outbox.Entry
	handlerCalled := false
	handler := func(_ context.Context, e outbox.Entry) outbox.HandleResult {
		handlerCalled = true
		receivedEntry = e
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	err := wrappedSub.Subscribe(context.Background(), "events.test", handler)
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

// stubSubscriber is a minimal Subscriber for integration tests.
type stubSubscriber struct {
	onSubscribe func(context.Context, string, outbox.EntryHandler) error
}

func (s *stubSubscriber) Subscribe(ctx context.Context, topic string, handler outbox.EntryHandler) error {
	if s.onSubscribe != nil {
		return s.onSubscribe(ctx, topic, handler)
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
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	pub := NewPublisher(conn)

	// Close the notifyPublishCh without sending any value to simulate
	// the confirm channel being closed (e.g., broker disconnected after publish).
	go func() {
		time.Sleep(10 * time.Millisecond)
		ch.mu.Lock()
		notifyCh := ch.notifyPublishCh
		ch.mu.Unlock()
		close(notifyCh)
	}()

	err := pub.Publish(context.Background(), "test.topic", []byte(`{"data":"value"}`))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_AMQP_CONFIRM_TIMEOUT")
	assert.Contains(t, err.Error(), "confirm channel closed")
}

// =============================================================================
// Mock Claimer / Receipt for Solution B tests
// =============================================================================

type mockReceipt struct {
	mu           sync.Mutex
	commitCalled bool
	commitErr    error
	releaseCalled bool
	releaseErr   error
	commitCtx    context.Context
	releaseCtx   context.Context
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

var _ outbox.Receipt = (*mockReceipt)(nil)

type mockClaimer struct {
	mu     sync.Mutex
	state  idempotency.ClaimState
	receipt outbox.Receipt
	err    error
	claims []string
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

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup: "test-group",
	})

	handlerCalled := false
	handler := cb.Wrap("test.topic", func(_ context.Context, e outbox.Entry) outbox.HandleResult {
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

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup: "test-group",
	})

	handlerCalled := false
	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup: "test-group",
	})

	handlerCalled := false
	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup:  "test-group",
		RetryCount:     1,
		RetryBaseDelay: 10 * time.Millisecond,
	})

	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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
	cb := NewConsumerBaseWithClaimer(claimer, ConsumerBaseConfig{
		ConsumerGroup:  "test-group",
		RetryCount:     3,
		RetryBaseDelay: 10 * time.Millisecond,
	})

	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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
	cb := NewConsumerBaseWithClaimer(claimer, ConsumerBaseConfig{
		ConsumerGroup:  "test-group",
		RetryCount:     3,
		RetryBaseDelay: 10 * time.Millisecond,
	})

	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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
		{err: errors.New("redis down")},                                     // attempt 0
		{err: errors.New("redis down")},                                     // attempt 1
		{state: idempotency.ClaimAcquired, receipt: receipt},                // attempt 2 — success
	}}

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup:      "test-group",
		ClaimRetryCount:    3,
		ClaimRetryBaseDelay: 10 * time.Millisecond,
	})

	handlerCalled := false
	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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
	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup:      "test-group",
		ClaimRetryCount:    3,
		ClaimRetryBaseDelay: 20 * time.Millisecond,
	})

	handlerCalled := false
	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup:      "test-group",
		ClaimRetryCount:    3,
		ClaimRetryBaseDelay: 5 * time.Second, // long delay — ctx cancel must short-circuit
	})

	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup:      "test-group",
		ClaimRetryCount:    1,
		ClaimRetryBaseDelay: 5 * time.Second,
	})

	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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
		{err: errors.New("redis down")},                                     // attempt 0
		{state: idempotency.ClaimAcquired, receipt: receipt},                // attempt 1 — success
	}}

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup:      "test-group",
		RetryCount:         5,                   // handler retries — should not affect claim
		RetryBaseDelay:     1 * time.Second,     // handler backoff — should not affect claim
		ClaimRetryCount:    2,                   // claim retries
		ClaimRetryBaseDelay: 10 * time.Millisecond, // claim backoff
	})

	handlerCalled := false
	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup:      "test-group",
		ClaimRetryCount:    3,
		ClaimRetryBaseDelay: 100 * time.Millisecond,
		MaxRetryDelay:      50 * time.Millisecond, // cap below base — forces all delays to 50ms
	})

	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup:       "test-group",
		ClaimRetryCount:     2,
		ClaimRetryBaseDelay: -1 * time.Second, // negative — must not panic
	})

	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	// Must not panic; negative delay is clamped to default by setDefaults.
	res := handler(context.Background(), outbox.Entry{ID: "evt-neg-delay"})
	assert.Equal(t, outbox.DispositionRequeue, res.Disposition)
}

func TestConsumerBase_NegativeMaxRetryDelay_NoPanic(t *testing.T) {
	claimer := &mockClaimer{err: errors.New("redis down")}

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup:       "test-group",
		ClaimRetryCount:     2,
		ClaimRetryBaseDelay: 10 * time.Millisecond,
		MaxRetryDelay:       -1 * time.Second, // negative — must not panic
	})

	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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
	entryBytes, err := json.Marshal(entry)
	require.NoError(t, err)

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
	entryBytes, err := json.Marshal(entry)
	require.NoError(t, err)

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
	entryBytes, err := json.Marshal(entry)
	require.NoError(t, err)

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
	entryBytes, err := json.Marshal(entry)
	require.NoError(t, err)

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
	entryBytes, err := json.Marshal(entry)
	require.NoError(t, err)

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

	err := sub.Subscribe(context.Background(), "test.topic", outbox.WrapLegacyHandler(func(_ context.Context, _ outbox.Entry) error { return nil }))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DLXExchange is required")
}

func TestConsumerBase_WrapWithClaimer_ClaimBusy_HasBackoff(t *testing.T) {
	claimer := &mockClaimer{state: idempotency.ClaimBusy}

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup:  "test-group",
		RetryBaseDelay: 50 * time.Millisecond,
	})

	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		t.Fatal("handler should not be called for ClaimBusy")
		return outbox.HandleResult{}
	})

	start := time.Now()
	res := handler(context.Background(), outbox.Entry{ID: "evt-busy-backoff"})
	elapsed := time.Since(start)

	assert.Equal(t, outbox.DispositionRequeue, res.Disposition)
	assert.GreaterOrEqual(t, elapsed, 40*time.Millisecond, "ClaimBusy should backoff before requeue")
}

func TestProcessDelivery_BrokerAckFails_ReleasesReceipt(t *testing.T) {
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

	receipt := &mockReceipt{}
	entry := outbox.Entry{ID: "evt-broker-fail", EventType: "test.brokerfail"}
	entryBytes, err := json.Marshal(entry)
	require.NoError(t, err)

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
	assert.False(t, receipt.commitCalled, "should not Commit when broker Ack fails")
	assert.True(t, receipt.releaseCalled, "should Release when broker Ack fails")
	receipt.mu.Unlock()
}

// =============================================================================
// ClaimFailOpen config tests
// =============================================================================

func boolPtr(b bool) *bool { return &b }

func TestConsumerBase_WrapWithClaimer_ClaimError_FailClosed(t *testing.T) {
	claimer := &mockClaimer{err: errors.New("redis down")}

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup:      "test-group",
		ClaimFailOpen:      boolPtr(false),
		ClaimRetryCount:    3,
		ClaimRetryBaseDelay: 10 * time.Millisecond,
	})

	handlerCalled := false
	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup: "test-group",
		ClaimFailOpen: boolPtr(true),
	})

	handlerCalled := false
	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		handlerCalled = true
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	res := handler(context.Background(), outbox.Entry{ID: "evt-fail-open-explicit"})
	assert.True(t, handlerCalled, "handler must be called when fail-open is explicit")
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
	assert.Nil(t, res.Receipt, "no Receipt when claim fails")
}

// =============================================================================
// retryLoop post-loop ctx.Err() test (S3-02)
// =============================================================================

func TestConsumerBase_RetryLoop_CtxCancelledAfterFinalAttempt_Requeues(t *testing.T) {
	// S3-02: When ctx is cancelled by the time the final retry completes,
	// retryLoop must return Requeue (not Reject to DLX).
	receipt := &mockReceipt{}
	claimer := &mockClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup:  "test-group",
		RetryCount:     1, // single attempt, no inter-attempt sleep
		RetryBaseDelay: time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())

	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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
	entryBytes, err := json.Marshal(entry)
	require.NoError(t, err)

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
	entryBytes, err := json.Marshal(entry)
	require.NoError(t, err)

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
	entryBytes, err := json.Marshal(entry)
	require.NoError(t, err)

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

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup:  "test-group",
		RetryCount:     3,
		RetryBaseDelay: 10 * time.Millisecond,
	})

	attempt := 0
	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup:  "test-group",
		RetryCount:     5,
		RetryBaseDelay: 10 * time.Millisecond,
	})

	callCount := 0
	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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

	cb := NewConsumerBase(claimer, ConsumerBaseConfig{
		ConsumerGroup:  "test-group",
		RetryCount:     5,
		RetryBaseDelay: 10 * time.Millisecond,
	})

	callCount := 0
	handler := cb.Wrap("test.topic", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
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

// =============================================================================
// MaxReconnectAttempts=1 boundary test (S3 P2)
// =============================================================================

func TestConnection_MaxReconnectAttempts_One(t *testing.T) {
	// MaxReconnectAttempts=1: after exactly 1 reconnect attempt, should enter terminal state.
	var mu sync.Mutex
	dialCount := 0
	mock := newMockConnection()
	recoverableErr := errors.New("connection refused")

	dialFunc := func(url string) (AMQPConnection, error) {
		mu.Lock()
		dialCount++
		n := dialCount
		mu.Unlock()
		if n == 1 {
			return mock, nil
		}
		return nil, recoverableErr
	}

	conn, err := NewConnection(Config{
		URL:                  "amqp://test:test@localhost:5672/",
		ChannelPoolSize:      2,
		ReconnectBaseDelay:   1 * time.Millisecond,
		ReconnectMaxBackoff:  5 * time.Millisecond,
		MaxReconnectAttempts: 1,
	}, WithDialFunc(dialFunc))
	require.NoError(t, err)
	defer conn.Close()

	// Wait for reconnectLoop to register NotifyClose.
	require.Eventually(t, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return mock.notifyCloseCh != nil
	}, time.Second, time.Millisecond, "reconnectLoop did not call NotifyClose")

	// Trigger disconnect.
	mock.mu.Lock()
	ch := mock.notifyCloseCh
	mock.isClosed = true
	mock.mu.Unlock()
	ch <- &amqp.Error{Code: 320, Reason: "CONNECTION_FORCED", Recover: true}

	// Wait for exactly 1 reconnect attempt (total dials: 1 initial + 1 reconnect = 2).
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return dialCount >= 2
	}, 2*time.Second, time.Millisecond, "reconnect should have attempted 1 dial")

	// Wait for terminal state.
	require.Eventually(t, func() bool {
		conn.mu.RLock()
		defer conn.mu.RUnlock()
		return conn.permanentErr != nil
	}, 2*time.Second, time.Millisecond, "terminal state should be set after 1 attempt")

	// WaitConnected should return permanent error.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	waitErr := conn.WaitConnected(ctx)
	require.Error(t, waitErr, "WaitConnected must return error after max attempts exceeded")

	var ecErr *errcode.Error
	require.True(t, errors.As(waitErr, &ecErr), "error should be errcode.Error")
	assert.Equal(t, ErrAdapterAMQPConnectPermanent, ecErr.Code,
		"WaitConnected should return permanent error code")
}

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
