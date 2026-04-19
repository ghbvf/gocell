//go:build integration

package integration

// TestE2E_ShutdownBarrier validates the Subscriber shutdown barrier against a
// real RabbitMQ broker:
//
//  1. NoMessageLoss: external ctx cancel does not lose messages — every
//     published message is either processed by the handler or remains in the
//     broker queue. StopIntake stops intake, drains in-flight, then Close()
//     finishes cleanly within the ShutdownTimeout budget.
//
//  2. BrokerHardClose: the broker container is stopped while the subscriber is
//     running. Subscriber.Close() must return within a bounded timeout rather
//     than hanging indefinitely.
//
// Both tests use the lightweight setup (Subscriber + Publisher directly) rather
// than full bootstrap, because bootstrap brings assembly, HTTP, workers and
// config watcher. That complexity is not load-bearing for the shutdown-barrier
// invariants under test here. The subscriber-layer behavior is identical
// regardless of whether bootstrap or direct wiring is used.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/adapters/rabbitmq"
	"github.com/ghbvf/gocell/kernel/outbox"
	outboxrt "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/tests/testutil"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcrabbitmq "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// startShutdownTestBroker starts a dedicated RabbitMQ testcontainer and returns
// the AMQP URL, the HTTP management base URL, the container (for hard-stop
// tests), and a cleanup function.
//
// Uses a dedicated container (not the shared broker) because Test 2 terminates
// the container — a broker-level side-effect that would break other tests
// running against the same broker.
func startShutdownTestBroker(t *testing.T) (amqpURL, mgmtURL string, container *tcrabbitmq.RabbitMQContainer, cleanup func()) {
	t.Helper()
	testutil.RequireDocker(t)

	ctx := context.Background()
	c, err := tcrabbitmq.Run(ctx, testutil.RabbitMQImage)
	require.NoError(t, err, "start rabbitmq container for shutdown e2e")

	amqp, err := c.AmqpURL(ctx)
	require.NoError(t, err, "get amqp url")

	mgmt, err := c.HttpURL(ctx)
	require.NoError(t, err, "get management http url")

	cleanup = func() {
		if termErr := c.Terminate(ctx); termErr != nil {
			t.Logf("WARN: failed to terminate rabbitmq container: %v", termErr)
		}
	}
	return amqp, mgmt, c, cleanup
}

// newShutdownTestConn opens a Connection against the given URL and registers
// t.Cleanup to close it.
func newShutdownTestConn(t *testing.T, amqpURL string) *rabbitmq.Connection {
	t.Helper()
	conn, err := rabbitmq.NewConnection(rabbitmq.Config{
		URL:                 amqpURL,
		ChannelPoolSize:     5,
		ConfirmTimeout:      10 * time.Second,
		ReconnectMaxBackoff: 5 * time.Second,
		ReconnectBaseDelay:  500 * time.Millisecond,
	})
	require.NoError(t, err, "create rabbitmq connection for shutdown e2e")
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

// getQueueDepth queries the RabbitMQ management HTTP API for the number of
// messages ready in the default vhost queue named queueName.
// Returns -1 on error (test-only helper; callers check via assert).
func getQueueDepth(t *testing.T, mgmtURL, queueName string) int {
	t.Helper()
	// Default vhost is "%2F" (URL-encoded "/").
	url := fmt.Sprintf("%s/api/queues/%%2F/%s", mgmtURL, queueName)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Logf("getQueueDepth: build request: %v", err)
		return -1
	}
	req.SetBasicAuth("guest", "guest")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("getQueueDepth: http get: %v", err)
		return -1
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		// Queue may not exist yet (no messages ever delivered).
		return 0
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Logf("getQueueDepth: unexpected status %d: %s", resp.StatusCode, body)
		return -1
	}

	var result struct {
		Messages int `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Logf("getQueueDepth: decode: %v", err)
		return -1
	}
	return result.Messages
}

// publishMessages publishes `count` messages to `topic` via `pub`, each
// carrying a v1 wire envelope so the subscriber's fail-closed deserialization
// accepts them. Returns on first error.
func publishMessages(ctx context.Context, pub *rabbitmq.Publisher, topic string, count int) error {
	for i := 0; i < count; i++ {
		entry := outbox.Entry{
			ID:            uuid.New().String(),
			AggregateID:   fmt.Sprintf("agg-%d", i),
			AggregateType: "shutdown-e2e",
			EventType:     topic,
			Payload:       []byte(fmt.Sprintf(`{"seq":%d}`, i)),
			CreatedAt:     time.Now().UTC(),
		}
		payload, err := outboxrt.MarshalEnvelope(outboxrt.ClaimedEntry{Entry: entry})
		if err != nil {
			return fmt.Errorf("marshal envelope %d: %w", i, err)
		}
		if err := pub.Publish(ctx, topic, payload); err != nil {
			return fmt.Errorf("publish %d: %w", i, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Test 1: No message loss across graceful shutdown
// ---------------------------------------------------------------------------

// TestE2E_ShutdownBarrier_NoMessageLoss validates the core shutdown-barrier
// invariant: when the external context is cancelled while messages are in
// flight, every published message is accounted for. It must either have been
// processed by the handler (processed counter) or remain in the broker queue
// (getQueueDepth). Zero messages may be silently discarded.
//
// Sequence:
//  1. Start dedicated RabbitMQ container.
//  2. Start Subscriber with 50 ms processing delay per message.
//  3. Publish 100 messages.
//  4. Wait until ≥1 message is processed (confirms in-flight processing).
//  5. Call StopIntake — stops new intake, drains already-prefetched messages.
//  6. Call Close — waits for all processDelivery goroutines to finish.
//  7. Assert: processed + queue_depth == 100 (no message lost).
//  8. Assert: Close returned nil (clean shutdown within ShutdownTimeout).
func TestE2E_ShutdownBarrier_NoMessageLoss(t *testing.T) {
	const (
		total           = 100
		topic           = "shutdown.e2e.noloss"
		queueName       = "shutdown.e2e.noloss.queue"
		dlxExchange     = "shutdown.e2e.noloss.dlx"
		processingDelay = 50 * time.Millisecond
		shutdownTimeout = 15 * time.Second
	)

	amqpURL, mgmtURL, _, cleanup := startShutdownTestBroker(t)
	defer cleanup()

	pubConn := newShutdownTestConn(t, amqpURL)
	subConn := newShutdownTestConn(t, amqpURL)

	pub := rabbitmq.NewPublisher(pubConn)
	sub := rabbitmq.NewSubscriber(subConn, rabbitmq.SubscriberConfig{
		QueueName:       queueName,
		PrefetchCount:   10,
		DLXExchange:     dlxExchange,
		ShutdownTimeout: shutdownTimeout,
	})

	// Handler counts processed messages. Simulate work with a delay.
	var processed atomic.Int64
	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		time.Sleep(processingDelay)
		processed.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	// Start subscriber.
	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()

	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- sub.Subscribe(subCtx, outbox.Subscription{
			Topic:         topic,
			ConsumerGroup: "cg-shutdown-noloss",
		}, handler)
	}()

	waitForSubscriberReady(t, subConn, queueName, subErrCh, 10*time.Second)

	// Publish 100 messages. Use a separate context so publish is not affected
	// by subCtx cancellation below.
	pubCtx := context.Background()
	require.NoError(t, publishMessages(pubCtx, pub, topic, total), "publish 100 messages")

	// Wait until at least 1 message is processed (subscriber is actively consuming).
	require.Eventually(t, func() bool {
		return processed.Load() > 0
	}, 10*time.Second, 50*time.Millisecond, "at least one message must be processed before shutdown")

	// Phase 1: StopIntake — stop accepting new broker deliveries, drain
	// already-prefetched messages in the consumeLoop's deliveries buffer.
	require.NoError(t, sub.StopIntake(subCtx), "StopIntake must succeed")

	// Phase 2: Close — wait for all in-flight processDelivery goroutines to
	// finish. Must return nil (no ErrAdapterAMQPCloseTimeout) because
	// StopIntake drained the backlog before Close() was called.
	closeErr := sub.Close()
	require.NoError(t, closeErr, "Close must return nil after StopIntake drained in-flight messages")

	// Cancel subscriber context so the Subscribe goroutine returns.
	subCancel()

	// F4: assert the Subscribe goroutine exits cleanly. A non-nil error other
	// than context.Canceled indicates an unexpected crash — account conservation
	// alone (processed + queue == total) would not detect this regression.
	select {
	case subErr := <-subErrCh:
		if subErr != nil && !errors.Is(subErr, context.Canceled) {
			require.NoError(t, subErr,
				"Subscribe goroutine must exit with nil or context.Canceled; got %v", subErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Subscribe goroutine did not exit after Close + subCancel within 5s")
	}

	// Accounting: processed + messages remaining in broker queue must equal total.
	// Allow a brief moment for the broker to acknowledge acks and update queue
	// stats (management API is eventually consistent for short windows).
	var queueDepth int
	require.Eventually(t, func() bool {
		queueDepth = getQueueDepth(t, mgmtURL, queueName)
		return queueDepth >= 0
	}, 5*time.Second, 200*time.Millisecond, "management API must return valid queue depth")

	processedFinal := processed.Load()
	t.Logf("shutdown e2e no-loss: processed=%d queue=%d total=%d", processedFinal, queueDepth, total)

	assert.EqualValues(t, total, processedFinal+int64(queueDepth),
		"no message lost: processed=%d queue=%d total=%d", processedFinal, queueDepth, total)
}

// ---------------------------------------------------------------------------
// Test 2: Broker hard close (container stop) — subscriber must not hang
// ---------------------------------------------------------------------------

// TestE2E_ShutdownBarrier_BrokerHardClose verifies that when the broker
// container is stopped while the subscriber is running, Subscriber.Close()
// returns within a bounded timeout rather than blocking indefinitely.
//
// This exercises the "broker hard close" path: the underlying AMQP connection
// is severed by the broker, which causes the delivery channel to close and the
// consumeLoop to surface errSubscriptionLost. The test then calls Close() and
// verifies it completes within shutdownTimeout + a small buffer.
//
// Sequence:
//  1. Start dedicated RabbitMQ container.
//  2. Start Subscriber, publish a few messages, wait for consumption to begin.
//  3. Terminate the broker container (hard kill — equivalent to kill -9).
//  4. Call Subscriber.Close() and assert it returns within the budget.
func TestE2E_ShutdownBarrier_BrokerHardClose(t *testing.T) {
	const (
		topic           = "shutdown.e2e.hardclose"
		queueName       = "shutdown.e2e.hardclose.queue"
		dlxExchange     = "shutdown.e2e.hardclose.dlx"
		shutdownTimeout = 5 * time.Second
		// Buffer beyond shutdownTimeout before declaring the test failed.
		// Accounts for OS-level socket timeout detection (~1-2s) on top of the
		// ShutdownTimeout budget.
		totalBudget = shutdownTimeout + 8*time.Second
	)

	amqpURL, _, container, cleanup := startShutdownTestBroker(t)
	// Do NOT defer cleanup here: we terminate the container explicitly as part
	// of the test scenario. Register a safety-net cleanup in case the test
	// exits early before the explicit Terminate call.
	containerTerminated := false
	t.Cleanup(func() {
		if !containerTerminated {
			cleanup()
		}
	})

	pubConn := newShutdownTestConn(t, amqpURL)
	subConn, err := rabbitmq.NewConnection(rabbitmq.Config{
		URL:                 amqpURL,
		ChannelPoolSize:     5,
		ConfirmTimeout:      10 * time.Second,
		ReconnectMaxBackoff: 3 * time.Second,
		ReconnectBaseDelay:  200 * time.Millisecond,
	})
	require.NoError(t, err, "create subscriber connection")
	// Do NOT defer subConn.Close here — the broker will be killed; the
	// connection teardown is handled implicitly via process exit / GC.

	pub := rabbitmq.NewPublisher(pubConn)
	sub := rabbitmq.NewSubscriber(subConn, rabbitmq.SubscriberConfig{
		QueueName:       queueName,
		PrefetchCount:   5,
		DLXExchange:     dlxExchange,
		ShutdownTimeout: shutdownTimeout,
	})

	// Simple handler — just counts deliveries.
	var consumed atomic.Int64
	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		consumed.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	subCtx, subCancel := context.WithCancel(context.Background())
	defer subCancel()

	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- sub.Subscribe(subCtx, outbox.Subscription{
			Topic:         topic,
			ConsumerGroup: "cg-shutdown-hardclose",
		}, handler)
	}()

	waitForSubscriberReady(t, subConn, queueName, subErrCh, 10*time.Second)

	// Publish a handful of messages.
	require.NoError(t, publishMessages(context.Background(), pub, topic, 5), "publish messages before hard close")

	// Wait until at least one message has been consumed before hard-stopping
	// the broker, to ensure we have in-flight state to exercise.
	require.Eventually(t, func() bool {
		return consumed.Load() > 0
	}, 5*time.Second, 50*time.Millisecond,
		"at least one message should be consumed before broker stop")

	// Hard-stop the broker container. This forcibly severs all AMQP connections
	// without a clean AMQP close handshake, simulating a broker crash or OOM
	// kill. The subscriber's delivery channel will close (errSubscriptionLost),
	// and the reconnect goroutine will begin retrying but find the broker gone.
	t.Log("stopping rabbitmq container (hard kill)...")
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer stopCancel()
	require.NoError(t, container.Stop(stopCtx, nil), "stop rabbitmq container")
	containerTerminated = true
	t.Log("rabbitmq container stopped")

	// Now call Close() and assert it returns within totalBudget.
	// The subscriber may be in the reconnect wait loop (WaitConnected); Close()
	// must unblock that wait via closeCh and then return after wg.Wait or
	// ShutdownTimeout, whichever comes first.
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- sub.Close()
	}()

	select {
	case closeErr := <-closeDone:
		t.Logf("shutdown e2e hard-close: Close() returned after broker kill, err=%v", closeErr)
		// Close() MAY return ErrAdapterAMQPCloseTimeout if in-flight handlers were
		// blocked (the ShutdownTimeout governs that path). Both nil and
		// ErrAdapterAMQPCloseTimeout are acceptable — what matters is that it
		// returned within the totalBudget and did not hang indefinitely.
		// The test assertion is purely temporal (bounded return).
	case <-time.After(totalBudget):
		t.Fatalf("Subscriber.Close() did not return within %s after broker hard kill — subscriber is hanging", totalBudget)
	}
}
