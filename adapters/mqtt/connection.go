package mqtt

import (
	"context"
	"log/slog"
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
	connected    chan struct{} // closed when phaseConnected; recreated on down/perm
	permanentErr error         // set on classPermanentRetain; cleared on OnConnectionUp
	closeCh      chan struct{} // closed once when Connection.Close() is called

	// bootstrapErrCh receives the first bootstrap-fatal error so Open can
	// return it instead of waiting for ctx to expire. Buffered(1) to prevent
	// the goroutine from blocking.
	bootstrapErrCh chan error

	// firstUp tracks whether the first OnConnectionUp has fired.
	// The very first successful connection is NOT counted as a reconnect.
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

// Open builds the autopaho ClientConfig, injects ReconnectBackoff, wires the
// three callbacks, starts the manager, and blocks (bounded by ctx) until the
// first connection comes up OR a fail-fast bootstrap error is received.
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

	c := &Connection{
		cfg:            cfg,
		clk:            clk,
		connected:      make(chan struct{}),
		closeCh:        make(chan struct{}),
		bootstrapErrCh: make(chan error, 1),
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
		OnConnectionUp:   c.onConnectionUp,
		OnConnectionDown: c.onConnectionDown,
		OnConnectError:   c.onConnectError,
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

	// Block until first connection up, bootstrap fatal, or ctx canceled.
	if waitErr := c.waitFirstConnection(ctx); waitErr != nil {
		// Best-effort shutdown of the manager; ignore error.
		_ = cm.Disconnect(context.Background())
		return nil, waitErr
	}
	return c, nil
}

// waitFirstConnection blocks until phaseConnected, a bootstrap-fatal error,
// or ctx cancellation. Cognitive complexity split from Open.
func (c *Connection) waitFirstConnection(ctx context.Context) error {
	c.mu.RLock()
	connCh := c.connected
	c.mu.RUnlock()

	select {
	case <-connCh:
		return nil
	case err := <-c.bootstrapErrCh:
		return err
	case <-ctx.Done():
		// Context expired before first connection — transient from caller's view.
		return errcode.WrapInfra(ErrAdapterMQTTConnectTimeout,
			"mqtt: context canceled before first connection", nil)
	}
}

// onConnectionUp is called by autopaho when a connection (or reconnection) is established.
// It clears permanentErr, advances phase to phaseConnected, closes the current connected
// chan (waking waiters), and — for reconnections — increments the collector metric.
func (c *Connection) onConnectionUp(cm *autopaho.ConnectionManager, _ *paho.Connack) {
	c.mu.Lock()
	c.permanentErr = nil
	c.phase = phaseConnected
	ch := c.connected
	c.connected = make(chan struct{}) // pre-create for next down cycle
	isReconnect := c.firstUp
	c.firstUp = true
	c.mu.Unlock()

	close(ch) // wake all waiters blocked on the old connected chan

	if isReconnect {
		// This is a reconnection (not the first connection). Record the metric.
		// Use a background context so the metric write doesn't block on caller's ctx.
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
	// Swap connected chan so WaitConnected on the next connected phase waits correctly.
	c.connected = make(chan struct{})
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
//   - classBootstrapFatal: send to bootstrapErrCh (once) and wake connected waiters.
//   - classPermanentRetain: set permanentErr and wake connected waiters.
//   - classTransient: record lastError only.
func (c *Connection) onConnectError(err error) {
	class, code := classifyConnackReason(err)
	redacted := redactErr(err)

	switch class {
	case classBootstrapFatal:
		slog.Error("mqtt: bootstrap-fatal connect error",
			slog.String("clientID", c.cfg.ClientID.String()),
			slog.String("code", string(code)),
			slog.Any("error", redacted))
		bootErr := errcode.New(errcode.KindInternal, code,
			"mqtt: connection rejected (fail-fast)")
		// Send once; non-blocking in case caller has already timed out.
		select {
		case c.bootstrapErrCh <- bootErr:
		default:
		}
		// Wake any WaitConnected callers that may be blocked.
		c.wakeWaitersWithPermanentErr(bootErr)

	case classPermanentRetain:
		slog.Warn("mqtt: permanent connect error; will retry until operator fixes",
			slog.String("clientID", c.cfg.ClientID.String()),
			slog.String("code", string(code)),
			slog.Any("error", redacted))
		c.wakeWaitersWithPermanentErr(
			errcode.New(errcode.KindInternal, code,
				"mqtt: connection rejected (permanent; retrying until operator fix)"),
		)

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

// wakeWaitersWithPermanentErr sets permanentErr and closes the current connected
// chan so WaitConnected returns the error.
func (c *Connection) wakeWaitersWithPermanentErr(permErr error) {
	c.mu.Lock()
	c.permanentErr = permErr
	ch := c.connected
	c.connected = make(chan struct{}) // fresh chan for next potential recovery
	c.mu.Unlock()

	close(ch)
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
// onConnectionDown returns false (stops retry).
func (c *Connection) Close(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	close(c.closeCh)
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
// permanent error is set, or ctx is canceled.
//
// Returns nil once connected, permanentErr if credentials are rejected,
// or ctx.Err() on cancellation.
func (c *Connection) WaitConnected(ctx context.Context) error {
	for {
		c.mu.RLock()
		phase := c.phase
		permErr := c.permanentErr
		ch := c.connected
		c.mu.RUnlock()

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
			return ctx.Err()
		}
	}
}

// noopCollector is a silent ConnectionCollector used when no collector is wired.
type noopCollector struct{}

func (noopCollector) RecordReconnect(_ context.Context) {}
