package mqtt

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
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
	clk       clock.Clock         // reserved for clock-based health timeouts; not yet wired (the Subscriber holds its own clk)
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

	// subMu guards the routes slice. It is a DEDICATED mutex (not c.mu) so that
	// onConnectionUp can snapshot routes and resubscribe without contending with
	// the connectivity state machine, and so the onPublishReceived callback path
	// (called by autopaho's read loop) never blocks on c.mu.
	subMu sync.RWMutex
	// routes is the registry of active subscriptions. onPublishReceived fans an
	// incoming PUBLISH out to every route whose matchFilter matches the topic;
	// onConnectionUp re-arms every route after a reconnect.
	routes []mqttRoute

	// ackClient is the paho.Client that delivered the most recent PUBLISH. The
	// autopaho v0.23.0 ConnectionManager does NOT expose Ack; manual
	// acknowledgement is performed via the *paho.Client carried on the received
	// PublishReceived. onPublishReceived captures it under subMu so
	// (*Connection).ack — the sole sanctioned ack callsite — can route through
	// it. ackClient is refreshed on every delivery (the client instance is
	// stable for the lifetime of a connection and replaced on reconnect).
	ackClient mqttAcker
}

// mqttAcker is the minimal manual-acknowledgement surface GoCell needs from a
// paho client (paho.Client implements it via Ack(*paho.Publish) error). It is
// an interface so unit tests can substitute a fake without a live broker
// connection, and so (*Connection).ack depends only on the ack capability.
type mqttAcker interface {
	Ack(pb *paho.Publish) error
}

// receiveHandler is invoked for each received PUBLISH that matches a route's
// filter. ctx is the subscription ctx captured at Subscribe time. The handler
// must NOT block the autopaho read loop for long; it is the subscriber's
// responsibility to hand off to a worker if processing is slow. The handler is
// also responsible for acking via Connection.ack (this layer does not ack).
type receiveHandler func(ctx context.Context, pb *paho.Publish)

// mqttRoute is one registered subscription: the validated filter, the requested
// QoS (used to re-arm the SUBSCRIBE on reconnect), and a dispatch closure that
// fans a matching PUBLISH to the user handler.
//
// The subscription ctx is intentionally NOT stored as a struct field — GoCell
// forbids context.Context in production struct fields (golangci containedctx,
// active repo-wide). Subscribe captures the ctx into the dispatch closure
// instead, so the handler still observes subscription-scoped cancellation while
// the struct stays ctx-free.
type mqttRoute struct {
	qos      byte
	filter   subscribableFilter
	dispatch func(pb *paho.Publish)
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
			ClientID:                   cfg.ClientID.String(),
			OnServerDisconnect:         c.onServerDisconnect,
			EnableManualAcknowledgment: true,
			OnPublishReceived: []func(paho.PublishReceived) (bool, error){
				c.onPublishReceived,
			},
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
		slog.String("client_id", c.cfg.ClientID.String()),
		slog.Bool("reconnect", isReconnect))

	// Re-arm all registered subscriptions. autopaho does not replay SUBSCRIBE
	// packets on reconnect (a clean session loses the broker-side subscription
	// state), so we resend every route. This runs for the first Up too — but at
	// that point routes is empty (Subscribe has not been called yet), so it is a
	// no-op. MUST run without holding c.mu or subMu during the cm.Subscribe call.
	c.resubscribeAll()
}

// resubscribeAll snapshots the route registry under subMu.RLock, releases the
// lock, then re-sends a SUBSCRIBE for each route via sendSubscribe. It is called
// from onConnectionUp to recover subscriptions after a reconnect. Errors are
// logged (no caller to return them to); the route stays registered so a later
// reconnect retries it.
func (c *Connection) resubscribeAll() {
	c.subMu.RLock()
	snapshot := make([]mqttRoute, len(c.routes))
	copy(snapshot, c.routes)
	c.subMu.RUnlock()

	for _, route := range snapshot {
		// Reconnect recovery is connection-scoped, not subscription-scoped, so a
		// background ctx is used for the resubscribe round-trip (autopaho's
		// per-call PacketTimeout still bounds it).
		if reason, err := c.sendSubscribe(context.Background(), route.filter, route.qos); err != nil {
			slog.Warn("mqtt: resubscribe after reconnect failed; will retry on next reconnect",
				slog.String("client_id", c.cfg.ClientID.String()),
				slog.String("filter", route.filter.String()),
				slog.Any("error", redactErr(err)))
			c.collector.RecordSubscribeFailure(context.Background(), reason)
		}
	}
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
		// Graceful-shutdown lifecycle event — Info per observability.md (not Debug,
		// which is off in production and would hide the orderly-stop confirmation).
		slog.Info("mqtt: connection down after close; stopping retry",
			slog.String("client_id", c.cfg.ClientID.String()))
		return false
	}
	slog.Info("mqtt: connection lost; autopaho will reconnect",
		slog.String("client_id", c.cfg.ClientID.String()))
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
			slog.String("client_id", c.cfg.ClientID.String()),
			slog.String("errcode", string(code)),
			slog.Any("error", redacted))
		c.recordPermanentLocked(permErr)
		c.emitFirstOutcome(bootstrapOutcome{permErr: permErr})

	case classPermanentRetain:
		permErr := buildConnackError(code, err,
			"mqtt: connection rejected (permanent; retrying until operator fix)")
		slog.Warn("mqtt: permanent connect error; will retry until operator fixes",
			slog.String("client_id", c.cfg.ClientID.String()),
			slog.String("errcode", string(code)),
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
			slog.String("client_id", c.cfg.ClientID.String()),
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

// isAuthRelatedConnackCode reports whether a CONNACK reason code is
// auth-related (bad credentials or unauthorized). For these codes, the
// human-readable reason name is moved to the Internal channel so it does
// not help an attacker enumerate "credentials wrong vs authz missing".
func isAuthRelatedConnackCode(code byte) bool {
	return code == 0x86 || code == 0x87 || code == 0x8C
}

// buildConnackError wraps a CONNACK rejection into an errcode.Error with
// structured diagnostics (C5 F9 — reason code/name visibility). For auth-related
// reason codes (0x86/0x87/0x8C) the reason name is moved to the Internal channel
// so it does not aid attacker enumeration of "credentials wrong vs authz missing";
// only the numeric reasonCode is kept on wire. For all other codes, reasonName
// stays in Public details for operator diagnostics.
// The cause is preserved via WithCause for errors.Is/As chains; redaction
// happens at logging boundaries (slog), not here.
func buildConnackError(code errcode.Code, cause error, message string) error {
	opts := []errcode.Option{}
	var connackErr *autopaho.ConnackError
	if errors.As(cause, &connackErr) {
		if isAuthRelatedConnackCode(connackErr.ReasonCode) {
			// Auth-related: keep only numeric code on wire; move name to Internal.
			opts = append(opts,
				errcode.WithDetails(
					errcode.PublicInt("reasonCode", int(connackErr.ReasonCode)),
				),
				errcode.WithInternal(
					errcode.InternalAttr("reasonName", connackReasonName(connackErr.ReasonCode)),
				),
			)
		} else {
			// Non-auth: both code and name are safe for operator diagnostics on wire.
			opts = append(opts, errcode.WithDetails(
				errcode.PublicInt("reasonCode", int(connackErr.ReasonCode)),
				errcode.PublicString("reasonName", connackReasonName(connackErr.ReasonCode)),
			))
		}
	}
	if cause != nil {
		opts = append(opts, errcode.WithInternal(
			errcode.InternalAttr("_", redactErr(cause).Error()),
		))
	}
	return errcode.New(errcode.KindInternal, code, message, opts...)
}

// onServerDisconnect records a server-initiated DISCONNECT for diagnostics.
// reason_name is decoded via disconnectReasonName (MQTT v5 §3.14.2.1) — the
// DISCONNECT reason-code table, NOT the CONNACK table, which shares numeric
// codes with different meanings.
func (c *Connection) onServerDisconnect(d *paho.Disconnect) {
	slog.Warn("mqtt: server requested disconnect",
		slog.String("client_id", c.cfg.ClientID.String()),
		slog.Int("reason_code", int(d.ReasonCode)),
		slog.String("reason_name", disconnectReasonName(d.ReasonCode)))
}

// publishOpts captures per-Publish options that are not part of the
// (ns, topic, payload) triple. Future MQTT v5 PUBLISH fields (MessageExpiry,
// UserProperties, ContentType, etc.) should be added here without breaking
// Connection.Publish's signature.
type publishOpts struct {
	QoS    byte
	Retain bool
}

// Publish sends a single MQTT PUBLISH packet via the underlying autopaho
// ConnectionManager. The publishableTopic argument carries a topic that has
// already been validated against the caller's TopicNamespace (constructor:
// TopicNamespace.Mint). Internally this method calls c.cm.Publish — the
// MQTT-PUBLISH-CALLSITE-FUNNEL-01 archtest locks this as the only callsite of
// (*autopaho.ConnectionManager).Publish in the adapters/mqtt package.
//
// Returns:
//   - (*paho.PublishResponse, nil) on broker Ack (QoS 1).
//   - (nil, ErrAdapterMQTTClosed) if Close has been called.
//   - (nil, ErrAdapterMQTTPublishCanceled) if ctx is already canceled.
//   - (nil, errcode-wrapped error) on transport-level failure from autopaho.
//
// The caller is responsible for setting any per-publish timeout via the ctx
// (the Publisher derives a child ctx from Config.PublishTimeout).
func (c *Connection) Publish(ctx context.Context, t publishableTopic, payload []byte, opts publishOpts) (*paho.PublishResponse, error) {
	c.mu.RLock()
	closed := c.closed
	c.mu.RUnlock()
	if closed {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterMQTTClosed,
			"mqtt: connection is closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, errcode.Wrap(errcode.KindUnavailable, ErrAdapterMQTTPublishCanceled,
			"mqtt: publish canceled by caller context", err)
	}
	return c.cm.Publish(ctx, &paho.Publish{
		Topic:   t.topic,
		QoS:     opts.QoS,
		Retain:  opts.Retain,
		Payload: payload,
	})
}

// onPublishReceived is the global received-PUBLISH callback wired into
// paho.ClientConfig.OnPublishReceived. It snapshots the route registry under
// subMu.RLock and dispatches the packet to every route whose matchFilter
// matches the packet topic. It returns (true, nil) if at least one route
// matched (signaling to autopaho that the packet was handled), else
// (false, nil). It does NOT ack — the subscriber acks via Connection.ack after
// processing, since EnableManualAcknowledgment is true.
func (c *Connection) onPublishReceived(pr paho.PublishReceived) (bool, error) {
	pb := pr.Packet
	if pb == nil {
		return false, nil
	}
	c.subMu.Lock()
	if pr.Client != nil {
		c.ackClient = pr.Client
	}
	snapshot := make([]mqttRoute, len(c.routes))
	copy(snapshot, c.routes)
	c.subMu.Unlock()

	matched := false
	for _, route := range snapshot {
		if topicFilterMatches(route.filter.matchFilter, pb.Topic) {
			matched = true
			route.dispatch(pb)
		}
	}
	return matched, nil
}

// topicFilterMatches reports whether an MQTT topic-filter matches a concrete
// topic. It implements MQTT v5 §4.7 wildcard semantics:
//   - "+" matches exactly one topic level
//   - "#" matches the remaining levels (must be the last level in the filter)
//   - all other level tokens must match exactly
//
// filter here is the bare matchFilter (the broker strips "$share/{group}/"
// before delivery, so no shared-subscription prefix appears).
func topicFilterMatches(filter, topic string) bool {
	fLevels := strings.Split(filter, "/")
	tLevels := strings.Split(topic, "/")

	for i, fl := range fLevels {
		if fl == "#" {
			// "#" matches the rest (including zero levels); must be last.
			return true
		}
		if i >= len(tLevels) {
			// Filter has more levels than the topic and the current level is
			// not "#": no match.
			return false
		}
		if fl == "+" {
			continue // single-level wildcard matches any one level
		}
		if fl != tLevels[i] {
			return false
		}
	}
	// All filter levels consumed; match iff the topic has no extra levels.
	return len(fLevels) == len(tLevels)
}

// sendSubscribe is the SOLE callsite of c.cm.Subscribe in this package. Both
// (*Connection).Subscribe (initial subscribe) and resubscribeAll (reconnect
// recovery) route through here so the cm.Subscribe literal lives in exactly one
// function body — keeping the MQTT subscribe callsite funnel single-site.
// It sends a SUBSCRIBE for f.wireFilter at the requested QoS and inspects the
// returned SUBACK reasons: any reason byte >= 0x80 is mapped via
// classifySubackReason into an errcode error. It does NOT touch the route
// registry (callers own registration).
//
// On failure it returns the matching SubscribeFailureReason so callers can
// record mqtt_subscribe_failed_total without re-introspecting the error:
// subscribeReasonTransport for a wire-level Subscribe failure, or
// subscribeReasonSubackReject for a SUBACK reason byte >= 0x80. The returned
// reason is meaningless when err is nil.
func (c *Connection) sendSubscribe(ctx context.Context, f subscribableFilter, qos byte) (SubscribeFailureReason, error) {
	suback, err := c.cm.Subscribe(ctx, &paho.Subscribe{
		Subscriptions: []paho.SubscribeOptions{{Topic: f.wireFilter, QoS: qos}},
	})
	if err != nil {
		return subscribeReasonTransport, errcode.Wrap(errcode.KindUnavailable, ErrAdapterMQTTSubscribe,
			"mqtt: subscribe request failed", err)
	}
	if subErr := subackError(suback); subErr != nil {
		return subscribeReasonSubackReject, subErr
	}
	return "", nil
}

// subackError inspects a SUBACK's reason bytes and returns a classified error
// for the first reason byte >= 0x80, or nil if all reasons are granted-QoS
// success values. Extracted to keep sendSubscribe's cognitive complexity low.
func subackError(suback *paho.Suback) error {
	if suback == nil {
		return nil
	}
	for _, reason := range suback.Reasons {
		if reason < 0x80 {
			continue
		}
		code, kind := classifySubackReason(reason)
		return errcode.New(kind, code,
			"mqtt: broker rejected subscription",
			errcode.WithDetails(
				errcode.PublicInt("reasonCode", int(reason)),
				errcode.PublicString("reasonName", subackReasonName(reason)),
			))
	}
	return nil
}

// Subscribe registers a route and sends a SUBSCRIBE for the (already validated)
// subscribableFilter. It is the public entry for the receive path. The
// subscribableFilter argument carries a filter that has already been validated
// against the caller's TopicNamespace via TopicNamespace.MintFilter.
//
// On success it returns a cancel closure that deregisters the route and sends an
// UNSUBSCRIBE for the filter's wire form. The cancel closure is idempotent: it
// wraps its body in a sync.Once so repeated calls (e.g. Subscriber.Subscribe's
// own `defer cancel()` on clean ctx-cancel exit AND the same func tracked for
// StopIntake/Close) run the deregister + UNSUBSCRIBE exactly once. Calling Close
// also unsubscribes all routes.
//
// On SUBACK rejection (reason byte >= 0x80) or transport failure, the route is
// deregistered and a classified error is returned.
//
// ctx is captured into the route's dispatch closure and passed to the handler on
// each delivery; callers should pass a ctx whose cancellation should stop
// handler dispatch.
func (c *Connection) Subscribe(ctx context.Context, f subscribableFilter, qos byte, h receiveHandler) (cancel func(), err error) {
	c.mu.RLock()
	closed := c.closed
	c.mu.RUnlock()
	if closed {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterMQTTClosed,
			"mqtt: connection is closed")
	}

	// Capture the subscription ctx + handler into a dispatch closure so the
	// route struct stays ctx-free (containedctx convention) while the handler
	// still observes subscription-scoped cancellation.
	route := mqttRoute{
		qos:    qos,
		filter: f,
		dispatch: func(pb *paho.Publish) {
			h(ctx, pb)
		},
	}
	c.registerRoute(route)

	if reason, subErr := c.sendSubscribe(ctx, f, qos); subErr != nil {
		c.deregisterRoute(f.wireFilter)
		c.collector.RecordSubscribeFailure(ctx, reason)
		return nil, subErr
	}

	var cancelOnce sync.Once
	cancel = func() {
		cancelOnce.Do(func() {
			c.deregisterRoute(f.wireFilter)
			if _, unsubErr := c.cm.Unsubscribe(ctx, &paho.Unsubscribe{
				Topics: []string{f.wireFilter},
			}); unsubErr != nil {
				slog.Warn("mqtt: unsubscribe on cancel failed",
					slog.String("client_id", c.cfg.ClientID.String()),
					slog.String("filter", f.String()),
					slog.Any("error", redactErr(unsubErr)))
			}
		})
	}
	return cancel, nil
}

// registerRoute appends a route under subMu.Lock.
func (c *Connection) registerRoute(route mqttRoute) {
	c.subMu.Lock()
	c.routes = append(c.routes, route)
	c.subMu.Unlock()
}

// deregisterRoute removes the route whose filter wire form equals wireFilter,
// under subMu.Lock. No-op if not found.
func (c *Connection) deregisterRoute(wireFilter string) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	for i := range c.routes {
		if c.routes[i].filter.wireFilter == wireFilter {
			c.routes = append(c.routes[:i], c.routes[i+1:]...)
			return
		}
	}
}

// ack is the SOLE manual-acknowledgement callsite in this package. The
// subscriber calls it after successfully processing a received PUBLISH
// (EnableManualAcknowledgment is true). autopaho v0.23.0's ConnectionManager
// does not expose Ack, so ack routes through the *paho.Client captured by
// onPublishReceived (stored in c.ackClient). It is unexported; the subscriber
// (later PR) holds a *Connection.
func (c *Connection) ack(pb *paho.Publish) error {
	c.mu.RLock()
	closed := c.closed
	c.mu.RUnlock()
	if closed {
		return errcode.New(errcode.KindInternal, ErrAdapterMQTTClosed,
			"mqtt: connection is closed")
	}
	c.subMu.RLock()
	acker := c.ackClient
	c.subMu.RUnlock()
	if acker == nil {
		return errcode.New(errcode.KindUnavailable, ErrAdapterMQTTSubscribe,
			"mqtt: no delivering client available for ack")
	}
	if err := acker.Ack(pb); err != nil {
		return errcode.Wrap(errcode.KindUnavailable, ErrAdapterMQTTSubscribe,
			"mqtt: manual ack failed", err)
	}
	return nil
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

	c.unsubscribeAll(ctx)

	return adapterutil.CloseWithDeadline(ctx, "mqtt", func() error {
		return c.cm.Disconnect(ctx)
	})
}

// unsubscribeAll best-effort sends an UNSUBSCRIBE for every registered route's
// wire filter before disconnect. It is one of the two sanctioned callsites of
// c.cm.Unsubscribe (the other is the cancel closure returned by Subscribe).
// Errors are logged, not returned — Close proceeds to Disconnect regardless.
func (c *Connection) unsubscribeAll(ctx context.Context) {
	c.subMu.Lock()
	filters := make([]string, 0, len(c.routes))
	for i := range c.routes {
		filters = append(filters, c.routes[i].filter.wireFilter)
	}
	c.routes = nil
	c.subMu.Unlock()

	if len(filters) == 0 {
		return
	}
	if _, err := c.cm.Unsubscribe(ctx, &paho.Unsubscribe{Topics: filters}); err != nil {
		slog.Warn("mqtt: unsubscribe-all on close failed",
			slog.String("client_id", c.cfg.ClientID.String()),
			slog.Any("error", redactErr(err)))
	}
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

func (noopCollector) RecordSubscribeFailure(_ context.Context, _ SubscribeFailureReason) {}
