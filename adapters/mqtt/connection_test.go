package mqtt_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	mqttserver "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/mqtt"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// startEmbeddedBroker starts an in-process mochi broker with the given hook on
// a random port, waits until it accepts connections, and returns addr + stop.
func startEmbeddedBroker(t *testing.T, hook mqttserver.Hook, hookID string) (addr string, stop func()) {
	t.Helper()
	srv := mqttserver.New(&mqttserver.Options{InlineClient: false})
	err := srv.AddHook(hook, nil)
	require.NoError(t, err, "add hook %s", hookID)

	// Bind a random port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "listen random port")
	addr = ln.Addr().String()
	if err = ln.Close(); err != nil {
		t.Logf("close probe listener: %v", err)
	}

	tcp := listeners.NewTCP(listeners.Config{
		ID:      hookID,
		Address: addr,
	})
	err = srv.AddListener(tcp)
	require.NoError(t, err, "add listener")

	go func() {
		_ = srv.Serve()
	}()

	// Poll until the broker is accepting TCP connections — deterministic
	// alternative to time.Sleep (TEST-SLEEP-DISCIPLINE-01).
	listenAddr := addr
	testwait.External(t, "mqtt-broker-accepts-connections", func() bool {
		c, derr := net.Dial("tcp", listenAddr)
		if derr != nil {
			return false
		}
		_ = c.Close()
		return true
	}, testtime.D2s, testtime.D10ms)

	return addr, func() {
		closeBrokerSafely(t, srv)
	}
}

// newEmbeddedBroker starts an in-process mochi MQTT v2 broker on a random port
// and returns the address and a stop function. The AllowHook permits all clients.
func newEmbeddedBroker(t *testing.T) (addr string, stop func()) {
	t.Helper()
	return startEmbeddedBroker(t, new(auth.AllowHook), "allow-tcp")
}

// newEmbeddedBrokerWithDenyHook starts a broker that denies all auth and
// returns the addr and a stop function.
func newEmbeddedBrokerWithDenyHook(t *testing.T) (addr string, stop func()) {
	t.Helper()
	return startEmbeddedBroker(t, &denyAuthHook{deny: true}, "deny-tcp")
}

// denyAuthHook is a mochi hook that denies all connections when deny=true.
type denyAuthHook struct {
	mqttserver.HookBase
	deny bool
}

func (h *denyAuthHook) ID() string { return "deny-hook" }
func (h *denyAuthHook) Provides(b byte) bool {
	return b == mqttserver.OnConnectAuthenticate || b == mqttserver.OnACLCheck
}

func (h *denyAuthHook) OnConnectAuthenticate(_ *mqttserver.Client, _ packets.Packet) bool {
	return !h.deny
}
func (h *denyAuthHook) OnACLCheck(_ *mqttserver.Client, _ string, _ bool) bool { return true }

// newValidConfig returns a minimal valid Config pointing at addr.
// addr is expected to be a loopback "127.0.0.1:port" form so the
// secutil.ValidateTLSEndpoint plaintext-broker check accepts it.
func newValidConfig(addr string) mqtt.Config {
	id, _ := mqtt.ParseEphemeralClientID("testcell", "client")
	return mqtt.Config{
		ClientID:        id,
		Brokers:         []string{fmt.Sprintf("tcp://%s", addr)},
		ConnectTimeout:  testtime.D5s,
		ConnectDeadline: testtime.D10s,
		KeepAlive:       testtime.D30s,
		Backoff: mqtt.BackoffConfig{
			BaseDelay: testtime.D100ms,
			MaxDelay:  testtime.D2s,
		},
	}
}

// TestConnection_HappyPath_HealthOk verifies that Open succeeds against a live
// broker and Health returns nil immediately after.
func TestConnection_HappyPath_HealthOk(t *testing.T) {
	addr, stop := newEmbeddedBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newValidConfig(addr)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	conn, err := mqtt.Open(ctx, clk, cfg)
	require.NoError(t, err)
	require.NotNil(t, conn)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup; error not relevant

	assert.NoError(t, conn.Health(ctx))
	// R4 round-2: Client() was demoted to unexported client(); the cm is
	// indirectly asserted live by conn.Health(ctx) == nil (Health reads
	// c.phase which is set to phaseConnected only after autopaho.NewConnection
	// returned a non-nil cm and OnConnectionUp fired).
}

// TestConnection_Close_IdempotentAndTerminal verifies that Close is idempotent
// and that Health returns a closed (non-transient) error after Close.
func TestConnection_Close_IdempotentAndTerminal(t *testing.T) {
	addr, stop := newEmbeddedBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newValidConfig(addr)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	conn, err := mqtt.Open(ctx, clk, cfg)
	require.NoError(t, err)

	err = conn.Close(ctx)
	assert.NoError(t, err, "first Close should succeed")

	// Second Close must also be nil (idempotent).
	err2 := conn.Close(ctx)
	assert.NoError(t, err2, "second Close should be idempotent")

	// Health after Close must return a non-transient error.
	hErr := conn.Health(context.Background())
	require.Error(t, hErr)
}

// TestConnection_ConnectDeadline_FailFast verifies that Open against an
// unreachable address fails-fast on the cfg.ConnectDeadline budget — NOT on the
// lifecycle ctx. The lifecycle ctx passed here is context.Background() (never
// canceled), so the only thing that can terminate the bootstrap wait is the
// connectCtx derived from ConnectDeadline. This is the #1388 regression guard:
// previously a never-canceled ctx meant Open hung forever.
func TestConnection_ConnectDeadline_FailFast(t *testing.T) {
	clk := clock.Real()
	id, _ := mqtt.ParseEphemeralClientID("test", "never")
	cfg := mqtt.Config{
		ClientID:        id,
		Brokers:         []string{"tcp://127.0.0.1:19999"}, // dead port
		ConnectTimeout:  testtime.D500ms,
		ConnectDeadline: testtime.D500ms,
		KeepAlive:       testtime.D30s,
		Backoff: mqtt.BackoffConfig{
			BaseDelay: testtime.D50ms,
			MaxDelay:  testtime.D200ms,
		},
	}

	// Lifecycle ctx never cancels — only ConnectDeadline can end the wait.
	_, err := mqtt.Open(context.Background(), clk, cfg)
	require.Error(t, err)
	assertErrCode(t, err, mqtt.ErrAdapterMQTTConnectTimeout)
}

// TestConnection_ConnectDeadline_Decoupled_CMSurvives verifies the other half of
// the #1388 split: once the first connection succeeds, the ConnectionManager is
// bound to the lifecycle ctx, NOT the connect-deadline ctx. Open derives a
// WithTimeout(ctx, ConnectDeadline) child and cancels it on return; if the CM
// were (wrongly) bound to that child, the deferred cancel would tear the
// connection down. A short ConnectDeadline plus a healthy post-Open connection
// proves the two lifetimes are decoupled.
func TestConnection_ConnectDeadline_Decoupled_CMSurvives(t *testing.T) {
	addr, stop := newEmbeddedBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newValidConfig(addr)
	cfg.ConnectDeadline = testtime.D2s // short; connectCtx is canceled on Open return

	conn, err := mqtt.Open(context.Background(), clk, cfg)
	require.NoError(t, err)
	require.NotNil(t, conn)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup; error not relevant

	// connectCtx (WithTimeout child) is already canceled by Open's defer; if the
	// CM were bound to it the connection would be down. Health == nil proves the
	// CM survives on the lifecycle ctx.
	assert.NoError(t, conn.Health(context.Background()))
}

// TestConnection_DenyBroker_PermanentErrViaHealth tests that a deny-all broker
// (0x87 NotAuthorized from mochi) sets permanentErr and Open surfaces it as
// ErrAdapterMQTTConnectPermanent — proving the bootstrap outcome channel
// routes first-attempt permanent rejection back to Open (C2 F4 + C5 F9).
func TestConnection_DenyBroker_PermanentErrViaHealth(t *testing.T) {
	addr, stop := newEmbeddedBrokerWithDenyHook(t)
	defer stop()

	clk := clock.Real()
	cfg := newValidConfig(addr)
	cfg.ConnectTimeout = testtime.D500ms
	cfg.Backoff.BaseDelay = testtime.D50ms
	cfg.Backoff.MaxDelay = testtime.D200ms

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D5s)
	defer cancel()

	// Open must return ErrAdapterMQTTConnectPermanent — the bootstrap outcome
	// path carries the CONNACK rejection back to the caller.
	conn, err := mqtt.Open(ctx, clk, cfg)
	require.Error(t, err, "deny-broker Open must return a permanent error")
	assertErrCode(t, err, mqtt.ErrAdapterMQTTConnectPermanent)
	require.Nil(t, conn, "Open returning a permanent error must not surface a Connection")
}

// TestConnection_WaitConnected_PermanentErrSurfaced verifies that when the
// first connection attempt is denied, Open returns the permanent error
// directly without falling back to ctx-deadline. The earlier shape that
// allowed Open to succeed and require WaitConnected to surface the error
// was the C2 F4 select-race bug; this test guards the fix.
func TestConnection_WaitConnected_PermanentErrSurfaced(t *testing.T) {
	addr, stop := newEmbeddedBrokerWithDenyHook(t)
	defer stop()

	clk := clock.Real()
	cfg := newValidConfig(addr)
	cfg.ConnectTimeout = testtime.D300ms
	cfg.Backoff.BaseDelay = testtime.D30ms
	cfg.Backoff.MaxDelay = testtime.D100ms

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D3s)
	defer cancel()

	conn, err := mqtt.Open(ctx, clk, cfg)
	require.Error(t, err, "deny-broker first attempt must fail-fast through Open")
	assertErrCode(t, err, mqtt.ErrAdapterMQTTConnectPermanent)
	require.Nil(t, conn)
}

// assertErrCode asserts that err carries an *errcode.Error whose Code matches
// want. Used by C2 F13 assertions to lock specific sentinel codes instead of
// any-error early-returns.
func assertErrCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "expected *errcode.Error in chain, got %T: %v", err, err)
	require.Equal(t, want, ec.Code, "errcode.Code = %s, want %s", ec.Code, want)
}

// TestConnection_ReconnectMetric_Counted verifies that RecordReconnect is
// called when a second connection event fires (first up does NOT count), and
// increments to >=1 after a reconnection.
//
// Strategy: start a live broker, connect, ask the broker to disconnect that
// client, and poll until autopaho reconnects to the same broker. Keeping the
// broker alive avoids racing mochi Server.Close with an in-flight client
// disconnect, which can deadlock inside mochi's Clients lock.
func TestConnection_ReconnectMetric_Counted(t *testing.T) {
	addr, srv := startControlledBroker(t)

	clk := clock.Real()
	cfg := newValidConfig(addr)
	cfg.Backoff.BaseDelay = testtime.D50ms
	cfg.Backoff.MaxDelay = testtime.D500ms

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D15s)
	defer cancel()

	collector := &fakeCollector{}
	conn, err := mqtt.Open(ctx, clk, cfg, mqtt.WithConnectionCollector(collector))
	require.NoError(t, err)
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), testtime.D2s)
		defer closeCancel()
		require.NoError(t, conn.Close(closeCtx), "connection cleanup")
		// closeBrokerSafely drains then guards srv.Close; the explicit
		// testwait drain above is now subsumed by the helper's own drain.
		closeBrokerSafely(t, srv)
	}()

	// Initial connection must not count as a reconnect.
	assert.Equal(t, int64(0), collector.count.Load(),
		"first connection must not increment reconnect counter")

	var client *mqttserver.Client
	testwait.External(t, "mqtt-broker-observes-client", func() bool {
		var ok bool
		client, ok = srv.Clients.Get(cfg.ClientID.String())
		return ok && !client.Closed()
	}, testtime.D2s, testtime.D10ms)

	require.NoError(t, srv.DisconnectClient(client, packets.CodeDisconnect),
		"server-initiated disconnect")

	// Wait until autopaho detects the TCP close and enters the reconnect loop.
	// We poll until onConnectionDown has fired (conn.Health returns non-nil).
	testwait.External(t, "autopaho-detects-disconnect", func() bool {
		return conn.Health(context.Background()) != nil
	}, testtime.D5s, testtime.D20ms)

	// Poll until RecordReconnect is called (count >= 1) or timeout.
	testwait.External(t, "reconnect-metric-fires", func() bool {
		return collector.count.Load() >= 1
	}, testtime.D10s, testtime.D100ms)

	assert.GreaterOrEqual(t, collector.count.Load(), int64(1),
		"reconnect counter must increment after reconnection")
}

// startControlledBroker starts a mochi broker on a random port and returns
// the address and server handle so tests can drive server-side client actions.
// The caller is responsible for closing the returned server.
func startControlledBroker(t *testing.T) (addr string, srv *mqttserver.Server) {
	t.Helper()
	srv = mqttserver.New(&mqttserver.Options{InlineClient: false})
	require.NoError(t, srv.AddHook(new(auth.AllowHook), nil))

	// Bind a random port.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr = ln.Addr().String()
	require.NoError(t, ln.Close())

	tcp := listeners.NewTCP(listeners.Config{ID: "allow-tcp-controlled", Address: addr})
	require.NoError(t, srv.AddListener(tcp))
	go func() { _ = srv.Serve() }()

	testwait.External(t, "mqtt-controlled-broker-accepts-connections", func() bool {
		c, derr := net.Dial("tcp", addr)
		if derr != nil {
			return false
		}
		_ = c.Close()
		return true
	}, testtime.D2s, testtime.D10ms)

	return addr, srv
}

// fakeCollector is a ConnectionCollector that counts RecordReconnect calls.
// count is an atomic int64 for race-safe access from the reconnect goroutine.
type fakeCollector struct {
	count atomic.Int64
}

func (f *fakeCollector) RecordReconnect(_ context.Context) { f.count.Add(1) }

// RecordSubscribeFailure satisfies the ConnectionCollector interface (added by
// PR-3 review fix F7). This fake only counts reconnects, so it is a no-op here.
func (f *fakeCollector) RecordSubscribeFailure(_ context.Context, _ mqtt.SubscribeFailureReason) {}

// closeBrokerSafely closes a mochi v2 broker with two defenses against the
// mochi v2.7.9 Clients-lock deadlock:
//
//  1. Best-effort drain: polls srv.Clients.Len() == 0 for up to testtime.D2s
//     so any in-flight client disconnects settle before Close is called.
//     Drain is non-fatal; if the budget expires the guarded close proceeds.
//
//  2. Timeout-guarded close: runs srv.Close() in a goroutine and selects on
//     done vs testtime.D5s. If Close returns, we're done. If it exceeds the
//     budget (the mochi Clients-lock deadlock) a clear diagnostic is logged
//     via t.Logf and the helper returns so the test fails fast instead of
//     hanging for ~10 min.
func closeBrokerSafely(t *testing.T, srv *mqttserver.Server) {
	t.Helper()

	// Defense 1: best-effort drain (non-fatal on timeout).
	drainTimer := time.NewTimer(testtime.D2s)
	drainTick := time.NewTicker(testtime.D10ms)
drainLoop:
	for srv.Clients.Len() > 0 {
		select {
		case <-drainTimer.C:
			break drainLoop
		case <-drainTick.C:
		}
	}
	drainTimer.Stop()
	drainTick.Stop()

	// Defense 2: timeout-guarded close.
	done := make(chan error, 1)
	go func() { done <- srv.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Logf("mqtt-test: srv.Close() returned error: %v", err)
		}
	case <-time.After(testtime.D5s):
		t.Logf("mqtt-test: srv.Close() exceeded %v — suspected mochi v2.7.9 Clients-lock deadlock; abandoning close", testtime.D5s)
	}
}
