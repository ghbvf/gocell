package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/url"
	"strings"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Error codes for the RabbitMQ adapter.
const (
	ErrAdapterAMQPConnect            errcode.Code = "ERR_ADAPTER_AMQP_CONNECT"
	ErrAdapterAMQPConnectPermanent   errcode.Code = "ERR_ADAPTER_AMQP_CONNECT_PERMANENT"
	ErrAdapterAMQPPublish            errcode.Code = "ERR_ADAPTER_AMQP_PUBLISH"
	ErrAdapterAMQPConfirmTimeout     errcode.Code = "ERR_ADAPTER_AMQP_CONFIRM_TIMEOUT"
	ErrAdapterAMQPSubscribe          errcode.Code = "ERR_ADAPTER_AMQP_SUBSCRIBE"
	ErrAdapterAMQPConsume            errcode.Code = "ERR_ADAPTER_AMQP_CONSUME"
	ErrAdapterAMQPReconnectExhausted errcode.Code = "ERR_ADAPTER_AMQP_RECONNECT_EXHAUSTED"
	ErrAdapterAMQPReconnecting       errcode.Code = "ERR_ADAPTER_AMQP_RECONNECTING"
)

// Pre-allocated Health() errors to avoid per-call allocation.
var (
	errHealthReconnecting   = errcode.New(ErrAdapterAMQPReconnecting, "rabbitmq: connection lost, reconnecting")
	errHealthNeverConnected = errcode.New(ErrAdapterAMQPConnect, "rabbitmq: never connected")
	errHealthClosed         = errcode.New(ErrAdapterAMQPConnect, "rabbitmq: connection is closed")
)

// ConnectionState represents the lifecycle state of a Connection.
//
// ref: wagslane/go-rabbitmq connection_manager.go — adopted explicit state tracking
// with RWMutex protection (checkout/checkin pattern). Deviated: uses channel-close
// signaling instead of checkout callbacks.
type ConnectionState uint8

const (
	// StateConnecting is the initial state before the first successful connection.
	StateConnecting ConnectionState = iota
	// StateConnected means the connection is live and ready for use.
	StateConnected
	// StateDisconnected means the connection was lost and reconnection is in progress.
	StateDisconnected
	// StateTerminal means a permanent error was encountered; no further reconnects.
	StateTerminal
)

// String returns a human-readable label for the connection state.
func (s ConnectionState) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	case StateDisconnected:
		return "disconnected"
	case StateTerminal:
		return "terminal"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}

// MarshalText implements encoding.TextMarshaler so that JSON serialization
// of PoolStats.State produces a human-readable string ("connected") instead
// of a numeric uint8 value.
func (s ConnectionState) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}

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

// isTerminalConnectionError reports whether the error indicates the Connection
// has entered terminal state (permanent dial failure or reconnect attempts
// exhausted). Callers should not retry — close the dependent component.
func isTerminalConnectionError(err error) bool {
	var ecErr *errcode.Error
	if !errors.As(err, &ecErr) {
		return false
	}
	return ecErr.Code == ErrAdapterAMQPConnectPermanent ||
		ecErr.Code == ErrAdapterAMQPReconnectExhausted
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

	// ChannelPoolSize is the maximum number of idle channels in the subscriber
	// pool. Publisher uses ephemeral channels (not pooled).
	// Default: 10.
	ChannelPoolSize int

	// ConfirmTimeout is the timeout for publisher confirm mode.
	// Default: 5s.
	ConfirmTimeout time.Duration

	// MaxReconnectAttempts is retained for field compatibility but ignored.
	// Runtime reconnect is always unbounded (A.1 semantics): once a Connection
	// has successfully established at least once, loss of connectivity triggers
	// indefinite retry with capped exponential backoff until Close() is called
	// or the connection recovers.
	//
	// Operational recovery escape hatch: orchestrators (k8s readinessProbe
	// against /readyz) restart the pod if reconnect cannot succeed within the
	// deployment's SLO, rather than the Connection self-terminating.
	//
	// Permanent errors (bad credentials, TLS, URI parse) are detected at
	// NewConnection time — fail-fast at startup — and do not apply at runtime.
	//
	// ref: ThreeDotsLabs/watermill-amqp ConnectionWrapper.reconnect uses
	// backoff.Retry() with no attempt cap; it stops only on Close() or
	// backoff.Permanent() wrapping of the sentinel close condition.
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
// Connection has four lifecycle states (see ConnectionState):
//   - connecting:   initial state before first successful dial
//   - connected:    ready for use (connected channel is closed)
//   - disconnected: lost connection, attempting backoff reconnect
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

	// state tracks the connection lifecycle for Health() and observability.
	// Protected by mu.
	state ConnectionState
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
			return nil, errcode.Wrap(ErrAdapterAMQPConnectPermanent, "rabbitmq: initial connection failed (permanent)", c.sanitizeDialError(err))
		}
		return nil, errcode.Wrap(ErrAdapterAMQPConnect, "rabbitmq: initial connection failed", c.sanitizeDialError(err))
	}

	c.mu.Lock()
	c.state = StateConnected
	c.mu.Unlock()
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

		// RMQ-RACE-01 fix: create a new connected channel BEFORE draining
		// the pool so that any concurrent WaitConnected callers who hold a
		// reference to the old (closed) channel will see a different reference
		// on re-validation and loop back to wait on the new channel.
		c.mu.Lock()
		c.connected = make(chan struct{})
		c.state = StateDisconnected
		c.mu.Unlock()

		// Drain the channel pool on disconnect.
		c.drainChannelPool()

		if c.reconnectWithBackoff() {
			c.mu.Lock()
			c.state = StateConnected
			close(c.connected)
			c.mu.Unlock()
		} else {
			// closeCh fired — clean shutdown.
			return
		}
	}
}

// reconnectWithBackoff attempts to re-establish the connection with capped
// exponential backoff. It retries indefinitely until the connection is
// recovered or Close() is called (closeCh fired).
//
// A.1 semantics (post-PR#173): transient dial errors during reconnect never
// cause the connection to give up. Permanent errors (bad credentials, TLS,
// URI) surface only at NewConnection time and fail fast at startup; once the
// Connection has successfully established at least once, we assume operational
// recovery is possible and keep trying. Operators rely on k8s readinessProbe
// (via /readyz → Health() == errHealthReconnecting) to restart the pod if the
// outage exceeds the deployment's SLO.
//
// Returns true when the connection was successfully re-established, false when
// closeCh fired (clean shutdown).
//
// ref: ThreeDotsLabs/watermill-amqp ConnectionWrapper.reconnect — same
// unbounded retry pattern, stops only on Closed() or backoff.Permanent().
func (c *Connection) reconnectWithBackoff() bool {
	attempt := 0
	for {
		select {
		case <-c.closeCh:
			return false
		default:
		}

		delay := c.backoffDelay(attempt)
		slog.Warn("rabbitmq: reconnect attempt",
			slog.Int("attempt", attempt+1),
			slog.Duration("delay", delay))

		select {
		case <-c.closeCh:
			return false
		case <-time.After(delay):
		}

		if err := c.connect(); err != nil {
			slog.Warn("rabbitmq: reconnect failed, retrying indefinitely",
				slog.Int("attempt", attempt+1),
				slog.String("error", sanitizeErrorURL(err.Error(), c.config.URL)))
			attempt++
			continue
		}

		slog.Info("rabbitmq: reconnected successfully",
			slog.Int("attempts", attempt+1))
		return true
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
	delay := outbox.ExponentialDelay(c.config.ReconnectBaseDelay, c.config.ReconnectMaxBackoff, attempt)
	if delay >= c.config.ReconnectMaxBackoff {
		return addDownJitter(c.config.ReconnectMaxBackoff)
	}

	// Uncapped region: jitter on actual delay. Cap any overshoot from +25%.
	withJitter := addJitter(delay)
	if withJitter > c.config.ReconnectMaxBackoff {
		return c.config.ReconnectMaxBackoff
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

// Health checks if the connection is alive. Returns a distinct error code per
// connection state so operators can tell "never connected" from "reconnecting"
// from "terminal".
//
// The ctx parameter is accepted for interface compatibility (e.g. bootstrap.BrokerHealthChecker)
// and to honour caller cancellation; this implementation does not perform I/O
// so ctx is only checked for early cancellation before the state read.
//
// Error codes returned:
//   - nil: healthy (StateConnected, live connection)
//   - ErrAdapterAMQPConnect: never connected (StateConnecting) or conn closed unexpectedly
//   - ErrAdapterAMQPReconnecting: lost connection, backoff reconnect in progress (StateDisconnected)
//   - ErrAdapterAMQPConnectPermanent / ErrAdapterAMQPReconnectExhausted: terminal, will not recover
func (c *Connection) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.RLock()
	state := c.state
	conn := c.conn
	permErr := c.permanentErr
	c.mu.RUnlock()

	// StateTerminal: permanent error was recorded — return it directly.
	// This covers ErrAdapterAMQPConnectPermanent and ErrAdapterAMQPReconnectExhausted.
	if permErr != nil {
		return permErr
	}
	switch state {
	case StateDisconnected:
		return errHealthReconnecting
	case StateConnecting:
		return errHealthNeverConnected
	case StateTerminal:
		// Defensive: permErr should be non-nil for terminal state (checked above).
		// If we reach here, it's an internal invariant violation.
		return errcode.New(ErrAdapterAMQPConnect, "rabbitmq: terminal state without permanent error")
	case StateConnected:
		// Fall through to conn.IsClosed() check below.
	}
	if conn == nil || conn.IsClosed() {
		return errHealthClosed
	}
	return nil
}

// PoolStats holds structured channel pool statistics.
//
// The channel pool is used by Subscriber only. Publisher uses ephemeral
// channels (open, confirm, publish, close) that bypass the pool entirely,
// so IdleChannels does not reflect publisher channel activity.
type PoolStats struct {
	ChannelPoolSize int             `json:"channelPoolSize"` // configured pool capacity (subscriber only)
	IdleChannels    int             `json:"idleChannels"`    // channels currently idle in pool (subscriber only)
	State           ConnectionState `json:"state"`           // current connection lifecycle state
}

// PoolStats returns structured pool statistics suitable for metrics collection
// and operational dashboards. See PoolStats type doc for scope limitations.
func (c *Connection) PoolStats() PoolStats {
	c.mu.RLock()
	state := c.state
	c.mu.RUnlock()
	return PoolStats{
		ChannelPoolSize: cap(c.channelPool),
		IdleChannels:    len(c.channelPool),
		State:           state,
	}
}

// ConnectionStatus returns the current lifecycle state of the connection.
// Useful for dashboards, structured logging, and operational tooling.
func (c *Connection) ConnectionStatus() ConnectionState {
	c.mu.RLock()
	s := c.state
	c.mu.RUnlock()
	return s
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
// The re-validation loop detects stale channel references caused by concurrent
// reconnectLoop activity (RMQ-RACE-01 fix).
//
// ref: go-micro broker/rabbitmq connection.go — adopted channel recreation under
// mutex + wake-and-recheck pattern (condition variable idiom).
//
// Returns nil on successful connection, or an error:
//   - ErrAdapterAMQPConnectPermanent: terminal state due to unrecoverable
//     condition (bad credentials, TLS failure). Do NOT retry.
//   - ErrAdapterAMQPReconnectExhausted: terminal state because
//     MaxReconnectAttempts was exceeded. May recover after Pod restart
//     with fresh config/network.
//   - ErrAdapterAMQPConnect wrapping ctx.Err(): caller's deadline/cancel.
//     May retry with a fresh context.
func (c *Connection) WaitConnected(ctx context.Context) error {
	for {
		c.mu.RLock()
		connected := c.connected
		terminalCh := c.terminalCh
		c.mu.RUnlock()

		select {
		case <-connected:
			// RMQ-RACE-01 re-validation: the channel we selected on may be
			// stale if reconnectLoop replaced it between our RLock and the
			// select. Re-read under lock and verify the reference matches.
			c.mu.RLock()
			sameRef := (c.connected == connected)
			permErr := c.permanentErr
			c.mu.RUnlock()
			if permErr != nil {
				return permErr
			}
			if sameRef {
				return nil // same channel — genuinely connected
			}
			continue // stale channel, loop back to re-read
		case <-terminalCh:
			c.mu.RLock()
			err := c.permanentErr
			c.mu.RUnlock()
			return err
		case <-ctx.Done():
			return errcode.Wrap(ErrAdapterAMQPConnect, "rabbitmq: wait for connection cancelled", ctx.Err())
		}
	}
}

// sanitizeDialError wraps a dial error with the URL credentials redacted
// so the error chain cannot leak credentials when .Error() is called upstream.
// Returns the original error unchanged if the URL is empty or not found in the
// error string. The returned error is a plain fmt.Errorf (losing type info),
// which is acceptable because isPermanentDialError classification happens BEFORE
// this sanitization, and the outer errcode.Wrap provides the error code.
func (c *Connection) sanitizeDialError(err error) error {
	if err == nil || c.config.URL == "" {
		return err
	}
	sanitized := sanitizeErrorURL(err.Error(), c.config.URL)
	if sanitized == err.Error() {
		return err // no URL found in error, return as-is
	}
	return fmt.Errorf("%s", sanitized)
}

// sanitizeErrorURL replaces any occurrence of rawURL in errStr with
// the redacted form, preventing credential leaks in log messages.
func sanitizeErrorURL(errStr, rawURL string) string {
	return strings.ReplaceAll(errStr, rawURL, sanitizeURL(rawURL))
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
