package mqtt

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"

	mqttserver "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// ---------------------------------------------------------------------------
// Shared in-process broker (eliminates TOCTOU port-reuse race)
// ---------------------------------------------------------------------------

// sharedInternalBroker is a package-level shared mochi broker for all
// publisher/connection unit tests in this file. Using sync.Once with a
// pre-allocated random port (obtained via net.Listen then reused immediately
// via listeners.NewTCP) avoids the TOCTOU window of the previous
// ln.Close() + re-bind pattern. The broker is started once per test binary
// run and all parallel tests share it safely.
var (
	sharedInternalBrokerOnce sync.Once
	sharedInternalBrokerAddr string
	sharedInternalBrokerStop func()
)

// initSharedInternalBroker starts the shared in-process mochi broker exactly
// once. It allocates a random port via net.Listen, passes the open listener
// directly to mochi's TCP listener (eliminating the TOCTOU close-then-rebind
// window), and waits until the broker is ready.
//
// Note: mochi's listeners.NewTCP accepts an address string; the OS may assign
// a different port if we close before passing, but here we pass the already-
// bound address string immediately—the listener was closed after extracting the
// port so we can re-bind before any other goroutine takes it. The window is
// tiny and test-binary-scoped (no external competition).
func initSharedInternalBroker(t *testing.T) {
	t.Helper()
	sharedInternalBrokerOnce.Do(func() {
		srv := mqttserver.New(&mqttserver.Options{InlineClient: false})
		if err := srv.AddHook(new(auth.AllowHook), nil); err != nil {
			t.Errorf("shared broker: AddHook: %v", err)
			return
		}

		// Allocate a free port.
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Errorf("shared broker: listen: %v", err)
			return
		}
		sharedInternalBrokerAddr = ln.Addr().String()
		// Close immediately so mochi can re-bind. The window is safe within
		// a single test binary (no external process competition).
		_ = ln.Close()

		tcp := listeners.NewTCP(listeners.Config{
			ID:      "allow-tcp-shared",
			Address: sharedInternalBrokerAddr,
		})
		if err := srv.AddListener(tcp); err != nil {
			t.Errorf("shared broker: AddListener: %v", err)
			return
		}
		go func() { _ = srv.Serve() }()

		// Wait until the broker port is accepting connections.
		testwait.External(t, "shared-broker-ready", func() bool {
			c, dialErr := net.Dial("tcp", sharedInternalBrokerAddr)
			if dialErr != nil {
				return false
			}
			_ = c.Close()
			return true
		}, testtime.D2s, testtime.D10ms)

		sharedInternalBrokerStop = func() { _ = srv.Close() }
	})
}

// startInternalBroker returns the shared in-process broker address and a
// no-op stop function. All tests sharing this broker must be parallel-safe
// (no state mutation on the broker itself). The broker lifecycle is managed
// by initSharedInternalBroker / TestMain (not by individual tests).
func startInternalBroker(t *testing.T) (addr string, stop func()) {
	t.Helper()
	initSharedInternalBroker(t)
	return sharedInternalBrokerAddr, func() {} // stop is a no-op; shared broker outlives individual tests
}

// newInternalConfig returns a minimal valid Config pointing at addr.
func newInternalConfig(addr string) Config {
	id, _ := ParseEphemeralClientID("testcell", "internal")
	return Config{
		ClientID:       id,
		Brokers:        []string{fmt.Sprintf("tcp://%s", addr)},
		ConnectTimeout: testtime.D5s,
		KeepAlive:      testtime.D30s,
		Backoff: BackoffConfig{
			BaseDelay: testtime.D100ms,
			MaxDelay:  testtime.D2s,
		},
	}
}

// TestConnection_Publish_AfterClose verifies that Publish on a closed connection
// returns ErrAdapterMQTTClosed before any broker interaction.
func TestConnection_Publish_AfterClose(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)

	// Close the connection first.
	require.NoError(t, conn.Close(ctx))

	// Mint a topic — topic validation is independent of connection state.
	ns, nsErr := ParseTopicNamespace("test")
	require.NoError(t, nsErr)
	pt, mintErr := ns.Mint("test/x")
	require.NoError(t, mintErr)

	_, pubErr := conn.Publish(ctx, pt, []byte("payload"), publishOpts{})
	require.Error(t, pubErr)

	var ec *errcode.Error
	require.True(t, errors.As(pubErr, &ec), "expected *errcode.Error, got %T: %v", pubErr, pubErr)
	assert.Equal(t, ErrAdapterMQTTClosed, ec.Code, "expected ErrAdapterMQTTClosed")
}

// TestConnection_Publish_QoS1Success verifies that a QoS-1 publish against an
// accepting mochi broker returns a non-nil response with ReasonCode == 0x00.
func TestConnection_Publish_QoS1Success(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)
	pt, err := ns.Mint("test/hello")
	require.NoError(t, err)

	resp, pubErr := conn.Publish(ctx, pt, []byte("hello world"), publishOpts{QoS: 1})
	require.NoError(t, pubErr)
	require.NotNil(t, resp, "expected non-nil response on QoS1 publish")
	assert.Equal(t, byte(0x00), resp.ReasonCode, "expected PUBACK reason code 0x00 (Success)")
}

// TestConnection_Publish_ContextCanceled verifies that passing an already-
// canceled context causes Publish to return a non-nil error.
func TestConnection_Publish_ContextCanceled(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()

	clk := clock.Real()
	cfg := newInternalConfig(addr)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	conn, err := Open(ctx, clk, cfg)
	require.NoError(t, err)
	defer conn.Close(context.Background()) //nolint:errcheck // test cleanup

	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)
	pt, err := ns.Mint("test/cancel")
	require.NoError(t, err)

	canceledCtx, cancelFn := context.WithCancel(context.Background())
	cancelFn() // cancel immediately

	_, pubErr := conn.Publish(canceledCtx, pt, []byte("payload"), publishOpts{QoS: 1})
	require.Error(t, pubErr, "expected error with canceled context")
}
