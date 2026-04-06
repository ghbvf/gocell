package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
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
	closeErr      error
}

func newMockConnection() *mockConnection {
	return &mockConnection{
		notifyCloseCh: make(chan *amqp.Error, 1),
	}
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

// --- Mock Idempotency Checker ---

type mockIdempotencyChecker struct {
	mu          sync.Mutex
	processed   map[string]bool
	checkErr    error
	markErr     error
	tryProcErr  error
}

func newMockIdempotencyChecker() *mockIdempotencyChecker {
	return &mockIdempotencyChecker{
		processed: make(map[string]bool),
	}
}

func (m *mockIdempotencyChecker) IsProcessed(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.checkErr != nil {
		return false, m.checkErr
	}
	return m.processed[key], nil
}

func (m *mockIdempotencyChecker) MarkProcessed(_ context.Context, key string, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.markErr != nil {
		return m.markErr
	}
	m.processed[key] = true
	return nil
}

func (m *mockIdempotencyChecker) TryProcess(_ context.Context, key string, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.tryProcErr != nil {
		return false, m.tryProcErr
	}
	if m.processed[key] {
		return false, nil
	}
	m.processed[key] = true
	return true, nil
}

// Compile-time interface checks.
var _ idempotency.Checker = (*mockIdempotencyChecker)(nil)

// --- Mock Publisher (for DLQ) ---

type mockPublisher struct {
	mu       sync.Mutex
	messages []publishedMsg
	err      error
}

type publishedMsg struct {
	topic   string
	payload []byte
}

func newMockPublisher() *mockPublisher {
	return &mockPublisher{}
}

func (m *mockPublisher) Publish(_ context.Context, topic string, payload []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.messages = append(m.messages, publishedMsg{topic: topic, payload: payload})
	return nil
}

var _ outbox.Publisher = (*mockPublisher)(nil)

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

func TestConnection_BackoffDelay(t *testing.T) {
	conn, _ := newTestConnection(t)

	tests := []struct {
		name     string
		attempt  int
		expected time.Duration
	}{
		{name: "attempt 0", attempt: 0, expected: 1 * time.Second},
		{name: "attempt 1", attempt: 1, expected: 2 * time.Second},
		{name: "attempt 2", attempt: 2, expected: 4 * time.Second},
		{name: "attempt 10 (capped)", attempt: 10, expected: 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delay := conn.backoffDelay(tt.attempt)
			assert.Equal(t, tt.expected, delay)
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
			name:     "empty string returns empty",
			url:      "",
			expected: "",
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
	handler := func(_ context.Context, e outbox.Entry) error {
		handled <- e
		return nil
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
		ShutdownTimeout: 2 * time.Second,
	})

	handler := func(_ context.Context, e outbox.Entry) error {
		t.Fatal("handler should not be called for unmarshal failure")
		return nil
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
		ShutdownTimeout: 2 * time.Second,
	})

	entry := outbox.Entry{ID: "evt-002", EventType: "test.failed"}
	entryBytes, err := json.Marshal(entry)
	require.NoError(t, err)

	handler := func(_ context.Context, e outbox.Entry) error {
		return errors.New("transient error")
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
	assert.True(t, ch.nackRequeue) // Transient error should requeue.
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
		ShutdownTimeout: 2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately so Subscribe exits.

	err := sub.Subscribe(ctx, "my.topic", func(_ context.Context, _ outbox.Entry) error { return nil })
	assert.NoError(t, err)

	ch.mu.Lock()
	assert.Contains(t, ch.queuesDeclared, "my.topic") // Queue name defaults to topic.
	ch.mu.Unlock()
}

func TestSubscriber_Close_Idempotent(t *testing.T) {
	conn, _ := newTestConnection(t)
	sub := NewSubscriber(conn, SubscriberConfig{})

	assert.NoError(t, sub.Close())
	assert.NoError(t, sub.Close()) // Second close is no-op.
}

func TestSubscriber_Subscribe_AfterClose(t *testing.T) {
	conn, _ := newTestConnection(t)
	sub := NewSubscriber(conn, SubscriberConfig{})

	assert.NoError(t, sub.Close())

	err := sub.Subscribe(context.Background(), "test.topic", func(_ context.Context, _ outbox.Entry) error { return nil })
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
	handler := func(_ context.Context, e outbox.Entry) error {
		handled <- e.ID
		return nil
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
	}

	// Make AcquireChannel fail so subscribeOnce returns error, entering reconnect wait.
	mockConn.mu.Lock()
	mockConn.chanErr = errors.New("no connection")
	mockConn.mu.Unlock()

	sub := NewSubscriber(c, SubscriberConfig{
		QueueName:       "test-queue",
		ShutdownTimeout: 1 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		// Cancel ctx after a short delay to unblock WaitConnected.
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	err := sub.Subscribe(ctx, "test.topic", func(_ context.Context, _ outbox.Entry) error { return nil })
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
		ShutdownTimeout: 1 * time.Second,
	})

	// subscribeOnce should return an error (channel acquisition failure).
	err = sub.subscribeOnce(context.Background(), "test.topic", "test-queue",
		func(_ context.Context, _ outbox.Entry) error { return nil })
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
	}
	// Mark as initially connected.
	close(c.connected)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(c, SubscriberConfig{
		QueueName:       "test-queue",
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
			func(_ context.Context, _ outbox.Entry) error { return nil })
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
		ShutdownTimeout: 2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately so Subscribe exits after setup.

	err := sub.Subscribe(ctx, "session.created", func(_ context.Context, _ outbox.Entry) error { return nil })
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
		ShutdownTimeout: 2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sub.Subscribe(ctx, "session.created", func(_ context.Context, _ outbox.Entry) error { return nil })
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
		ShutdownTimeout: 2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sub.Subscribe(ctx, "my.topic", func(_ context.Context, _ outbox.Entry) error { return nil })
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

	err := sub.Subscribe(ctx, "test.topic", func(_ context.Context, _ outbox.Entry) error { return nil })
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

	err := sub.Subscribe(ctx, "test.topic", func(_ context.Context, _ outbox.Entry) error { return nil })
	assert.NoError(t, err)

	ch.mu.Lock()
	require.Len(t, ch.queueDeclareArgs, 1)
	args := ch.queueDeclareArgs[0]
	assert.Equal(t, "my-dlx", args["x-dead-letter-exchange"])
	assert.Equal(t, "dead-letter-key", args["x-dead-letter-routing-key"])
	ch.mu.Unlock()
}

func TestSubscriber_Subscribe_NoDLX_NilArgs(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		ShutdownTimeout: 2 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sub.Subscribe(ctx, "test.topic", func(_ context.Context, _ outbox.Entry) error { return nil })
	assert.NoError(t, err)

	ch.mu.Lock()
	require.Len(t, ch.queueDeclareArgs, 1)
	assert.Nil(t, ch.queueDeclareArgs[0], "queue args should be nil when DLX is not configured")
	ch.mu.Unlock()
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

func TestConsumerBase_Wrap_Success(t *testing.T) {
	checker := newMockIdempotencyChecker()
	pub := newMockPublisher()

	cb := NewConsumerBase(checker, pub, ConsumerBaseConfig{
		ConsumerGroup: "test-group",
	})

	handlerCalled := false
	handler := cb.Wrap("test.topic", func(_ context.Context, e outbox.Entry) error {
		handlerCalled = true
		assert.Equal(t, "evt-001", e.ID)
		return nil
	})

	entry := outbox.Entry{ID: "evt-001", EventType: "test.created"}
	err := handler(context.Background(), entry)
	assert.NoError(t, err)
	assert.True(t, handlerCalled)

	// Verify idempotency key was marked.
	checker.mu.Lock()
	assert.True(t, checker.processed["test-group:evt-001"])
	checker.mu.Unlock()
}

func TestConsumerBase_Wrap_AlreadyProcessed(t *testing.T) {
	checker := newMockIdempotencyChecker()
	checker.processed["test-group:evt-001"] = true
	pub := newMockPublisher()

	cb := NewConsumerBase(checker, pub, ConsumerBaseConfig{
		ConsumerGroup: "test-group",
	})

	handlerCalled := false
	handler := cb.Wrap("test.topic", func(_ context.Context, e outbox.Entry) error {
		handlerCalled = true
		return nil
	})

	entry := outbox.Entry{ID: "evt-001"}
	err := handler(context.Background(), entry)
	assert.NoError(t, err)
	assert.False(t, handlerCalled) // Should skip because already processed.
}

func TestConsumerBase_Wrap_TransientError_Retry(t *testing.T) {
	checker := newMockIdempotencyChecker()
	pub := newMockPublisher()

	cb := NewConsumerBase(checker, pub, ConsumerBaseConfig{
		ConsumerGroup:  "test-group",
		RetryCount:     3,
		RetryBaseDelay: 10 * time.Millisecond, // Fast for test.
	})

	callCount := 0
	handler := cb.Wrap("test.topic", func(_ context.Context, e outbox.Entry) error {
		callCount++
		if callCount < 3 {
			return errors.New("transient error")
		}
		return nil
	})

	entry := outbox.Entry{ID: "evt-002"}
	err := handler(context.Background(), entry)
	assert.NoError(t, err)
	assert.Equal(t, 3, callCount) // Should retry 3 times total.

	// Should be marked processed on success.
	checker.mu.Lock()
	assert.True(t, checker.processed["test-group:evt-002"])
	checker.mu.Unlock()
}

func TestConsumerBase_Wrap_RetryExhausted_DLQ(t *testing.T) {
	checker := newMockIdempotencyChecker()
	pub := newMockPublisher()

	cb := NewConsumerBase(checker, pub, ConsumerBaseConfig{
		ConsumerGroup:  "test-group",
		RetryCount:     2,
		RetryBaseDelay: 10 * time.Millisecond,
	})

	handler := cb.Wrap("test.topic", func(_ context.Context, e outbox.Entry) error {
		return errors.New("always fails")
	})

	entry := outbox.Entry{ID: "evt-003", EventType: "test.fail"}
	err := handler(context.Background(), entry)
	assert.NoError(t, err) // Returns nil because message was DLQ'd.

	// Verify DLQ message was published.
	pub.mu.Lock()
	require.Len(t, pub.messages, 1)
	assert.Equal(t, "test.topic.dlq", pub.messages[0].topic)

	var dlqEntry outbox.Entry
	require.NoError(t, json.Unmarshal(pub.messages[0].payload, &dlqEntry))
	assert.Equal(t, "evt-003", dlqEntry.ID)
	assert.Equal(t, "always fails", dlqEntry.Metadata["x-death-reason"])
	assert.Equal(t, "test.topic", dlqEntry.Metadata["x-death-topic"])
	assert.Equal(t, "test-group", dlqEntry.Metadata["x-death-consumer-group"])
	assert.Equal(t, "2", dlqEntry.Metadata["x-death-retry-count"])
	pub.mu.Unlock()

	// With TryProcess, the key is claimed atomically before the handler runs.
	// Even though the handler failed and exhausted retries, the key remains marked
	// to prevent duplicate processing by other consumers (message goes to DLQ).
	checker.mu.Lock()
	assert.True(t, checker.processed["test-group:evt-003"])
	checker.mu.Unlock()
}

func TestConsumerBase_Wrap_PermanentError_DLQ(t *testing.T) {
	checker := newMockIdempotencyChecker()
	pub := newMockPublisher()

	cb := NewConsumerBase(checker, pub, ConsumerBaseConfig{
		ConsumerGroup:  "test-group",
		RetryCount:     3,
		RetryBaseDelay: 10 * time.Millisecond,
	})

	handler := cb.Wrap("test.topic", func(_ context.Context, e outbox.Entry) error {
		return NewPermanentError(errors.New("bad payload"))
	})

	entry := outbox.Entry{ID: "evt-004", EventType: "test.permanent"}
	err := handler(context.Background(), entry)
	assert.NoError(t, err) // Returns nil because message was DLQ'd.

	// Verify DLQ message was published — should only be called once (no retry).
	pub.mu.Lock()
	require.Len(t, pub.messages, 1)
	assert.Equal(t, "test.topic.dlq", pub.messages[0].topic)
	pub.mu.Unlock()
}

func TestConsumerBase_Wrap_CustomDLQTopic(t *testing.T) {
	checker := newMockIdempotencyChecker()
	pub := newMockPublisher()

	cb := NewConsumerBase(checker, pub, ConsumerBaseConfig{
		ConsumerGroup:  "test-group",
		RetryCount:     1,
		RetryBaseDelay: 10 * time.Millisecond,
		DLQTopic:       "custom-dlq",
	})

	handler := cb.Wrap("test.topic", func(_ context.Context, e outbox.Entry) error {
		return errors.New("fail")
	})

	entry := outbox.Entry{ID: "evt-005"}
	err := handler(context.Background(), entry)
	assert.NoError(t, err)

	pub.mu.Lock()
	require.Len(t, pub.messages, 1)
	assert.Equal(t, "custom-dlq", pub.messages[0].topic)
	pub.mu.Unlock()
}

func TestConsumerBase_Wrap_IdempotencyCheckError_StillProcesses(t *testing.T) {
	checker := newMockIdempotencyChecker()
	checker.tryProcErr = errors.New("redis down")
	pub := newMockPublisher()

	cb := NewConsumerBase(checker, pub, ConsumerBaseConfig{
		ConsumerGroup: "test-group",
	})

	handlerCalled := false
	handler := cb.Wrap("test.topic", func(_ context.Context, e outbox.Entry) error {
		handlerCalled = true
		return nil
	})

	entry := outbox.Entry{ID: "evt-006"}
	err := handler(context.Background(), entry)
	assert.NoError(t, err)
	assert.True(t, handlerCalled) // Should still process when idempotency check fails.
}

func TestConsumerBase_Wrap_ContextCancelled_DuringRetry(t *testing.T) {
	checker := newMockIdempotencyChecker()
	pub := newMockPublisher()

	cb := NewConsumerBase(checker, pub, ConsumerBaseConfig{
		ConsumerGroup:  "test-group",
		RetryCount:     3,
		RetryBaseDelay: 500 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())

	handler := cb.Wrap("test.topic", func(_ context.Context, e outbox.Entry) error {
		cancel() // Cancel context during first handler call.
		return errors.New("transient error")
	})

	entry := outbox.Entry{ID: "evt-007"}
	err := handler(ctx, entry)
	assert.Error(t, err) // Should return context error.
}

func TestPermanentError(t *testing.T) {
	inner := errors.New("bad data")
	pe := NewPermanentError(inner)

	assert.Contains(t, pe.Error(), "permanent")
	assert.Contains(t, pe.Error(), "bad data")
	assert.Equal(t, inner, pe.Unwrap())
}

// --- P0 #5: DLQ publish failure must propagate error (not silently ACK) ---

func TestConsumerBase_Wrap_DLQPublishFails_RetryExhausted_ReturnsError(t *testing.T) {
	checker := newMockIdempotencyChecker()
	pub := newMockPublisher()
	pub.err = errors.New("DLQ broker down")

	cb := NewConsumerBase(checker, pub, ConsumerBaseConfig{
		ConsumerGroup:  "test-group",
		RetryCount:     1,
		RetryBaseDelay: 10 * time.Millisecond,
	})

	handler := cb.Wrap("test.topic", func(_ context.Context, e outbox.Entry) error {
		return errors.New("always fails")
	})

	entry := outbox.Entry{ID: "evt-dlq-fail-001", EventType: "test.fail"}
	err := handler(context.Background(), entry)
	// When DLQ publish fails, Wrap must return an error so subscriber NACKs with requeue.
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DLQ publish failed after retry exhaustion")
	assert.Contains(t, err.Error(), "DLQ broker down")
}

func TestConsumerBase_Wrap_DLQPublishFails_PermanentError_ReturnsError(t *testing.T) {
	checker := newMockIdempotencyChecker()
	pub := newMockPublisher()
	pub.err = errors.New("DLQ broker down")

	cb := NewConsumerBase(checker, pub, ConsumerBaseConfig{
		ConsumerGroup:  "test-group",
		RetryCount:     3,
		RetryBaseDelay: 10 * time.Millisecond,
	})

	handler := cb.Wrap("test.topic", func(_ context.Context, e outbox.Entry) error {
		return NewPermanentError(errors.New("bad payload"))
	})

	entry := outbox.Entry{ID: "evt-dlq-fail-002", EventType: "test.permanent"}
	err := handler(context.Background(), entry)
	// When DLQ publish fails for a permanent error, Wrap must return an error.
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DLQ publish failed for permanent error")
	assert.Contains(t, err.Error(), "DLQ broker down")
}

// --- Extra fix: wrapped PermanentError detected via errors.As ---

func TestConsumerBase_Wrap_WrappedPermanentError_DetectedByErrorsAs(t *testing.T) {
	checker := newMockIdempotencyChecker()
	pub := newMockPublisher()

	cb := NewConsumerBase(checker, pub, ConsumerBaseConfig{
		ConsumerGroup:  "test-group",
		RetryCount:     3,
		RetryBaseDelay: 10 * time.Millisecond,
	})

	callCount := 0
	handler := cb.Wrap("test.topic", func(_ context.Context, e outbox.Entry) error {
		callCount++
		// Wrap PermanentError inside fmt.Errorf — the old type assertion would miss this.
		return fmt.Errorf("handler context: %w", NewPermanentError(errors.New("unmarshal failed")))
	})

	entry := outbox.Entry{ID: "evt-wrapped-perm", EventType: "test.wrapped"}
	err := handler(context.Background(), entry)
	assert.NoError(t, err) // Should return nil because message was DLQ'd.
	assert.Equal(t, 1, callCount) // Should not retry — permanent error detected on first attempt.

	// Verify DLQ message was published.
	pub.mu.Lock()
	require.Len(t, pub.messages, 1)
	assert.Equal(t, "test.topic.dlq", pub.messages[0].topic)
	pub.mu.Unlock()
}

// --- P0 #7: ctx cancel → NACK without requeue (no requeue storm) ---

func TestSubscriber_ProcessDelivery_CtxCancelled_NackWithoutRequeue(t *testing.T) {
	conn, mockConn := newTestConnection(t)

	ch := newMockChannel()
	mockConn.mu.Lock()
	mockConn.nextCh = ch
	mockConn.mu.Unlock()

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:       "test-queue",
		ShutdownTimeout: 2 * time.Second,
	})

	entry := outbox.Entry{ID: "evt-ctx-cancel", EventType: "test.cancel"}
	entryBytes, err := json.Marshal(entry)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	handler := func(_ context.Context, e outbox.Entry) error {
		// Simulate ctx cancel happening before/during handler.
		cancel()
		return errors.New("transient error during shutdown")
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
	assert.False(t, ch.nackRequeue, "should NACK without requeue when ctx is cancelled")
	assert.Equal(t, uint64(42), ch.nackTag)
	ch.mu.Unlock()

	_ = sub.Close()
}

// =============================================================================
// ConsumerBase.AsMiddleware Tests
// =============================================================================

func TestConsumerBase_AsMiddleware_ReturnsTopicHandlerMiddleware(t *testing.T) {
	checker := newMockIdempotencyChecker()
	pub := newMockPublisher()

	cb := NewConsumerBase(checker, pub, ConsumerBaseConfig{
		ConsumerGroup: "mw-group",
	})

	mw := cb.AsMiddleware()

	// mw should be a valid TopicHandlerMiddleware.
	var _ outbox.TopicHandlerMiddleware = mw

	handlerCalled := false
	wrapped := mw("test.topic", func(_ context.Context, e outbox.Entry) error {
		handlerCalled = true
		assert.Equal(t, "evt-mw-001", e.ID)
		return nil
	})

	entry := outbox.Entry{ID: "evt-mw-001", EventType: "test.middleware"}
	err := wrapped(context.Background(), entry)
	assert.NoError(t, err)
	assert.True(t, handlerCalled)

	// Verify idempotency key was marked (ConsumerBase wrapping is active).
	checker.mu.Lock()
	assert.True(t, checker.processed["mw-group:evt-mw-001"])
	checker.mu.Unlock()
}

func TestConsumerBase_AsMiddleware_Idempotency_SkipsDuplicate(t *testing.T) {
	checker := newMockIdempotencyChecker()
	checker.processed["mw-group:evt-mw-dup"] = true
	pub := newMockPublisher()

	cb := NewConsumerBase(checker, pub, ConsumerBaseConfig{
		ConsumerGroup: "mw-group",
	})

	mw := cb.AsMiddleware()

	handlerCalled := false
	wrapped := mw("test.topic", func(_ context.Context, _ outbox.Entry) error {
		handlerCalled = true
		return nil
	})

	entry := outbox.Entry{ID: "evt-mw-dup"}
	err := wrapped(context.Background(), entry)
	assert.NoError(t, err)
	assert.False(t, handlerCalled, "handler should be skipped for duplicate event")
}

func TestConsumerBase_AsMiddleware_RoutesToDLQ_OnPermanentError(t *testing.T) {
	checker := newMockIdempotencyChecker()
	pub := newMockPublisher()

	cb := NewConsumerBase(checker, pub, ConsumerBaseConfig{
		ConsumerGroup:  "mw-group",
		RetryCount:     3,
		RetryBaseDelay: 10 * time.Millisecond,
	})

	mw := cb.AsMiddleware()

	wrapped := mw("orders.created", func(_ context.Context, _ outbox.Entry) error {
		return NewPermanentError(errors.New("corrupted payload"))
	})

	entry := outbox.Entry{ID: "evt-mw-perm", EventType: "orders.created"}
	err := wrapped(context.Background(), entry)
	assert.NoError(t, err) // DLQ'd, returns nil.

	pub.mu.Lock()
	require.Len(t, pub.messages, 1)
	assert.Equal(t, "orders.created.dlq", pub.messages[0].topic)
	pub.mu.Unlock()
}

func TestConsumerBase_AsMiddleware_WithSubscriberWithMiddleware(t *testing.T) {
	// Integration-style test: wire AsMiddleware into SubscriberWithMiddleware.
	checker := newMockIdempotencyChecker()
	pub := newMockPublisher()

	cb := NewConsumerBase(checker, pub, ConsumerBaseConfig{
		ConsumerGroup: "integration-group",
	})

	// Use a simple recording subscriber to verify the chain works end-to-end.
	var capturedHandler func(context.Context, outbox.Entry) error
	innerSub := &stubSubscriber{
		onSubscribe: func(_ context.Context, _ string, h func(context.Context, outbox.Entry) error) error {
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
	handler := func(_ context.Context, e outbox.Entry) error {
		handlerCalled = true
		receivedEntry = e
		return nil
	}

	err := wrappedSub.Subscribe(context.Background(), "events.test", handler)
	assert.NoError(t, err)
	require.NotNil(t, capturedHandler)

	// Simulate an incoming entry.
	entry := outbox.Entry{ID: "evt-integration-001", EventType: "events.test"}
	err = capturedHandler(context.Background(), entry)
	assert.NoError(t, err)
	assert.True(t, handlerCalled)
	assert.Equal(t, "evt-integration-001", receivedEntry.ID)

	// Verify idempotency was applied.
	checker.mu.Lock()
	assert.True(t, checker.processed["integration-group:evt-integration-001"])
	checker.mu.Unlock()

	// Calling again with the same event should be skipped.
	handlerCalled = false
	err = capturedHandler(context.Background(), entry)
	assert.NoError(t, err)
	assert.False(t, handlerCalled, "duplicate should be skipped by ConsumerBase middleware")
}

// stubSubscriber is a minimal Subscriber for integration tests.
type stubSubscriber struct {
	onSubscribe func(context.Context, string, func(context.Context, outbox.Entry) error) error
}

func (s *stubSubscriber) Subscribe(ctx context.Context, topic string, handler func(context.Context, outbox.Entry) error) error {
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
