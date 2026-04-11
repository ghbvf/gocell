package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/bits"
	"math/rand/v2"
	"net"
	"strings"
	"net/url"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Error codes for the RabbitMQ adapter.
const (
	ErrAdapterAMQPConnect          errcode.Code = "ERR_ADAPTER_AMQP_CONNECT"
	ErrAdapterAMQPConnectPermanent errcode.Code = "ERR_ADAPTER_AMQP_CONNECT_PERMANENT"
	ErrAdapterAMQPPublish          errcode.Code = "ERR_ADAPTER_AMQP_PUBLISH"
	ErrAdapterAMQPConfirmTimeout   errcode.Code = "ERR_ADAPTER_AMQP_CONFIRM_TIMEOUT"
	ErrAdapterAMQPSubscribe        errcode.Code = "ERR_ADAPTER_AMQP_SUBSCRIBE"
	ErrAdapterAMQPConsume          errcode.Code = "ERR_ADAPTER_AMQP_CONSUME"
)

// isPermanentDialError returns true if the error from Dial indicates a
// permanent condition that will not resolve by retrying.
//
// Classification strategy (structured first, string fallback):
//  1. *amqp.Error with Recover=false → permanent (broker handshake rejection)
//  2. net.Error → recoverable (network-level: timeout, refused, DNS)
//  3. *url.Error → permanent (URI parse failure, structural)
//  4. String keyword fallback → permanent for known amqp091-go plain errors
//  5. Default → recoverable (avoid false-positive abort)
//
// ref: rabbitmq/amqp091-go README — reconnection is delegated to the caller;
// amqp091-go surfaces *amqp.Error for handshake issues and plain errors for
// pre-handshake failures (URI parse, auth mechanism, TLS).
func isPermanentDialError(err error) bool {
	if err == nil {
		return false
	}

	// 1. AMQP protocol errors from the broker handshake.
	// Recover=false: 403 ACCESS_REFUSED, 404 NOT_FOUND, 530 NOT_ALLOWED.
	var amqpErr *amqp.Error
	if errors.As(err, &amqpErr) {
		return !amqpErr.Recover
	}

	// 2. Network-level errors are recoverable (timeout, refused, DNS).
	var netErr net.Error
	if errors.As(err, &netErr) {
		return false
	}

	// 3. URL parse errors are structural/permanent.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}

	// 4. String keyword fallback for amqp091-go plain errors that don't
	// implement a typed error. These are pre-handshake configuration errors
	// (sourced from amqp091-go v1.10.0: uri.go, connection.go, tls.go).
	msg := err.Error()
	for _, keyword := range permanentDialSubstrings {
		if strings.Contains(msg, keyword) {
			return true
		}
	}

	// 5. Unknown errors default to recoverable to avoid false-positive abort.
	return false
}

// permanentDialSubstrings are substrings in amqp091-go plain-error messages
// that indicate permanent dial failures. String matching is the last resort —
// structural checks (amqp.Error, net.Error, url.Error) are tried first.
var permanentDialSubstrings = []string{
	"AMQP URI",       // amqp.ParseURI → malformed URI
	"auth mechanism", // connection.go → unsupported SASL mechanism
	"x509:",          // crypto/tls → certificate validation failure
	"tls: ",          // crypto/tls → handshake / protocol error
}

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

	// MaxReconnectAttempts limits the number of reconnection attempts after
	// a connection is lost. 0 (default) means unlimited reconnection.
	// When the limit is reached, the connection enters terminal state.
	MaxReconnectAttempts int
}

func (c *Config) setDefaults() {
	if c.ReconnectMaxBackoff <= 0 {
		c.ReconnectMaxBackoff = 30 * time.Second
	}
	if c.ReconnectBaseDelay <= 0 {
		c.ReconnectBaseDelay = 1 * time.Second
	}
	if c.ChannelPoolSize <= 0 {
		c.ChannelPoolSize = 10
	}
	if c.ConfirmTimeout <= 0 {
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
//
// Connection has three states:
//   - connected:    ready for use (connected channel is closed)
//   - reconnecting: lost connection, attempting backoff reconnect
//   - terminal:     permanent error, will not reconnect (terminalCh is closed)
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

	// terminalCh is closed when a permanent dial error is encountered.
	// permanentErr holds the error for callers to inspect.
	terminalCh   chan struct{}
	permanentErr error
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
		terminalCh:  make(chan struct{}),
	}

	for _, opt := range opts {
		opt(c)
	}

	if err := c.connect(); err != nil {
		// Classify initial connection failure: permanent errors get a distinct code
		// so callers can fail-fast on bad credentials, bad URI, TLS misconfiguration.
		var ecErr *errcode.Error
		dialErr := err
		if errors.As(err, &ecErr) && ecErr.Unwrap() != nil {
			dialErr = ecErr.Unwrap()
		}
		if isPermanentDialError(dialErr) {
			return nil, errcode.Wrap(ErrAdapterAMQPConnectPermanent, "rabbitmq: initial connection failed (permanent)", err)
		}
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

		ok, permErr := c.reconnectWithBackoff()
		if ok {
			c.mu.Lock()
			close(c.connected)
			c.mu.Unlock()
		} else if permErr != nil {
			// Terminal state: permanent dial error. Signal all WaitConnected callers.
			c.mu.Lock()
			c.permanentErr = permErr
			close(c.terminalCh)
			c.mu.Unlock()
			return
		} else {
			// closeCh fired — clean shutdown, no terminal error.
			return
		}
	}
}

// reconnectWithBackoff attempts to re-establish the connection with exponential
// backoff. Returns (true, nil) on success, (false, err) on permanent failure,
// or (false, nil) if closeCh fired (clean shutdown).
func (c *Connection) reconnectWithBackoff() (bool, error) {
	attempt := 0
	for {
		select {
		case <-c.closeCh:
			return false, nil
		default:
		}

		delay := c.backoffDelay(attempt)
		slog.Warn("rabbitmq: reconnect attempt",
			slog.Int("attempt", attempt+1),
			slog.Duration("delay", delay))

		select {
		case <-c.closeCh:
			return false, nil
		case <-time.After(delay):
		}

		if err := c.connect(); err != nil {
			// Unwrap the errcode wrapper to inspect the underlying dial error.
			var ecErr *errcode.Error
			dialErr := err
			if errors.As(err, &ecErr) && ecErr.Unwrap() != nil {
				dialErr = ecErr.Unwrap()
			}

			if isPermanentDialError(dialErr) {
				permErr := errcode.Wrap(ErrAdapterAMQPConnectPermanent,
					"rabbitmq: permanent connection error, giving up", err)
				slog.Error("rabbitmq: permanent connection error, giving up",
					slog.Int("attempt", attempt+1),
					slog.String("error", err.Error()))
				return false, permErr
			}

			slog.Warn("rabbitmq: reconnect failed (recoverable), will retry",
				slog.Int("attempt", attempt+1),
				slog.String("error", err.Error()))
			attempt++
			if c.config.MaxReconnectAttempts > 0 && attempt >= c.config.MaxReconnectAttempts {
				slog.Error("rabbitmq: max reconnect attempts exceeded, entering terminal state",
					slog.Int("max_attempts", c.config.MaxReconnectAttempts),
					slog.Int("attempt", attempt),
					slog.String("error", err.Error()))
				return false, errcode.Wrap(ErrAdapterAMQPConnectPermanent,
					fmt.Sprintf("rabbitmq: max reconnect attempts (%d) exceeded", c.config.MaxReconnectAttempts), err)
			}
			continue
		}

		slog.Info("rabbitmq: reconnected successfully",
			slog.Int("attempts", attempt+1))
		return true, nil
	}
}

// backoffDelay calculates the reconnect delay for the given attempt using
// exponential backoff (base * 2^attempt) with +-25% jitter.
//
// When the exponential value reaches or exceeds ReconnectMaxBackoff, jitter
// is applied to ReconnectMaxBackoff itself (not the uncapped value), so the
// capped result is always in [0.75*max, max]. This prevents thundering-herd
// at the cap while keeping ReconnectMaxBackoff as a true upper bound.
func (c *Connection) backoffDelay(attempt int) time.Duration {
	maxBackoff := c.config.ReconnectMaxBackoff
	base := c.config.ReconnectBaseDelay

	// Compute max safe exponent: 63 - bits needed to represent base.
	// This adapts to any ReconnectBaseDelay (1ns → exp 62, 1s → exp 33).
	maxSafeShift := 63 - bits.Len64(uint64(base))
	if attempt > maxSafeShift {
		return addDownJitter(maxBackoff)
	}

	delay := base * time.Duration(1<<uint(attempt))
	if delay <= 0 { // overflow guard
		return addDownJitter(maxBackoff)
	}

	if delay >= maxBackoff {
		// Capped region: apply downward-only jitter [0.75*max, max] to prevent
		// thundering-herd while keeping maxBackoff as a true upper bound.
		return addDownJitter(maxBackoff)
	}

	// Uncapped region: jitter on actual delay. Cap any overshoot from +25%.
	withJitter := addJitter(delay)
	if withJitter > maxBackoff {
		return maxBackoff
	}
	return withJitter
}

// addDownJitter applies 0-25% downward jitter to a duration.
// The result is in the range [0.75*d, d]. Used when d is already at the
// maximum allowed value so the result never exceeds the cap.
func addDownJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	// Remove up to 25% of d.
	reduction := rand.Int64N(int64(d)/4 + 1)
	return d - time.Duration(reduction)
}

// addJitter applies +-25% random jitter to a duration.
// The result is in the range [0.75*d, 1.25*d].
func addJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	// jitter range: 50% of d (from -25% to +25%)
	jitterRange := int64(d) / 2
	// offset: random value in [0, jitterRange]
	offset := rand.Int64N(jitterRange + 1)
	// shift to [-25%, +25%]: subtract 25% of d
	return time.Duration(int64(d) - jitterRange/2 + offset)
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
// Returns the permanent error if the connection is in terminal state.
func (c *Connection) AcquireChannel() (AMQPChannel, error) {
	c.mu.RLock()
	permErr := c.permanentErr
	conn := c.conn
	c.mu.RUnlock()

	// Terminal state: return permanent error so callers (Publisher, Subscriber)
	// get a consistent error code instead of generic "connection not available".
	if permErr != nil {
		return nil, permErr
	}

	// Try to get from pool first.
	select {
	case ch := <-c.channelPool:
		return ch, nil
	default:
	}

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

// Health checks if the connection is alive. Returns a permanent error if the
// connection entered terminal state (e.g., credential revocation).
func (c *Connection) Health() error {
	c.mu.RLock()
	conn := c.conn
	permErr := c.permanentErr
	c.mu.RUnlock()

	if permErr != nil {
		return permErr
	}
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

// WaitConnected blocks until the connection is established, a permanent error
// occurs, or ctx is cancelled.
//
// Returns nil on successful connection, or an error:
//   - ErrAdapterAMQPConnectPermanent: terminal state (bad credentials, TLS
//     failure, etc). Do NOT retry — close the dependent component.
//   - ErrAdapterAMQPConnect wrapping ctx.Err(): caller's deadline/cancel fired.
//     May retry with a fresh context if the cause was transient.
func (c *Connection) WaitConnected(ctx context.Context) error {
	c.mu.RLock()
	connected := c.connected
	terminalCh := c.terminalCh
	c.mu.RUnlock()

	select {
	case <-connected:
		return nil
	case <-terminalCh:
		c.mu.RLock()
		err := c.permanentErr
		c.mu.RUnlock()
		return err
	case <-ctx.Done():
		return errcode.Wrap(ErrAdapterAMQPConnect, "rabbitmq: wait for connection cancelled", ctx.Err())
	}
}

// sanitizeURL redacts credentials from the AMQP URL for safe logging.
func sanitizeURL(raw string) string {
	if raw == "" {
		return "amqp://***"
	}
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
