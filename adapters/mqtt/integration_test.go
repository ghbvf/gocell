//go:build integration

package mqtt

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/kernel/clock"
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
		ConnectTimeout: 5 * time.Second,
		KeepAlive:      10 * time.Second,
		Backoff: BackoffConfig{
			BaseDelay: 100 * time.Millisecond,
			MaxDelay:  2 * time.Second,
		},
		PublishTimeout: 5 * time.Second,
	}
}

// TestIntegration_PublisherQoS1 verifies end-to-end publish against a real
// Mosquitto broker: open connection, construct Publisher, publish a small
// payload to a valid topic, assert no error returned (broker PUBACK 0x00).
func TestIntegration_PublisherQoS1(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cfg := newTestConfig(t, "publish-qos1")
	conn, err := Open(ctx, clock.Real(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
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
		cleanupCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := newTestConfig(t, "publish-reconnect")
	cfg.KeepAlive = 2 * time.Second // tighter keep-alive for quick failure detection

	conn, err := Open(ctx, clock.Real(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
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
		cleanupCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = pub.Close(cleanupCtx)
	})

	topic := "itest/reconnect/" + uuid.NewString()
	if err := pub.Publish(ctx, topic, []byte(`{"seq":1}`)); err != nil {
		t.Fatalf("Publish seq=1: %v", err)
	}

	// Sleep > KeepAlive interval — exercises keep-alive ping path.
	time.Sleep(3 * time.Second)

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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := newTestConfig(t, "publish-timeout")
	cfg.PublishTimeout = time.Nanosecond // intentionally fires immediately

	conn, err := Open(ctx, clock.Real(), cfg)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
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
		cleanupCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = pub.Close(cleanupCtx)
	})

	topic := "itest/timeout/" + uuid.NewString()
	err = pub.Publish(ctx, topic, []byte(`{"x":1}`))
	if err == nil {
		t.Fatalf("Publish succeeded; expected ErrAdapterMQTTPubAckTimeout")
	}
	// wrapPublishErr maps context.DeadlineExceeded → ErrAdapterMQTTPubAckTimeout
	// with message "mqtt: PUBACK timeout". Accept any timeout-related text to be
	// resilient to minor message wording changes.
	msg := err.Error()
	if !strings.Contains(msg, "PUBACK") && !strings.Contains(msg, "puback") && !strings.Contains(msg, "timeout") {
		t.Fatalf("Publish err = %v; want PUBACK timeout-related", err)
	}
}
