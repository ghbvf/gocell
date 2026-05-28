package mqtt

import (
	"context"
	"errors"
	"fmt"
	"net"
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

// startInternalBroker starts an in-process mochi MQTT broker on a random port
// and returns the address plus a stop function. Internal variant (package mqtt)
// mirrors startEmbeddedBroker from connection_test.go but is scoped here so
// that internal tests (package mqtt) can access unexported types.
func startInternalBroker(t *testing.T) (addr string, stop func()) {
	t.Helper()
	srv := mqttserver.New(&mqttserver.Options{InlineClient: false})
	require.NoError(t, srv.AddHook(new(auth.AllowHook), nil), "add allow hook")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err, "listen random port")
	addr = ln.Addr().String()
	require.NoError(t, ln.Close())

	tcp := listeners.NewTCP(listeners.Config{ID: "allow-tcp-internal", Address: addr})
	require.NoError(t, srv.AddListener(tcp), "add listener")
	go func() { _ = srv.Serve() }()

	testwait.External(t, "internal-broker-ready", func() bool {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	}, testtime.D2s, testtime.D10ms)

	return addr, func() { _ = srv.Close() }
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
