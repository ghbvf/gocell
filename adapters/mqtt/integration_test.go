//go:build integration

package mqtt

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	dockercontainer "github.com/moby/moby/api/types/container"
	dockernet "github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/tests/testutil"
)

// newTestConfig returns a Config valid for connecting to the shared Mosquitto
// container. The broker URL is normalized via testutil.LoopbackIPEndpoint so
// that "localhost"-form URLs pass the loopback-IP-literal validator in
// Config.validateBrokers (secutil.ValidateTLSEndpoint rejects DNS names for
// plaintext plaintext schemes).
func newTestConfig(t *testing.T, role string) Config {
	t.Helper()
	cid, err := ParseEphemeralClientID("itest", role)
	if err != nil {
		t.Fatalf("ParseEphemeralClientID: %v", err)
	}
	brokerURL := testutil.LoopbackIPEndpoint(sharedBrokerURL(t))
	return Config{
		ClientID:       cid,
		Brokers:        []string{brokerURL},
		ConnectTimeout: testtime.D5s,
		KeepAlive:      testtime.D10s,
		Backoff: BackoffConfig{
			BaseDelay: testtime.D100ms,
			MaxDelay:  testtime.D2s,
		},
		PublishTimeout: testtime.D5s,
	}
}

// TestIntegration_PublisherQoS1 verifies end-to-end publish against a real
// Mosquitto broker: open connection, construct Publisher, publish a small
// payload to a valid topic, assert no error returned (broker PUBACK 0x00).
func TestIntegration_PublisherQoS1(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D20s)
	defer cancel()

	cfg := newTestConfig(t, "publish-qos1")
	conn, err := Open(ctx, clock.Real(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = conn.Close(cleanupCtx)
	})

	ns, err := ParseTopicNamespace("itest")
	if err != nil {
		t.Fatalf("ParseTopicNamespace: %v", err)
	}
	pub, err := NewPublisher(clock.Real(), conn, ns)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = pub.Close(cleanupCtx)
	})

	topic := "itest/qos1/" + uuid.NewString()
	if err := pub.Publish(ctx, topic, []byte(`{"hello":"world"}`)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
}

// TestIntegration_PublisherReconnect verifies that after a keep-alive interval
// passes, the autopaho connection remains healthy and subsequent publishes
// succeed. We use a short KeepAlive (2s) and sleep > that interval to exercise
// the keep-alive ping path — the test confirms the connection survives the
// heartbeat exchange transparently.
func TestIntegration_PublisherReconnect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D30s)
	defer cancel()

	cfg := newTestConfig(t, "publish-reconnect")
	cfg.KeepAlive = testtime.D2s // tighter keep-alive for quick failure detection

	conn, err := Open(ctx, clock.Real(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = conn.Close(cleanupCtx)
	})

	ns, err := ParseTopicNamespace("itest")
	if err != nil {
		t.Fatalf("ParseTopicNamespace: %v", err)
	}
	pub, err := NewPublisher(clock.Real(), conn, ns)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = pub.Close(cleanupCtx)
	})

	topic := "itest/reconnect/" + uuid.NewString()
	if err := pub.Publish(ctx, topic, []byte(`{"seq":1}`)); err != nil {
		t.Fatalf("Publish seq=1: %v", err)
	}

	// Sleep > KeepAlive interval — exercises keep-alive ping path.
	// Wall-clock time must pass so the KeepAlive timer fires and exercises
	// the broker heartbeat path; polling does not accelerate KeepAlive.
	time.Sleep(testtime.D3s) //archtest:allow:test-sleep keep-alive-heartbeat-traversal: wall-clock must elapse for KeepAlive timer to fire

	if err := pub.Publish(ctx, topic, []byte(`{"seq":2}`)); err != nil {
		t.Fatalf("Publish seq=2 (post keep-alive): %v", err)
	}
}

// TestIntegration_PublisherPubAckTimeout verifies that when PublishTimeout
// fires before the broker can respond, the error is classified as PUBACK
// timeout. We provoke this by setting a very short PublishTimeout (1ns) and
// expect the context deadline to fire immediately, surfacing as
// ErrAdapterMQTTPubAckTimeout per publisher.go's wrapPublishErr.
func TestIntegration_PublisherPubAckTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
	defer cancel()

	cfg := newTestConfig(t, "publish-timeout")
	cfg.PublishTimeout = time.Nanosecond // intentionally fires immediately

	conn, err := Open(ctx, clock.Real(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = conn.Close(cleanupCtx)
	})

	ns, err := ParseTopicNamespace("itest")
	if err != nil {
		t.Fatalf("ParseTopicNamespace: %v", err)
	}
	pub, err := NewPublisher(clock.Real(), conn, ns)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = pub.Close(cleanupCtx)
	})

	topic := "itest/timeout/" + uuid.NewString()
	err = pub.Publish(ctx, topic, []byte(`{"x":1}`))
	if err == nil {
		t.Fatalf("Publish succeeded; expected ErrAdapterMQTTPubAckTimeout")
	}
	// wrapPublishErr maps context.DeadlineExceeded → ErrAdapterMQTTPubAckTimeout.
	// Assert via typed errcode check rather than fragile string matching.
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("Publish err is not *errcode.Error: %v", err)
	}
	if ec.Code != ErrAdapterMQTTPubAckTimeout {
		t.Fatalf("Publish err code = %v, want %v", ec.Code, ErrAdapterMQTTPubAckTimeout)
	}
}

// TestIntegration_PublisherTrueReconnect verifies that the autopaho connection
// fully reconnects after a broker restart: it publishes, stops the broker
// container, waits for WaitConnected to return after the container restarts,
// then publishes again successfully.
func TestIntegration_PublisherTrueReconnect(t *testing.T) {
	testutil.RequireDocker(t)

	// Bring up a dedicated broker — NOT the shared one — so we can stop/start it.
	dedicatedURL, container, err := startDedicatedMosquittoContainer(t)
	if err != nil {
		t.Fatalf("start dedicated broker: %v", err)
	}
	t.Cleanup(func() {
		termCtx, cancel := context.WithTimeout(context.Background(), testtime.D10s)
		defer cancel()
		_ = container.Terminate(termCtx)
	})

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D60s)
	defer cancel()

	cid, err := ParseEphemeralClientID("itest", "true-reconnect")
	if err != nil {
		t.Fatalf("ParseEphemeralClientID: %v", err)
	}
	cfg := Config{
		ClientID:       cid,
		Brokers:        []string{testutil.LoopbackIPEndpoint(dedicatedURL)},
		ConnectTimeout: testtime.D5s,
		KeepAlive:      testtime.D10s,
		Backoff: BackoffConfig{
			BaseDelay: testtime.D100ms,
			MaxDelay:  testtime.D2s,
		},
		PublishTimeout: testtime.D5s,
	}

	conn, err := Open(ctx, clock.Real(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = conn.Close(cleanupCtx)
	})

	ns, err := ParseTopicNamespace("itest")
	if err != nil {
		t.Fatalf("ParseTopicNamespace: %v", err)
	}
	pub, err := NewPublisher(clock.Real(), conn, ns)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), testtime.D5s)
		defer c()
		_ = pub.Close(cleanupCtx)
	})

	topic := "itest/true-reconnect/" + uuid.NewString()

	// seq=1: initial publish — must succeed with broker up.
	if err := pub.Publish(ctx, topic, []byte(`{"seq":1}`)); err != nil {
		t.Fatalf("Publish seq=1: %v", err)
	}

	// Stop the broker — triggers disconnection.
	stopTimeout := testtime.D10s
	if err := container.Stop(ctx, &stopTimeout); err != nil {
		t.Fatalf("container.Stop: %v", err)
	}

	// Restart the broker container.
	if err := container.Start(ctx); err != nil {
		t.Fatalf("container.Start: %v", err)
	}

	// Wait for autopaho to reconnect.
	reconnectCtx, reconnectCancel := context.WithTimeout(ctx, testtime.D30s)
	defer reconnectCancel()
	if err := conn.WaitConnected(reconnectCtx); err != nil {
		t.Fatalf("WaitConnected after restart: %v", err)
	}

	// seq=2: publish after full reconnect — must succeed.
	if err := pub.Publish(ctx, topic, []byte(`{"seq":2}`)); err != nil {
		t.Fatalf("Publish seq=2 (post restart): %v", err)
	}
}

// startDedicatedMosquittoContainer starts an eclipse-mosquitto container for
// exclusive use by TestIntegration_PublisherTrueReconnect (stop/start lifecycle).
//
// The 1883 container port is bound to a FIXED host port via PortBindings rather
// than the default random ephemeral mapping. This is essential for the reconnect
// test: a Stop()/Start() restart re-publishes the container's ports, and with a
// random mapping Docker assigns a NEW host port on restart — leaving the broker
// URL captured here stale, so autopaho keeps dialing the dead old port and
// WaitConnected times out. Pinning the host port keeps the endpoint stable
// across restart so reconnection can actually succeed.
func startDedicatedMosquittoContainer(t *testing.T) (string, testcontainers.Container, error) {
	t.Helper()
	ctx := context.Background()

	hostPort, err := freeLoopbackTCPPort()
	if err != nil {
		return "", nil, err
	}
	hostPortStr := strconv.Itoa(hostPort)

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        testutil.MosquittoImage,
			ExposedPorts: []string{"1883/tcp"},
			Files: []testcontainers.ContainerFile{
				{
					Reader:            strings.NewReader(mosquittoConf),
					ContainerFilePath: "/mosquitto/config/mosquitto.conf",
					FileMode:          0o644,
				},
			},
			HostConfigModifier: func(hc *dockercontainer.HostConfig) {
				hc.PortBindings = dockernet.PortMap{
					dockernet.MustParsePort("1883/tcp"): []dockernet.PortBinding{
						{HostPort: hostPortStr},
					},
				}
			},
			WaitingFor: wait.ForListeningPort("1883/tcp").
				WithStartupTimeout(testtime.D30s),
		},
		Started: true,
	})
	if err != nil {
		return "", nil, err
	}
	// Endpoint is the pinned host port, stable across Stop()/Start().
	url := "tcp://127.0.0.1:" + hostPortStr
	return url, container, nil
}

// freeLoopbackTCPPort allocates and immediately releases a loopback TCP port,
// returning its number for use as a fixed container host-port binding. The
// close-then-rebind window is small and test-binary-scoped (no external
// competitor for a loopback ephemeral port within one `go test` run) — the same
// idiom the in-process mochi broker tests use.
func freeLoopbackTCPPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if cerr := ln.Close(); cerr != nil {
		return 0, cerr
	}
	return port, nil
}
