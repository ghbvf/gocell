package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	// nosemgrep: go.lang.security.audit.crypto.math_random.math-random-used // non-crypto reconnect jitter; gosec G404 already silenced at usage sites
	"math/rand/v2"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/ghbvf/gocell/adapters/adapterutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/worker"
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
	ErrAdapterAMQPReconnecting       errcode.Code = "ERR_ADAPTER_AMQP_RECONNECTING"
	ErrAdapterAMQPCloseTimeout       errcode.Code = "ERR_ADAPTER_AMQP_CLOSE_TIMEOUT"
	ErrAdapterAMQPChannelMaxExceeded errcode.Code = "ERR_ADAPTER_AMQP_CHANNEL_MAX_EXCEEDED"
	ErrAdapterAMQPNack               errcode.Code = "ERR_ADAPTER_AMQP_NACK"
)

const (
	// defaultRMQReconnectMaxBackoff is the upper bound for the exponential
	// reconnect backoff delay.
	defaultRMQReconnectMaxBackoff = 30 * time.Second
	// defaultRMQReconnectBaseDelay is the initial delay between reconnect attempts.
	defaultRMQReconnectBaseDelay = 1 * time.Second
	// defaultRMQConfirmTimeout is the per-publish confirm wait deadline.
	defaultRMQConfirmTimeout = 5 * time.Second
	// defaultRMQMaxChannelsPerConn caps in-flight AMQP channels per physical
	// TCP connection. Far below amqp091-go's negotiated channel_max default
	// (2047) so adapter-side fail-fast triggers before the broker shuts the
	// connection.
	//
	// ref: rabbitmq/amqp091-go connection.go openTune (defaultChannelMax = 2<<10-1).
	defaultRMQMaxChannelsPerConn = 256
)

// Pre-allocated Health() errors to avoid per-call allocation.
var (
	errHealthReconnecting   = errcode.New(errcode.KindInternal, ErrAdapterAMQPReconnecting, "rabbitmq: connection lost, reconnecting")
	errHealthNeverConnected = errcode.New(errcode.KindInternal, ErrAdapterAMQPConnect, "rabbitmq: never connected")
	errHealthClosed         = errcode.New(errcode.KindInternal, ErrAdapterAMQPConnect, "rabbitmq: connection is closed")
)

// ConnectionPhase represents the lifecycle state of a Connection.
//
// ref: wagslane/go-rabbitmq connection_manager.go — adopted explicit state tracking
// with RWMutex protection (checkout/checkin pattern). Deviated: uses channel-close
// signaling instead of checkout callbacks.
type ConnectionPhase uint8

const (
	// StateConnecting is the initial state before the first successful connection.
	StateConnecting ConnectionPhase = iota
	// StateConnected means the connection is live and ready for use.
	StateConnected
	// StateDisconnected means the connection was lost and reconnection is in
	// progress. Permanent broker classification (e.g. ErrCredentials after admin
	// revokes the user) is exposed via Connection.permanentErr while staying in
	// this state — Health()/WaitConnected return the permanent error so /readyz
	// flips to 503, but the reconnect goroutine keeps retrying so an operator
	// fix (re-issued credentials / restored vhost) self-heals automatically.
	// See ADR docs/architecture/202605051700-adr-rmq-runtime-permanent-classification.md.
	StateDisconnected
)

// String returns a human-readable label for the connection state.
func (s ConnectionPhase) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateConnected:
		return "connected"
	case StateDisconnected:
		return "disconnected"
	default:
		return fmt.Sprintf("unknown(%d)", s)
	}
}

// MarshalText implements encoding.TextMarshaler so that JSON serialization
// of PoolStats.State produces a human-readable string ("connected") instead
// of a numeric uint8 value.
func (s ConnectionPhase) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}

// ConnectionState is a structured diagnostic snapshot for dashboards and
// operator tooling. LastError is sanitized and must never include AMQP URL
// credentials.
type ConnectionState struct {
	State             ConnectionPhase `json:"state"`
	Message           string          `json:"message"`
	LastError         string          `json:"lastError,omitempty"`
	LastDisconnectAt  time.Time       `json:"lastDisconnectAt,omitempty"`
	ReconnectAttempts int             `json:"reconnectAttempts"`
}

// unwrapDialErr peels one layer of *errcode.Error wrapping (applied by
// connect()) so callers can pass the underlying transport/AMQP error to
// isPermanentDialError. Returns err unchanged when it is not an *errcode.Error
// or when the wrapped Cause is nil.
func unwrapDialErr(err error) error {
	var ecErr *errcode.Error
	if errors.As(err, &ecErr) && ecErr.Unwrap() != nil {
		return ecErr.Unwrap()
	}
	return err
}

// definitivePermanentSentinels are amqp091-go sentinels classified by the
// library itself as protocol-level hard errors (not inferred from socket
// close). Single-hit at runtime is sufficient to promote to permanentErr.
//
// Coverage (amqp091-go types.go:50-77):
//   - ErrSASL                — SASL mechanism mismatch (config issue)
//   - ErrSyntax              — hard protocol error / incompatible encoding
//   - ErrFrame               — frame could not be parsed (protocol mismatch)
//   - ErrCommandInvalid      — broker sent unexpected command (library bug)
//   - ErrUnexpectedFrame     — non-method/heartbeat frame (library bug)
var definitivePermanentSentinels = []error{
	amqp.ErrSASL,
	amqp.ErrSyntax,
	amqp.ErrFrame,
	amqp.ErrCommandInvalid,
	amqp.ErrUnexpectedFrame,
}

// inferredPermanentSentinels are amqp091-go sentinels the library *infers*
// from socket close (no broker close frame), per source comments at
// connection.go:1039-1043 ("we know it's an auth error, but the socket was
// closed instead. Return a meaningful error.") and connection.go:1094-1096
// ("Cannot be closed yet, but we know it's a vhost problem"). A network
// blip mid-handshake will surface as one of these too, so runtime promotion
// requires runtimePermanentConfirmHits consecutive hits to avoid flipping
// /readyz to 503 on a single transient fault.
var inferredPermanentSentinels = []error{
	amqp.ErrCredentials, // openTune L1043 — broker rejected (or socket dropped)
	amqp.ErrVhost,       // openVhost L1096 — vhost denied (or socket dropped)
}

// permanentDialClass categorizes a dial error for runtime classification:
//   - permanentClassNone       — recoverable / unknown (retry)
//   - permanentClassDefinitive — single-hit promote (broker frame, SASL,
//     hard protocol, URI parse, TLS / x509)
//   - permanentClassInferred   — amqp091-go-inferred from socket close;
//     promote only after runtimePermanentConfirmHits consecutive hits
type permanentDialClass int

const (
	permanentClassNone permanentDialClass = iota
	permanentClassDefinitive
	permanentClassInferred
)

// classifyDialError categorizes a dial error.
//
// Classification order (matches must precede mismatches):
//  1. Inferred sentinels (errors.Is amqp.ErrCredentials/ErrVhost) → inferred.
//     Must precede the *amqp.Error structural check; the sentinels have
//     Server=false default-zero and errors.As(...) would otherwise short the
//     structural branch.
//  2. Definitive sentinels (ErrSASL/ErrSyntax/ErrFrame/...) → definitive.
//  3. Broker-emitted *amqp.Error{Server:true, !Recover} → definitive.
//     Server=false is excluded — amqp091-go uses it for transport faults
//     (TCP reset → 501) which are transient broker-restart races.
//  4. *url.Error (URI parse failure) → definitive. Must precede the net.Error
//     check because *url.Error implements net.Error via Timeout/Temporary.
//  5. net.Error (timeout / refused / DNS) → none (recoverable).
//  6. String keyword fallback (TLS / x509 / amqp091-go pre-handshake plain
//     errors) → definitive.
//  7. Default → none.
func classifyDialError(err error) permanentDialClass {
	if err == nil {
		return permanentClassNone
	}
	if matchesAnySentinel(err, inferredPermanentSentinels) {
		return permanentClassInferred
	}
	if matchesAnySentinel(err, definitivePermanentSentinels) {
		return permanentClassDefinitive
	}
	if c, ok := classifyStructuredDialError(err); ok {
		return c
	}
	if matchesPermanentSubstring(err.Error()) {
		return permanentClassDefinitive
	}
	return permanentClassNone
}

// matchesAnySentinel reports whether err wraps any sentinel in the list.
func matchesAnySentinel(err error, sentinels []error) bool {
	for _, s := range sentinels {
		if errors.Is(err, s) {
			return true
		}
	}
	return false
}

// classifyStructuredDialError handles typed-error dispatch (*amqp.Error,
// *url.Error, net.Error). Returns ok=true when it produced a definitive
// answer; ok=false signals "not my type, fall through to string-fallback".
//
// *url.Error must be tried before net.Error: *url.Error satisfies net.Error
// via embedded Timeout/Temporary forwarders, so net.Error-first would silently
// classify URI parse failures as recoverable.
func classifyStructuredDialError(err error) (permanentDialClass, bool) {
	var amqpErr *amqp.Error
	if errors.As(err, &amqpErr) {
		if amqpErr.Server && !amqpErr.Recover {
			return permanentClassDefinitive, true
		}
		return permanentClassNone, true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return permanentClassDefinitive, true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return permanentClassNone, true
	}
	return permanentClassNone, false
}

// matchesPermanentSubstring is the last-resort string-keyword fallback for
// amqp091-go plain errors that don't carry a typed shape (sourced from
// amqp091-go v1.11.0 uri.go / connection.go / tls.go).
func matchesPermanentSubstring(msg string) bool {
	for _, keyword := range permanentDialSubstrings {
		if strings.Contains(msg, keyword) {
			return true
		}
	}
	return false
}

// isPermanentDialError returns true for any non-none classification. Kept as
// a convenience for NewConnection (startup-time fail-fast where the inferred
// vs definitive distinction does not matter — a single hit always aborts).
func isPermanentDialError(err error) bool {
	return classifyDialError(err) != permanentClassNone
}

// permanentDialSubstrings are substrings in amqp091-go plain-error messages
// that indicate permanent dial failures. String matching is the last resort —
// structural checks (amqp.Error, url.Error, net.Error) are tried first.
//
// Coverage from amqp091-go v1.11.0:
//   - "AMQP scheme"             uri.go ParseURI scheme validation
//   - "AMQP URI"                uri.go ParseURI host / port / whitespace
//   - "invalid port"            uri.go ParseURI bad port
//   - "auth mechanism"          connection.go pickSASLMechanism
//   - "x509:"                   crypto/tls — certificate validation
//   - "tls: "                   crypto/tls — handshake / protocol error
var permanentDialSubstrings = []string{
	"AMQP scheme",
	"AMQP URI",
	"invalid port",
	"auth mechanism",
	"x509:",
	"tls: ",
}

// isTerminalConnectionError reports whether the error indicates the Connection
// has entered terminal state (broker-classified permanent dial failure).
// Callers should not retry — close the dependent component.
func isTerminalConnectionError(err error) bool {
	var ecErr *errcode.Error
	if !errors.As(err, &ecErr) {
		return false
	}
	return ecErr.Code == ErrAdapterAMQPConnectPermanent
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

	// MaxChannelsPerConn caps the number of in-flight channels acquired
	// from one Connection. Pool-miss acquisitions check this counter and
	// return ErrAdapterAMQPChannelMaxExceeded when the cap is reached.
	// Default: 256.
	MaxChannelsPerConn int
}

func (c *Config) setDefaults() {
	if c.ReconnectMaxBackoff <= 0 {
		c.ReconnectMaxBackoff = defaultRMQReconnectMaxBackoff
	}
	if c.ReconnectBaseDelay <= 0 {
		c.ReconnectBaseDelay = defaultRMQReconnectBaseDelay
	}
	if c.ChannelPoolSize <= 0 {
		c.ChannelPoolSize = 10
	}
	if c.ConfirmTimeout <= 0 {
		c.ConfirmTimeout = defaultRMQConfirmTimeout
	}
	if c.MaxChannelsPerConn <= 0 {
		c.MaxChannelsPerConn = defaultRMQMaxChannelsPerConn
	}
}

// Compile-time interface check: Connection must satisfy lifecycle.ContextCloser.
//
// ref: ThreeDotsLabs/watermill-amqp pkg/amqp/publisher.go Close signature
// ref: uber-go/fx app.go StopTimeout — ctx propagation pattern
var _ lifecycle.ContextCloser = (*Connection)(nil)

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
	// Cancel issues a basic.cancel to the broker, instructing it to stop
	// delivering new messages to the given consumer. Already-prefetched messages
	// remain in the deliveries channel and can be drained by the subscriber.
	Cancel(consumer string, noWait bool) error
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
// Connection has three lifecycle states (see ConnectionPhase):
//   - connecting:   initial state before first successful dial
//   - connected:    ready for use (connected channel is closed)
//   - disconnected: lost connection, attempting backoff reconnect
//
// Broker-classified permanent errors (e.g. amqp.ErrCredentials, ACCESS_REFUSED
// frames) at runtime do NOT terminate the reconnect goroutine. Instead they
// are recorded in permanentErr and surfaced via Health()/WaitConnected so
// /readyz returns 503; the reconnect loop keeps trying so that operator
// remediation (rotated creds, restored vhost) self-heals on the next dial.
// permanentErr is cleared the moment a dial succeeds. NewConnection-time
// permanent errors still fail-fast (no Connection instance is created).
//
// ref: ThreeDotsLabs/watermill-amqp pkg/amqp/connection.go reconnect — same
// "indefinite retry, surface state to caller" pattern.
type Connection struct {
	config Config
	dial   DialFunc
	clock  clock.Clock

	mu   sync.RWMutex
	conn AMQPConnection

	channelPool chan AMQPChannel

	closeCh chan struct{}
	closed  bool

	// connected is closed when a connection is established, re-created on
	// disconnect or when permanentErr transitions (so WaitConnected wakes and
	// re-evaluates permanentErr instead of waiting on a stale channel).
	connected chan struct{}

	// permanentErr is set when reconnect dial returns an unrecoverable
	// classification (broker-emitted Recover=false / amqp091-go sentinel).
	// Cleared on the next successful reconnect. Protected by mu.
	permanentErr error

	// pendingPermanentHits counts consecutive runtime hits for inferred
	// sentinels (amqp.ErrCredentials / amqp.ErrVhost) which amqp091-go
	// derives from socket close rather than a broker close frame. We require
	// runtimePermanentConfirmHits consecutive hits before promoting to
	// permanentErr so a single transient handshake fault does not flip
	// /readyz to 503 unnecessarily. Reset on any successful dial or
	// non-inferred classification. Protected by mu.
	pendingPermanentHits int

	// state tracks the connection lifecycle for Health() and observability.
	// Protected by mu.
	state             ConnectionPhase
	lastError         string
	lastDisconnectAt  time.Time
	reconnectAttempts int

	// inUseChannels counts channels handed out to callers (subscribers'
	// per-subscription channels + publishers' ephemeral channels) net of
	// returns. Pool-miss path increments before allocation, pool-full /
	// close-path decrement on release. Reads are eventually-consistent
	// snapshot reads — exact-time reads not required for fail-fast.
	inUseChannels atomic.Int32
}

// runtimePermanentConfirmHits is the number of consecutive inferred-sentinel
// hits (amqp.ErrCredentials / amqp.ErrVhost — amqp091-go infers these from
// socket close, see connection.go:1043/1096) required before promoting them
// to permanentErr at runtime. Definitive sentinels (ErrSASL / ErrSyntax /
// ErrFrame / ErrCommandInvalid / ErrUnexpectedFrame) and broker-emitted
// Server=true && Recover=false errors bypass this gate (single hit promotes).
const runtimePermanentConfirmHits = 2

// NewConnection creates a new Connection with the given config.
// It attempts an initial connection and starts the reconnect loop.
// A clock.Clock must be supplied via WithConnectionClock; NewConnection
// panics if no clock is provided (use clock.Real() at the composition root).
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

	clock.MustHaveClock(c.clock, "rabbitmq.NewConnection")

	if err := c.connect(); err != nil {
		// Classify initial connection failure: permanent errors get a distinct code
		// so callers can fail-fast on bad credentials, bad URI, TLS misconfiguration.
		if isPermanentDialError(unwrapDialErr(err)) {
			return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterAMQPConnectPermanent,
				"rabbitmq: initial connection failed (permanent)", c.sanitizeDialError(err))
		}
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterAMQPConnect, "rabbitmq: initial connection failed", c.sanitizeDialError(err))
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

// WithConnectionClock sets the clock used by the Connection for reconnect
// backoff and timeout calculations. Required — NewConnection panics if no
// clock is supplied. Pass clock.Real() at the composition root.
func WithConnectionClock(clk clock.Clock) ConnectionOption {
	return func(c *Connection) {
		c.clock = clk
	}
}

func (c *Connection) connect() error {
	conn, err := c.dial(c.config.URL)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterAMQPConnect, "rabbitmq: dial", err)
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

		closeErr, ok := c.waitForCloseNotification(conn)
		if !ok {
			return
		}

		// RMQ-RACE-01 fix: create a new connected channel BEFORE draining
		// the pool so that any concurrent WaitConnected callers who hold a
		// reference to the old (closed) channel will see a different reference
		// on re-validation and loop back to wait on the new channel.
		c.markDisconnected(closeErr)

		// Drain the channel pool on disconnect.
		c.drainChannelPool()

		if c.reconnectWithBackoff() {
			c.mu.Lock()
			c.state = StateConnected
			close(c.connected)
			c.mu.Unlock()
		} else {
			// reconnectWithBackoff returns false only on closeCh (clean shutdown).
			// Permanent classifications stay in the inner retry loop so an
			// operator fix self-heals on the next successful dial.
			return
		}
	}
}

func (c *Connection) waitForCloseNotification(conn AMQPConnection) (*amqp.Error, bool) {
	closeCh := conn.NotifyClose(make(chan *amqp.Error, 1))
	select {
	case <-c.closeCh:
		return nil, false
	case amqpErr, ok := <-closeCh:
		if !ok {
			return nil, false
		}
		if amqpErr != nil {
			slog.Warn("rabbitmq: connection lost, reconnecting",
				slog.String("error", sanitizeErrorURL(amqpErr.Error(), c.config.URL)))
		}
		return amqpErr, true
	}
}

func (c *Connection) markDisconnected(closeErr *amqp.Error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.connected = make(chan struct{})
	c.state = StateDisconnected
	c.lastDisconnectAt = c.clock.Now().UTC()
	c.reconnectAttempts = 0
	if closeErr != nil {
		c.lastError = sanitizeErrorURL(closeErr.Error(), c.config.URL)
		return
	}
	c.lastError = "connection close notification received"
}

// reconnectWithBackoff attempts to re-establish the connection with capped
// exponential backoff. The loop runs until closeCh fires (Close was called)
// or a dial succeeds — broker-classified permanent errors do NOT terminate
// the loop. Instead they are recorded in permanentErr (markPermanent) so
// Health()/WaitConnected surface them and /readyz returns 503; the next
// successful dial clears permanentErr (markRecovered) and operations resume
// transparently.
//
// Definitive permanent classifications (broker error frames with Server=true,
// SASL / Syntax / Frame / CommandInvalid / UnexpectedFrame sentinels, URI
// parse / TLS) promote on the first hit. Inferred classifications
// (ErrCredentials / ErrVhost — amqp091-go derives these from socket close,
// which a transient handshake fault can also produce) require
// runtimePermanentConfirmHits consecutive hits before promotion to avoid
// flipping /readyz on a single network blip.
//
// Returns true when the connection was successfully re-established, false
// when closeCh fires (clean shutdown). Permanent classifications never
// return false — the goroutine stays alive so an operator fix self-heals.
//
// ref: ThreeDotsLabs/watermill-amqp pkg/amqp/connection.go reconnect — same
// "indefinite retry, surface state to caller" pattern.
// ref: rabbitmq/amqp091-go connection.go Recover / Server fields — broker
// handshake rejections set Server=true; library-inferred sentinels do not.
func (c *Connection) reconnectWithBackoff() bool {
	attempt := 0
	for {
		select {
		case <-c.closeCh:
			return false
		default:
		}

		delay := c.backoffDelay(attempt)
		// Pre-increment semantics: reconnectAttempts reflects "about to attempt
		// reconnect #N" once written, so observers reading ConnectionStatus during
		// the backoff wait already see the upcoming attempt count. This is
		// intentional — operators want "next attempt N due in <delay>" visibility,
		// not "N-1 done and a new one is brewing in the dark".
		c.mu.Lock()
		c.reconnectAttempts = attempt + 1
		c.mu.Unlock()
		slog.Warn("rabbitmq: reconnect attempt",
			slog.Int("attempt", attempt+1),
			slog.Duration("delay", delay))

		t := c.clock.NewTimerAt(c.clock.Now().Add(delay))
		select {
		case <-c.closeCh:
			t.Stop()
			return false
		case <-t.C():
		}

		if err := c.connect(); err != nil {
			sanitizedErr := sanitizeErrorURL(err.Error(), c.config.URL)
			class := classifyDialError(unwrapDialErr(err))

			switch class {
			case permanentClassDefinitive:
				c.markPermanent(err, sanitizedErr)
				slog.Error("rabbitmq: reconnect dial classified permanent (definitive); /readyz will return 503 until recovery",
					slog.String("errCode", string(ErrAdapterAMQPConnectPermanent)),
					slog.Int("attempt", attempt+1),
					slog.String("error", sanitizedErr))
			case permanentClassInferred:
				c.mu.Lock()
				c.pendingPermanentHits++
				hits := c.pendingPermanentHits
				c.lastError = sanitizedErr
				c.mu.Unlock()
				if hits >= runtimePermanentConfirmHits {
					c.markPermanent(err, sanitizedErr)
					slog.Error("rabbitmq: reconnect dial classified permanent (inferred, confirmed); /readyz will return 503 until recovery",
						slog.String("errCode", string(ErrAdapterAMQPConnectPermanent)),
						slog.Int("attempt", attempt+1),
						slog.Int("confirmedHits", hits),
						slog.String("error", sanitizedErr))
				} else {
					slog.Warn("rabbitmq: reconnect failed, awaiting confirmation before classifying permanent",
						slog.Int("attempt", attempt+1),
						slog.Int("pendingHits", hits),
						slog.Int("confirmThreshold", runtimePermanentConfirmHits),
						slog.String("error", sanitizedErr))
				}
			default: // permanentClassNone
				c.mu.Lock()
				c.lastError = sanitizedErr
				c.pendingPermanentHits = 0
				c.mu.Unlock()
				slog.Warn("rabbitmq: reconnect failed, retrying",
					slog.Int("attempt", attempt+1),
					slog.String("error", sanitizedErr))
			}
			attempt++
			continue
		}

		c.markRecovered()
		slog.Info("rabbitmq: reconnected successfully",
			slog.Int("attempts", attempt+1))
		return true
	}
}

// markPermanent records a permanent classification on the connection. It
// wakes any WaitConnected callers by closing-and-replacing the connected
// channel — the wake-and-recheck idiom (see WaitConnected stale-channel
// handling): on wake, callers re-read permanentErr under RLock and, finding
// it non-nil, return it. permanentErr is cleared by markRecovered when a
// later dial succeeds.
func (c *Connection) markPermanent(cause error, sanitizedErr string) {
	c.mu.Lock()
	c.permanentErr = errcode.Wrap(errcode.KindInternal, ErrAdapterAMQPConnectPermanent,
		"rabbitmq: reconnect dial classified permanent", c.sanitizeDialError(cause))
	c.lastError = sanitizedErr
	c.pendingPermanentHits = 0
	old := c.connected
	c.connected = make(chan struct{})
	c.mu.Unlock()
	// Close the old channel to wake any waiters. Use select+default to keep
	// the operation idempotent in case the channel was already closed by an
	// earlier reconnect-success path on the same connection generation.
	select {
	case <-old:
	default:
		close(old)
	}
}

// markRecovered clears any prior permanent classification. Called from the
// reconnect success path so an operator fix (rotated creds, restored vhost)
// self-heals on the next successful dial. The connected channel is closed
// by reconnectLoop after this returns, signaling waiters that the
// connection is live again.
func (c *Connection) markRecovered() {
	c.mu.Lock()
	c.permanentErr = nil
	c.pendingPermanentHits = 0
	c.mu.Unlock()
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
	reduction := rand.Int64N(int64(d)/4 + 1) //nolint:gosec // G404 R2-approved: reconnect down-jitter has no cryptographic requirement
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
	offset := rand.Int64N(jitterRange + 1) //nolint:gosec // G404 R2-approved: reconnect jitter has no cryptographic requirement
	// shift to [-25%, +25%]: subtract 25% of d
	return time.Duration(int64(d) - jitterRange/2 + offset)
}

func (c *Connection) drainChannelPool() {
	for {
		select {
		case ch := <-c.channelPool:
			if err := ch.Close(); err != nil {
				slog.Debug("rabbitmq: error closing pooled channel",
					slog.Any("error", err))
			}
		default:
			return
		}
	}
}

// AcquireChannel gets a channel from the pool or creates a new one.
// Returns the permanent error if the connection is in terminal state.
//
// Invariants:
//   - Pool hit (idle channel returned): inUseChannels unchanged — the broker
//     channel was already counted when it was first allocated.
//   - Pool miss: inUseChannels.Add(1) before allocation; rolled back on cap
//     exceeded or broker error so over-cap slots cannot leak.
//   - MaxChannelsPerConn <= 0: cap check skipped (uncapped). NewConnection's
//     setDefaults populates the field with defaultRMQMaxChannelsPerConn, so
//     this branch only fires when callers build *Connection directly without
//     setDefaults — a test convenience, not a production path.
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

	// Try to get from pool first — pool hit does not change inUseChannels
	// because the channel's broker slot was already counted on first allocation.
	select {
	case ch := <-c.channelPool:
		return ch, nil
	default:
	}

	if conn == nil || conn.IsClosed() {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterAMQPConnect, "rabbitmq: connection not available")
	}

	// Pool miss — increment in-use BEFORE allocating; rollback on cap miss
	// or broker error so over-cap allocations cannot leak counter slots.
	next := c.inUseChannels.Add(1)
	if cap := c.config.MaxChannelsPerConn; cap > 0 && int(next) > cap {
		c.inUseChannels.Add(-1)
		return nil, errcode.New(errcode.KindInternal, ErrAdapterAMQPChannelMaxExceeded,
			"rabbitmq: channel cap reached for connection")
	}

	ch, err := conn.Channel()
	if err != nil {
		c.inUseChannels.Add(-1)
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterAMQPConnect, "rabbitmq: open channel", err)
	}

	return ch, nil
}

// ReleaseChannel returns a channel to the pool. If the pool is full, the
// channel is closed and inUseChannels is decremented.
//
// Invariants (mirror of AcquireChannel):
//   - Pool return: broker channel stays held (pool holds it), inUseChannels
//     unchanged — the slot is still "in use" by the idle pool.
//   - Pool full path: close the excess channel, decrement inUseChannels so the
//     slot is available for the next pool-miss acquisition.
func (c *Connection) ReleaseChannel(ch AMQPChannel) {
	select {
	case c.channelPool <- ch:
		// Returned to pool — broker channel still held; in-use unchanged.
		return
	default:
	}
	// Pool full — close the channel and decrement in-use.
	if err := ch.Close(); err != nil {
		slog.Debug("rabbitmq: error closing excess channel",
			slog.Any("error", err))
	}
	c.inUseChannels.Add(-1)
}

// Health checks if the connection is alive. Returns a distinct error code per
// connection state so operators can tell "never connected" from "reconnecting"
// from "permanent classification recorded".
//
// The ctx parameter is accepted to satisfy the lifecycle.Checker contract
// (func(ctx context.Context) error) used by /readyz integration via
// Connection.Checkers(); this implementation does not perform I/O so ctx is
// only checked for early cancellation before the state read.
//
// Error codes returned:
//   - nil: healthy (StateConnected, live connection, permanentErr cleared)
//   - ErrAdapterAMQPConnectPermanent: a reconnect dial was classified
//     permanent and the broker has not accepted us since. The reconnect
//     goroutine is still trying — if the operator restores creds/vhost, the
//     next successful dial clears this and Health returns nil again.
//   - ErrAdapterAMQPReconnecting: lost connection, backoff reconnect in
//     progress (StateDisconnected) without a recorded permanent
//     classification.
//   - ErrAdapterAMQPConnect: never connected (StateConnecting) or conn
//     closed unexpectedly.
func (c *Connection) Health(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.RLock()
	state := c.state
	conn := c.conn
	permErr := c.permanentErr
	c.mu.RUnlock()

	// permanentErr supersedes the phase: a connection in StateDisconnected
	// with permanentErr set still owes /readyz a 503 because the broker is
	// rejecting us, even though the reconnect goroutine keeps retrying.
	if permErr != nil {
		return permErr
	}
	switch state {
	case StateDisconnected:
		return errHealthReconnecting
	case StateConnecting:
		return errHealthNeverConnected
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
	State           ConnectionPhase `json:"state"`           // current connection lifecycle state
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

// ConnectionStatus returns a structured lifecycle snapshot of the connection.
// Useful for dashboards, structured logging, and operational tooling.
func (c *Connection) ConnectionStatus() ConnectionState {
	c.mu.RLock()
	s := c.connectionStatusLocked()
	c.mu.RUnlock()
	return s
}

func (c *Connection) connectionStatusLocked() ConnectionState {
	return ConnectionState{
		State:             c.state,
		Message:           connectionStateMessage(c.state),
		LastError:         c.lastError,
		LastDisconnectAt:  c.lastDisconnectAt,
		ReconnectAttempts: c.reconnectAttempts,
	}
}

func connectionStateMessage(state ConnectionPhase) string {
	switch state {
	case StateConnected:
		return "connected"
	case StateDisconnected:
		return "reconnecting"
	case StateConnecting:
		return "connecting"
	default:
		return "unknown"
	}
}

// Compile-time assertion: Connection satisfies lifecycle.ManagedResource.
// Composition roots wire the connection via bootstrap.WithManagedResource(conn)
// — the "rabbitmq_ready" probe is exposed automatically.
var _ lifecycle.ManagedResource = (*Connection)(nil)

// Checkers returns the rabbitmq_ready probe for /readyz integration.
//
// The probe wraps Health(ctx) which honors ctx (early cancel) and reads the
// in-memory state machine fed by NotifyClose. No broker round-trip per probe —
// that would amplify load on every /readyz hit. Liveness vs readiness signals
// come from the reconnect loop's NotifyClose feedback.
//
// The probe name carries the _ready suffix for parity with sibling adapter
// probes (vault_transit_ready, etc.); operator dashboards and alert rules
// consuming /readyz?verbose dependencies must reference the suffixed name.
//
// ref: kernel/lifecycle/managed_resource.go::Checkers — contract: nil = healthy.
// ref: adapters/vault/transit_provider.go::Checkers — sibling adapter pattern.
func (c *Connection) Checkers() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"rabbitmq_ready": c.Health,
	}
}

// Worker returns nil — the RabbitMQ reconnect goroutine is started inside
// NewConnection and managed by closeCh, not via the ManagedResource worker
// contract. Returning nil is the documented "no background worker" signal
// in lifecycle.ManagedResource.
//
// ref: kernel/lifecycle/managed_resource.go::Worker — nil documents "no worker".
func (c *Connection) Worker() worker.Worker {
	return nil
}

// Close shuts down the connection, bounded by ctx.
//
// It signals closeCh (stopping the reconnect loop), drains the channel pool,
// then closes the underlying AMQP connection in a goroutine so that ctx
// expiry is honored even if the broker handshake takes longer than the
// caller's budget allows.
//
// closeCh signaling and channel-pool drain run unconditionally, even when
// ctx is already canceled — these are local state-machine transitions that
// must happen on every Close to prevent the reconnect loop from leaking
// past process-shutdown. Only the AMQP network handshake is gated by ctx
// via adapterutil.CloseWithDeadline.
//
// Close is idempotent: a second call returns nil immediately.
//
// ref: uber-go/fx app.go StopTimeout — ctx carries the shared shutdown budget.
// ref: ThreeDotsLabs/watermill-amqp pkg/amqp/publisher.go Close — closeCh signal
// then conn.Close() with the caller's budget.
// ref: rabbitmq/amqp091-go channel.go Close — IsClosed short-circuit pattern.
func (c *Connection) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.closeCh)
	conn := c.conn
	status := c.connectionStatusLocked()
	idleChannels := len(c.channelPool)
	c.mu.Unlock()

	fields := []slog.Attr{
		slog.String("reason", "managed_resource_close"),
		slog.String("state", status.State.String()),
		slog.String("message", status.Message),
		slog.Int("reconnectAttempts", status.ReconnectAttempts),
		slog.Int("idleChannels", idleChannels),
	}
	if status.LastError != "" {
		fields = append(fields, slog.String("lastError", status.LastError))
	}
	if !status.LastDisconnectAt.IsZero() {
		fields = append(fields, slog.Time("lastDisconnectAt", status.LastDisconnectAt))
	}
	slog.LogAttrs(ctx, slog.LevelInfo, "rabbitmq: closing connection", fields...)

	c.drainChannelPool()

	// conn.Close() performs a network handshake (AMQP connection.close
	// frame exchange) and may block. The helper runs it in a goroutine so
	// the caller's context budget is honored. A pre-canceled ctx makes
	// the helper return ctx.Err() without dialing the broker — closeCh and
	// the channel pool above have already been cleaned up.
	return adapterutil.CloseWithDeadline(ctx, "rabbitmq", func() error {
		if conn == nil {
			return nil
		}
		if err := conn.Close(); err != nil {
			return errcode.Wrap(errcode.KindInternal, ErrAdapterAMQPConnect, "rabbitmq: close connection", err)
		}
		return nil
	})
}

// WaitConnected blocks until the connection is established, a permanent
// error has been recorded, or ctx is canceled.
//
// The re-validation loop detects stale channel references caused by concurrent
// reconnectLoop / markPermanent activity (RMQ-RACE-01 fix). markPermanent
// closes-and-replaces c.connected to wake waiters; markRecovered+reconnect
// success closes the (replacement) channel after permanentErr is cleared.
//
// ref: go-micro broker/rabbitmq connection.go — adopted channel recreation under
// mutex + wake-and-recheck pattern (condition variable idiom).
//
// Returns nil on successful connection, or an error:
//   - ErrAdapterAMQPConnectPermanent: a reconnect dial was classified
//     permanent and the broker has not accepted us since (typically broker-
//     emitted Recover=false / inferred ErrCredentials/ErrVhost / hard
//     protocol sentinel). Subscribers/publishers should propagate this so
//     EventRouter can retry at the right cadence; the underlying reconnect
//     goroutine keeps trying so a later operator fix self-heals.
//   - ErrAdapterAMQPConnect wrapping ctx.Err(): caller's deadline/cancel.
//     May retry with a fresh context.
func (c *Connection) WaitConnected(ctx context.Context) error {
	for {
		c.mu.RLock()
		connected := c.connected
		c.mu.RUnlock()

		select {
		case <-connected:
			// RMQ-RACE-01 re-validation: the channel we selected on may be
			// stale if reconnectLoop / markPermanent / markDisconnected
			// replaced it between our RLock and the select. Re-read under
			// lock and verify the reference matches.
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
		case <-ctx.Done():
			return errcode.Wrap(errcode.KindInternal, ErrAdapterAMQPConnect, "rabbitmq: wait for connection canceled", ctx.Err())
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
//
// fail-closed: discards both userinfo and the entire query string. RabbitMQ
// AMQP URI query parameters can carry credentials (`?password=...`) and TLS
// material (`?cacertfile=...`, `?certfile=...`, `?keyfile=...`); a per-key
// allowlist would be a moving target as upstream adds parameters, so we
// drop the lot. Path is preserved (it carries the vhost and is not
// considered sensitive in RabbitMQ deployments).
//
// ref: https://www.rabbitmq.com/docs/uri-query-parameters
func sanitizeURL(raw string) string {
	if raw == "" {
		return "amqp://***"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "amqp://***"
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "amqp"
	}
	host := u.Host
	path := u.EscapedPath()
	if u.User != nil {
		return scheme + "://***:***@" + host + path
	}
	if u.RawQuery != "" || u.Fragment != "" {
		// No userinfo but query/fragment present — still strip them in case
		// they carry secrets (e.g. ?password=).
		return scheme + "://" + host + path
	}
	return u.String()
}
