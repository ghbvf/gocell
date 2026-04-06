package rabbitmq

import (
	"context"
	"log/slog"
	"math"
	"net/url"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Error codes for the RabbitMQ adapter.
const (
	ErrAdapterAMQPConnect        errcode.Code = "ERR_ADAPTER_AMQP_CONNECT"
	ErrAdapterAMQPPublish        errcode.Code = "ERR_ADAPTER_AMQP_PUBLISH"
	ErrAdapterAMQPConfirmTimeout errcode.Code = "ERR_ADAPTER_AMQP_CONFIRM_TIMEOUT"
	ErrAdapterAMQPSubscribe      errcode.Code = "ERR_ADAPTER_AMQP_SUBSCRIBE"
	ErrAdapterAMQPConsume        errcode.Code = "ERR_ADAPTER_AMQP_CONSUME"
)

// Config holds configuration for the RabbitMQ connection.
type Config struct {
	// URL is the AMQP connection URL (e.g., "amqp://guest:guest@localhost:5672/").
	URL string

	// ReconnectMaxBackoff is the maximum backoff duration between reconnect attempts.
	// Default: 30s.
	ReconnectMaxBackoff time.Duration

	// ReconnectBaseDelay is the initial delay for exponential backoff.
	// Default: 1s.
	ReconnectBaseDelay time.Duration

	// ChannelPoolSize is the maximum number of channels in the pool.
	// Default: 10.
	ChannelPoolSize int

	// ConfirmTimeout is the timeout for publisher confirm mode.
	// Default: 5s.
	ConfirmTimeout time.Duration
}

func (c *Config) setDefaults() {
	if c.ReconnectMaxBackoff == 0 {
		c.ReconnectMaxBackoff = 30 * time.Second
	}
	if c.ReconnectBaseDelay == 0 {
		c.ReconnectBaseDelay = 1 * time.Second
	}
	if c.ChannelPoolSize <= 0 {
		c.ChannelPoolSize = 10
	}
	if c.ConfirmTimeout == 0 {
		c.ConfirmTimeout = 5 * time.Second
	}
}

// AMQPConnection abstracts the amqp.Connection for testing.
type AMQPConnection interface {
	Channel() (AMQPChannel, error)
	NotifyClose(receiver chan *amqp.Error) chan *amqp.Error
	IsClosed() bool
	Close() error
}

// AMQPChannel abstracts the amqp.Channel for testing.
type AMQPChannel interface {
	Publish(exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error
	PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error
	Consume(queue, consumer string, autoAck, exclusive, noLocal, noWait bool, args amqp.Table) (<-chan amqp.Delivery, error)
	Qos(prefetchCount, prefetchSize int, global bool) error
	Confirm(noWait bool) error
	NotifyPublish(confirm chan amqp.Confirmation) chan amqp.Confirmation
	ExchangeDeclare(name, kind string, durable, autoDelete, internal, noWait bool, args amqp.Table) error
	QueueDeclare(name string, durable, autoDelete, exclusive, noWait bool, args amqp.Table) (amqp.Queue, error)
	QueueBind(name, key, exchange string, noWait bool, args amqp.Table) error
	Ack(tag uint64, multiple bool) error
	Nack(tag uint64, multiple, requeue bool) error
	Close() error
}

// DialFunc is the function signature for establishing AMQP connections.
type DialFunc func(url string) (AMQPConnection, error)

// amqpConnectionWrapper wraps a real *amqp.Connection to implement AMQPConnection.
type amqpConnectionWrapper struct {
	conn *amqp.Connection
}

func (w *amqpConnectionWrapper) Channel() (AMQPChannel, error) {
	return w.conn.Channel()
}

func (w *amqpConnectionWrapper) NotifyClose(receiver chan *amqp.Error) chan *amqp.Error {
	return w.conn.NotifyClose(receiver)
}

func (w *amqpConnectionWrapper) IsClosed() bool {
	return w.conn.IsClosed()
}

func (w *amqpConnectionWrapper) Close() error {
	return w.conn.Close()
}

// DefaultDial creates a real AMQP connection.
func DefaultDial(url string) (AMQPConnection, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, err
	}
	return &amqpConnectionWrapper{conn: conn}, nil
}

// Connection manages an AMQP connection with auto-reconnect and channel pooling.
type Connection struct {
	config Config
	dial   DialFunc

	mu   sync.RWMutex
	conn AMQPConnection

	channelPool chan AMQPChannel

	closeCh chan struct{}
	closed  bool

	// connected is closed when a connection is established, re-created on disconnect.
	connected chan struct{}
}

// NewConnection creates a new Connection with the given config.
// It attempts an initial connection and starts the reconnect loop.
func NewConnection(config Config, opts ...ConnectionOption) (*Connection, error) {
	config.setDefaults()

	c := &Connection{
		config:      config,
		dial:        DefaultDial,
		channelPool: make(chan AMQPChannel, config.ChannelPoolSize),
		closeCh:     make(chan struct{}),
		connected:   make(chan struct{}),
	}

	for _, opt := range opts {
		opt(c)
	}

	if err := c.connect(); err != nil {
		return nil, errcode.Wrap(ErrAdapterAMQPConnect, "rabbitmq: initial connection failed", err)
	}

	close(c.connected)
	go c.reconnectLoop()

	return c, nil
}

// ConnectionOption configures a Connection.
type ConnectionOption func(*Connection)

// WithDialFunc overrides the default AMQP dial function (useful for testing).
func WithDialFunc(dial DialFunc) ConnectionOption {
	return func(c *Connection) {
		c.dial = dial
	}
}

func (c *Connection) connect() error {
	conn, err := c.dial(c.config.URL)
	if err != nil {
		return errcode.Wrap(ErrAdapterAMQPConnect, "rabbitmq: dial", err)
	}

	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()

	slog.Info("rabbitmq: connection established",
		slog.String("url", sanitizeURL(c.config.URL)))
	return nil
}

func (c *Connection) reconnectLoop() {
	for {
		c.mu.RLock()
		conn := c.conn
		c.mu.RUnlock()

		if conn == nil {
			return
		}

		closeCh := conn.NotifyClose(make(chan *amqp.Error, 1))

		select {
		case <-c.closeCh:
			return
		case amqpErr, ok := <-closeCh:
			if !ok {
				return
			}
			if amqpErr != nil {
				slog.Warn("rabbitmq: connection lost, reconnecting",
					slog.String("error", amqpErr.Error()))
			}
		}

		// Drain the channel pool on disconnect.
		c.drainChannelPool()

		// Create a new connected channel for waiters.
		c.mu.Lock()
		c.connected = make(chan struct{})
		c.mu.Unlock()

		c.reconnectWithBackoff()

		c.mu.Lock()
		close(c.connected)
		c.mu.Unlock()
	}
}

func (c *Connection) reconnectWithBackoff() {
	attempt := 0
	for {
		select {
		case <-c.closeCh:
			return
		default:
		}

		delay := c.backoffDelay(attempt)
		slog.Info("rabbitmq: reconnect attempt",
			slog.Int("attempt", attempt+1),
			slog.Duration("delay", delay))

		select {
		case <-c.closeCh:
			return
		case <-time.After(delay):
		}

		if err := c.connect(); err != nil {
			slog.Warn("rabbitmq: reconnect failed",
				slog.Int("attempt", attempt+1),
				slog.String("error", err.Error()))
			attempt++
			continue
		}

		slog.Info("rabbitmq: reconnected successfully",
			slog.Int("attempts", attempt+1))
		return
	}
}

func (c *Connection) backoffDelay(attempt int) time.Duration {
	delay := c.config.ReconnectBaseDelay * time.Duration(math.Pow(2, float64(attempt)))
	if delay > c.config.ReconnectMaxBackoff {
		delay = c.config.ReconnectMaxBackoff
	}
	return delay
}

func (c *Connection) drainChannelPool() {
	for {
		select {
		case ch := <-c.channelPool:
			if err := ch.Close(); err != nil {
				slog.Debug("rabbitmq: error closing pooled channel",
					slog.String("error", err.Error()))
			}
		default:
			return
		}
	}
}

// AcquireChannel gets a channel from the pool or creates a new one.
func (c *Connection) AcquireChannel() (AMQPChannel, error) {
	// Try to get from pool first.
	select {
	case ch := <-c.channelPool:
		return ch, nil
	default:
	}

	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()

	if conn == nil || conn.IsClosed() {
		return nil, errcode.New(ErrAdapterAMQPConnect, "rabbitmq: connection not available")
	}

	ch, err := conn.Channel()
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterAMQPConnect, "rabbitmq: open channel", err)
	}

	return ch, nil
}

// ReleaseChannel returns a channel to the pool. If the pool is full, the channel is closed.
func (c *Connection) ReleaseChannel(ch AMQPChannel) {
	select {
	case c.channelPool <- ch:
	default:
		if err := ch.Close(); err != nil {
			slog.Debug("rabbitmq: error closing excess channel",
				slog.String("error", err.Error()))
		}
	}
}

// Health checks if the connection is alive.
func (c *Connection) Health() error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()

	if conn == nil || conn.IsClosed() {
		return errcode.New(ErrAdapterAMQPConnect, "rabbitmq: connection is closed")
	}
	return nil
}

// Close shuts down the connection and drains the channel pool.
func (c *Connection) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.closeCh)
	conn := c.conn
	c.mu.Unlock()

	c.drainChannelPool()

	if conn != nil {
		if err := conn.Close(); err != nil {
			return errcode.Wrap(ErrAdapterAMQPConnect, "rabbitmq: close connection", err)
		}
	}

	slog.Info("rabbitmq: connection closed")
	return nil
}

// WaitConnected blocks until the connection is established or ctx is cancelled.
func (c *Connection) WaitConnected(ctx context.Context) error {
	c.mu.RLock()
	connected := c.connected
	c.mu.RUnlock()

	select {
	case <-connected:
		return nil
	case <-ctx.Done():
		return errcode.Wrap(ErrAdapterAMQPConnect, "rabbitmq: wait for connection cancelled", ctx.Err())
	}
}

// sanitizeURL redacts credentials from the AMQP URL for safe logging.
func sanitizeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "amqp://***"
	}
	if u.User != nil {
		u.User = nil
		// Rebuild with redacted placeholder to avoid URL-encoding of special chars.
		host := u.Host
		u.Host = ""
		return u.Scheme + "://***:***@" + host + u.RequestURI()
	}
	return u.String()
}
