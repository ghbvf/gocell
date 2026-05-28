package mqtt

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"

	"github.com/ghbvf/gocell/adapters/adapterutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// connPhase encodes the current connectivity state of the Connection.
type connPhase uint8

const (
	// phaseConnecting is the initial state: no successful connection yet.
	phaseConnecting connPhase = iota
	// phaseConnected means the most recent autopaho lifecycle event was OnConnectionUp.
	phaseConnected
	// phaseDisconnected means OnConnectionDown fired; autopaho will reconnect.
	phaseDisconnected
)

// Compile-time interface assertions.
var (
	_ lifecycle.ManagedResource = (*Connection)(nil)
)

// bootstrapOutcome carries the result of the first connection attempt from the
// autopaho callback goroutine back to Open. Exactly one of `connected` or
// `permErr` is set per outcome; the buffered(1) outcomeCh ensures the
// receiving side observes exactly one outcome (additional Up/Error events
// after the first one are observed via lastError / permanentErr fields and
// reconnect metrics, not via outcomeCh).
//
// This single-channel design replaces the prior "connectedCh closed on
// success / bootstrapErrCh sent on error" two-channel state machine which
// suffered a `select` race where bootstrap-fatal errors could be silently
// swallowed because `<-connectedCh` could win against `<-bootstrapErrCh`.
type bootstrapOutcome struct {
	connected bool
	permErr   error // non-nil → bootstrap-fatal CONNACK or first-attempt permanent rejection
}

// Connection wraps an autopaho.ConnectionManager and exposes a GoCell adapter
// lifecycle (healthz probe, structured errors, redaction-safe logging). autopaho
// owns reconnection; this type only injects ReconnectBackoff and wires the three
// callbacks into a small mutex-guarded state machine.
//
// ref: eclipse/paho.golang autopaho/auto.go — NewConnection, ClientConfig.
// ref: adapters/rabbitmq/connection.go reconnect wrap pattern.
type Connection struct {
	cfg       Config
	clk       clock.Clock         // reserved for clock-based health timeouts (used from PR-3)
	collector ConnectionCollector // optional; nil → no-op

	cm *autopaho.ConnectionManager

	mu           sync.RWMutex
	phase        connPhase
	closed       bool
	lastError    string        // redacted; for slog diagnostics
	stateCh      chan struct{} // closed on every phase transition; recreated by the writer
	permanentErr error         // set on classPermanentRetain; cleared on OnConnectionUp
	closeCh      chan struct{} // closed once when Connection.Close() is called

	// outcomeCh receives the first bootstrap outcome (connected OR permErr).
	// Buffered(1) so callbacks never block; only the first sender wins via a
	// non-blocking select-default. Subsequent OnConnectionUp / OnConnectError
	// events do NOT write to outcomeCh — they update state fields directly
	// and broadcast via stateCh.
	outcomeCh chan bootstrapOutcome

	// firstOutcomeSent guards outcomeCh against multiple bootstrap sends.
	// Read/written under mu.
	firstOutcomeSent bool

	// firstUp tracks whether the first OnConnectionUp has fired (for reconnect
	// metric: the very first successful connection is NOT counted as a reconnect).
	firstUp bool
}

// ConnectionOption configures optional behavior of a Connection.
type ConnectionOption func(*Connection)

// WithConnectionCollector injects a ConnectionCollector that records reconnect
// events. If not set (or nil), reconnect metrics are silently dropped.
func WithConnectionCollector(c ConnectionCollector) ConnectionOption {
	return func(conn *Connection) {
		if c != nil {
			conn.collector = c
		}
	}
}

// Open validates cfg, builds the autopaho ClientConfig, wires the three
// callbacks, starts the manager, and blocks (bounded by ctx) until the first
// connection comes up OR a bootstrap-fatal / first-attempt permanent error
// is received.
//
// Validation: Open calls cfg.Validate() before any side effect. Callers do
// not need to call Validate explicitly. Archtest MQTT-CONFIG-VALIDATE-FIRST-01
// locks this gate.
//
// Bootstrap-fatal CONNACK reason codes (0x81/0x82/0x84/0x85/0x8A/0x95) and TLS
// handshake errors cause Open to return a non-transient error immediately.
//
// A context cancellation before first connection up returns a transient error
// (the server may become reachable later).
//
// clk is a required positional parameter (CLOCK-POSITIONAL-INJECTION-01);
// call clock.MustHaveClock before passing to Open.
//
// ref: autopaho/auto.go NewConnection
func Open(ctx context.Context, clk clock.Clock, cfg Config, opts ...ConnectionOption) (*Connection, error) {
	clock.MustHaveClock(clk, "mqtt.Open")
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	c := &Connection{
		cfg:       cfg,
		clk:       clk,
		stateCh:   make(chan struct{}),
		closeCh:   make(chan struct{}),
		outcomeCh: make(chan bootstrapOutcome, 1),
	}
	for _, o := range opts {
		o(c)
	}
	if c.collector == nil {
		c.collector = noopCollector{}
	}

	urls, err := cfg.brokerURLs()
	if err != nil {
		return nil, err
	}

	autoCfg := autopaho.ClientConfig{
		ServerUrls:                    urls,
		TlsCfg:                        cfg.TLS,
		KeepAlive:                     uint16(cfg.KeepAlive.Seconds()),
		SessionExpiryInterval:         uint32(cfg.SessionExpiry.Seconds()),
		CleanStartOnInitialConnection: cfg.SessionExpiry == 0,
		ConnectTimeout:                cfg.ConnectTimeout,
		ConnectUsername:               cfg.Auth.Username,
		ConnectPassword:               cfg.Auth.Password,
		ReconnectBackoff: func(n int) time.Duration {
			return adapterutil.ExponentialBackoffWithJitter(
				cfg.Backoff.BaseDelay, cfg.Backoff.MaxDelay, n,
			)
		},
		ConnectPacketBuilder: connectPacketBuilder(cfg),
		OnConnectionUp:       c.onConnectionUp,
		OnConnectionDown:     c.onConnectionDown,
		OnConnectError:       c.onConnectError,
		ClientConfig: paho.ClientConfig{
			ClientID:           cfg.ClientID.String(),
			OnServerDisconnect: c.onServerDisconnect,
		},
	}

	cm, err := autopaho.NewConnection(ctx, autoCfg)
	if err != nil {
		return nil, errcode.WrapInfra(ErrAdapterMQTTConnect,
			"mqtt: failed to start connection manager", redactErr(err))
	}
	c.cm = cm

	// Block until first outcome (connected or permErr) or ctx canceled.
	if waitErr := c.waitFirstConnection(ctx); waitErr != nil {
		// Best-effort shutdown of the manager; ignore error.
		_ = cm.Disconnect(context.Background())
		return nil, waitErr
	}
	return c, nil
}

// connectPacketBuilder returns an autopaho ConnectPacketBuilder that injects
// cfg.MaximumPacketSize into the CONNECT packet's Properties when non-zero.
// Returns nil (no builder) when MaximumPacketSize is 0 so autopaho's default
// CONNECT packet is used unchanged. Wiring this field closes C1 F12 — the
// MaximumPacketSize Config field was previously declared but unused.
func connectPacketBuilder(cfg Config) func(*paho.Connect, *url.URL) (*paho.Connect, error) {
	if cfg.MaximumPacketSize == 0 {
		return nil
	}
	maxSize := cfg.MaximumPacketSize
	return func(cp *paho.Connect, _ *url.URL) (*paho.Connect, error) {
		if cp.Properties == nil {
			cp.Properties = &paho.ConnectProperties{}
		}
		cp.Properties.MaximumPacketSize = &maxSize
		return cp, nil
	}
}

// waitFirstConnection blocks until the first bootstrapOutcome arrives on
// outcomeCh, or ctx is canceled. Single-channel design eliminates the prior
// `select` race between connectedCh and bootstrapErrCh.
func (c *Connection) waitFirstConnection(ctx context.Context) error {
	select {
	case outcome := <-c.outcomeCh:
		if outcome.permErr != nil {
			return outcome.permErr
		}
		return nil
	case <-ctx.Done():
		return errcode.WrapInfra(ErrAdapterMQTTConnectTimeout,
			"mqtt: context canceled before first connection", nil)
	}
}

// emitFirstOutcome sends `o` to outcomeCh exactly once. Subsequent calls are
// silent no-ops. Caller must NOT hold c.mu (this function acquires it).
func (c *Connection) emitFirstOutcome(o bootstrapOutcome) {
	c.mu.Lock()
	if c.firstOutcomeSent {
		c.mu.Unlock()
		return
	}
	c.firstOutcomeSent = true
	c.mu.Unlock()
	// outcomeCh is buffered(1); we are the unique first sender, so this send
	// never blocks. Use select-default as belt-and-suspenders.
	select {
	case c.outcomeCh <- o:
	default:
	}
}

// broadcastState closes the current stateCh and installs a fresh one. It is
// the single place that recreates stateCh, ensuring every phase transition
// wakes any WaitConnected waiters exactly once and re-arms for the next
// transition. Caller MUST hold c.mu in write mode.
func (c *Connection) broadcastStateLocked() {
	ch := c.stateCh
	c.stateCh = make(chan struct{})
	close(ch)
}

// onConnectionUp is called by autopaho when a connection (or reconnection) is established.
// It clears permanentErr, advances phase to phaseConnected, broadcasts the state change,
// and — for reconnections — increments the collector metric. On the first up it also
// emits the bootstrap "connected" outcome.
func (c *Connection) onConnectionUp(_ *autopaho.ConnectionManager, _ *paho.Connack) {
	c.mu.Lock()
	c.permanentErr = nil
	c.phase = phaseConnected
	isReconnect := c.firstUp
	c.firstUp = true
	c.broadcastStateLocked()
	c.mu.Unlock()

	if !isReconnect {
		c.emitFirstOutcome(bootstrapOutcome{connected: true})
	} else {
		// Reconnection (not the first connection). Record the metric using a
		// background context so the metric write doesn't block on caller's ctx.
		c.collector.RecordReconnect(context.Background())
	}
	slog.Info("mqtt: connection established",
		slog.String("clientID", c.cfg.ClientID.String()),
		slog.Bool("reconnect", isReconnect))
}

// onConnectionDown is called by autopaho when an established connection is lost.
// Returning false stops retry; returning true continues.
func (c *Connection) onConnectionDown() bool {
	c.mu.Lock()
	closed := c.closed
	c.phase = phaseDisconnected
	c.broadcastStateLocked()
	c.mu.Unlock()

	if closed {
		slog.Debug("mqtt: connection down after close; stopping retry",
			slog.String("clientID", c.cfg.ClientID.String()))
		return false
	}
	slog.Info("mqtt: connection lost; autopaho will reconnect",
		slog.String("clientID", c.cfg.ClientID.String()))
	return true
}

// onConnectError is called by autopaho whenever a connection attempt fails.
// classifyConnackReason maps the error to a class:
//   - classBootstrapFatal: emit first-outcome with permErr (Open returns it).
//   - classPermanentRetain: set permanentErr; if this is the first outcome,
//     emit it so Open returns instead of waiting for a transient retry. Either
//     way, broadcast state so WaitConnected waiters see the permErr.
//   - classTransient: record lastError only.
func (c *Connection) onConnectError(err error) {
	class, code := classifyConnackReason(err)
	redacted := redactErr(err)

	switch class {
	case classBootstrapFatal:
		permErr := buildConnackError(code, err,
			"mqtt: connection rejected (fail-fast)")
		slog.Error("mqtt: bootstrap-fatal connect error",
			slog.String("clientID", c.cfg.ClientID.String()),
			slog.String("code", string(code)),
			slog.Any("error", redacted))
		c.recordPermanentLocked(permErr)
		c.emitFirstOutcome(bootstrapOutcome{permErr: permErr})

	case classPermanentRetain:
		permErr := buildConnackError(code, err,
			"mqtt: connection rejected (permanent; retrying until operator fix)")
		slog.Warn("mqtt: permanent connect error; will retry until operator fixes",
			slog.String("clientID", c.cfg.ClientID.String()),
			slog.String("code", string(code)),
			slog.Any("error", redacted))
		c.recordPermanentLocked(permErr)
		// First-attempt permanent error: surface to Open as bootstrap outcome
		// so the caller doesn't hang on the connect timeout budget.
		c.emitFirstOutcome(bootstrapOutcome{permErr: permErr})

	default: // classTransient
		c.mu.Lock()
		if redacted != nil {
			c.lastError = redacted.Error()
		}
		c.mu.Unlock()
		slog.Warn("mqtt: transient connect error; autopaho will retry",
			slog.String("clientID", c.cfg.ClientID.String()),
			slog.Any("error", redacted))
	}
}

// recordPermanentLocked stores permErr into c.permanentErr and broadcasts the
// state change so WaitConnected waiters can observe it.
func (c *Connection) recordPermanentLocked(permErr error) {
	c.mu.Lock()
	c.permanentErr = permErr
	c.broadcastStateLocked()
	c.mu.Unlock()
}

// buildConnackError wraps a CONNACK rejection into an errcode.Error with
// public reasonCode/reasonName details so operators get structured diagnostics
// (C5 F9 — reason code/name visibility). The cause is preserved via WithCause
// for errors.Is/As chains; redaction happens at logging boundaries (slog), not
// here (errcode WithCause does not redact).
func buildConnackError(code errcode.Code, cause error, message string) error {
	opts := []errcode.Option{}
	var connackErr *autopaho.ConnackError
	if errors.As(cause, &connackErr) {
		opts = append(opts, errcode.WithDetails(
			errcode.PublicInt("reasonCode", int(connackErr.ReasonCode)),
			errcode.PublicString("reasonName", connackReasonName(connackErr.ReasonCode)),
		))
	}
	if cause != nil {
		opts = append(opts, errcode.WithInternal(
			errcode.InternalAttr("_", redactErr(cause).Error()),
		))
	}
	return errcode.New(errcode.KindInternal, code, message, opts...)
}

// onServerDisconnect records a server-initiated DISCONNECT for diagnostics.
func (c *Connection) onServerDisconnect(d *paho.Disconnect) {
	slog.Warn("mqtt: server requested disconnect",
		slog.String("clientID", c.cfg.ClientID.String()),
		slog.Int("reasonCode", int(d.ReasonCode)))
}

// Client returns the underlying autopaho.ConnectionManager. Callers use this
// to publish messages or subscribe to topics.
//
// Warning: PR-2 Publisher and PR-3 Subscriber MUST route publish/subscribe
// topic arguments through TopicNamespace.PublishOK / TopicNamespace.SubscribeOK
// before calling any ConnectionManager methods. The callsite funnel is not yet
// enforced at compile time; it is tracked by gh issue #1225 and will be locked
// in PR-2/PR-3.
func (c *Connection) Client() *autopaho.ConnectionManager {
	return c.cm
}

// Health returns the current readiness of the connection:
//   - nil         — phaseConnected and no permanent error
//   - ErrClosed   — the Connection has been explicitly closed (non-transient)
//   - permanentErr — credentials/authorization rejection (non-transient)
//   - NeverConnected — still connecting for the first time (transient)
//   - reconnecting — connection was up but dropped; autopaho is retrying (transient)
//
// Health performs no broker round-trip (in-memory state only).
func (c *Connection) Health(_ context.Context) error {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.closed {
		return errcode.New(errcode.KindInternal, ErrAdapterMQTTClosed,
			"mqtt: connection is closed")
	}
	if c.permanentErr != nil {
		return c.permanentErr
	}
	switch c.phase {
	case phaseConnected:
		return nil
	case phaseConnecting:
		return errcode.WrapInfra(ErrAdapterMQTTNeverConnected,
			"mqtt: never connected", nil)
	default: // phaseDisconnected
		return errcode.WrapInfra(ErrAdapterMQTTConnect,
			"mqtt: reconnecting", nil)
	}
}

// Close shuts down the connection idempotently, bounded by ctx.
// Sets the closed flag and closes closeCh before calling cm.Disconnect so that
// onConnectionDown returns false (stops retry). After Close, any WaitConnected
// caller is woken via broadcastStateLocked and observes the closed flag.
func (c *Connection) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.closeCh)
	c.broadcastStateLocked()
	c.mu.Unlock()

	return adapterutil.CloseWithDeadline(ctx, "mqtt", func() error {
		return c.cm.Disconnect(ctx)
	})
}

// Probes returns the singleton readiness probe for the MQTT connection.
// The probe name is ProbeReady ("mqtt_ready"), satisfying PROBENAME-SEALED-FUNNEL-01.
func (c *Connection) Probes() []healthz.Probe {
	return []healthz.Probe{
		adapterutil.HealthToProbe(ProbeReady, c.Health, adapterutil.DefaultProbeTimeout),
	}
}

// Worker returns nil: autopaho manages its own reconnect goroutine internally.
// No GoCell background worker registration is needed.
func (c *Connection) Worker() worker.Worker {
	return nil
}

// WaitConnected blocks until the connection phase is phaseConnected, a
// permanent error is set, Close is called, or ctx is canceled.
//
// Returns nil once connected, ErrAdapterMQTTClosed if Close was invoked,
// permanentErr on credentials/authorization rejection, or a wrapped errcode
// carrying ctx.Err() on cancellation.
func (c *Connection) WaitConnected(ctx context.Context) error {
	for {
		c.mu.RLock()
		closed := c.closed
		phase := c.phase
		permErr := c.permanentErr
		ch := c.stateCh
		c.mu.RUnlock()

		if closed {
			return errcode.New(errcode.KindInternal, ErrAdapterMQTTClosed,
				"mqtt: connection is closed")
		}
		if permErr != nil {
			return permErr
		}
		if phase == phaseConnected {
			return nil
		}

		select {
		case <-ch:
			// State changed; re-check at top of loop.
		case <-ctx.Done():
			return errcode.WrapInfra(ErrAdapterMQTTConnectTimeout,
				"mqtt: WaitConnected canceled", ctx.Err())
		}
	}
}

// noopCollector is a silent ConnectionCollector used when no collector is wired.
type noopCollector struct{}

func (noopCollector) RecordReconnect(_ context.Context) {}
