package outboxtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// waitForSubscription waits until the subscriber is ready to receive messages
// for the given topic. It calls Setup to declare topology, then waits for the
// Ready channel to close. For persistent brokers, Setup pre-declares queues so
// messages are durably queued before Subscribe starts consuming. For in-memory
// implementations, Ready blocks until the goroutine calls Subscribe.
//
// ref: Watermill message.SubscribeInitializer — synchronous topology pre-creation.
func waitForSubscription(t *testing.T, ctx context.Context, sub outbox.Subscriber, topic, consumerGroup string) {
	t.Helper()
	subSpec := outbox.Subscription{Topic: topic, ConsumerGroup: consumerGroup}
	if err := sub.Setup(ctx, subSpec); err != nil {
		t.Fatalf("waitForSubscription: Setup(%s): %v", topic, err)
	}
	select {
	case <-sub.Ready(subSpec):
	case <-ctx.Done():
		t.Fatalf("waitForSubscription: context cancelled before subscriber ready: %v", ctx.Err())
	case <-time.After(subscribeInitDelay):
		// Fallback: subscriber did not signal Ready within init delay. This is
		// acceptable for implementations that return a never-closing Ready channel
		// (e.g., persistent brokers where setup is fire-and-forget). The caller
		// may experience delivery loss on the first message; that is acceptable
		// for non-persistent test setups.
	}
}

// TestTopic returns a unique topic name scoped to the given test.
// Prevents cross-test interference when implementations share state.
func TestTopic(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("outboxtest: crypto/rand failed: %v", err)
	}
	return fmt.Sprintf("test-%s-%s", t.Name(), hex.EncodeToString(b))
}

// NewEntry creates a valid Entry with a unique ID for testing.
func NewEntry(topic string, payload []byte) outbox.Entry {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return outbox.Entry{
		ID:        "evt-" + hex.EncodeToString(b),
		EventType: topic,
		Topic:     topic,
		Payload:   payload,
		CreatedAt: time.Now(),
	}
}

// NewEntryWithMetadata creates a valid Entry with metadata.
func NewEntryWithMetadata(topic string, payload []byte, metadata map[string]string) outbox.Entry {
	e := NewEntry(topic, payload)
	e.Metadata = metadata
	return e
}

// testPayload creates a JSON payload with a sequence number.
func testPayload(seq int) []byte {
	data, _ := json.Marshal(map[string]int{"seq": seq})
	return data
}

// PublishN publishes n messages to the given topic and returns the entries published.
// Messages are wrapped in v1 wire envelopes so relay-aware implementations
// (e.g. InMemoryEventBus after P1-14) can receive them. Subscribers receive
// the unwrapped business payload.
func PublishN(t *testing.T, ctx context.Context, pub outbox.Publisher, topic string, n int) []outbox.Entry {
	t.Helper()
	entries := make([]outbox.Entry, 0, n)
	for i := range n {
		payload := testPayload(i)
		entry := NewEntry(topic, payload)
		if err := pub.Publish(ctx, topic, wrapV1Envelope(t, topic, payload)); err != nil {
			t.Fatalf("publish message %d: %v", i, err)
		}
		entries = append(entries, entry)
	}
	return entries
}

// CollectN starts a subscriber and collects exactly n entries, with a timeout.
// It launches Subscribe in a goroutine (blocking interface) and collects
// via mutex+slice. Returns collected entries. Fails the test on timeout.
//
// IMPORTANT: CollectN only subscribes — the caller must publish messages
// AFTER calling CollectN (or before, if the implementation is persistent).
// For at-most-once implementations (e.g., InMemoryEventBus), publish after
// CollectN returns control, since it includes a subscribeInitDelay wait.
func CollectN(
	t *testing.T,
	ctx context.Context,
	sub outbox.Subscriber,
	topic string,
	n int,
	timeout time.Duration,
) []outbox.Entry {
	t.Helper()

	var (
		mu        sync.Mutex
		collected []outbox.Entry
		done      = make(chan struct{})
		closeOnce sync.Once
	)

	subCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)

	handler := func(_ context.Context, entry outbox.Entry) outbox.HandleResult {
		mu.Lock()
		collected = append(collected, entry)
		count := len(collected)
		mu.Unlock()

		if count >= n {
			closeOnce.Do(func() { close(done) })
		}
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	// Subscribe blocks -- run in goroutine.
	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, outbox.Subscription{Topic: topic}, handler)
	}()

	// Wait for subscription to register. Uses SubscriberInitializer if available,
	// otherwise falls back to a brief sleep.
	waitForSubscription(t, ctx, sub, topic, "")

	select {
	case <-done:
	case <-time.After(timeout):
		mu.Lock()
		got := len(collected)
		mu.Unlock()
		t.Fatalf("CollectN: timed out after %v, collected %d/%d entries", timeout, got, n)
	}

	cancel()
	<-subDone

	mu.Lock()
	defer mu.Unlock()
	return collected
}

// ---------------------------------------------------------------------------
// collector — two-phase subscribe helper to reduce conformance.go duplication
// ---------------------------------------------------------------------------

// collector starts a subscriber in a goroutine and collects entries.
// Phase 1: call startCollecting — subscriber goroutine starts, waits for init.
// Phase 2: caller publishes messages.
// Phase 3: call waitAndGet — blocks until n entries collected or timeout.
type collector struct {
	t         *testing.T
	mu        sync.Mutex
	collected []outbox.Entry
	done      chan struct{}
	closeOnce sync.Once
	cancel    context.CancelFunc
	subDone   chan struct{}
	n         int
}

// startCollecting launches a subscriber goroutine that collects entries.
// Returns after the subscriber goroutine is running (ready channel handshake).
// Call waitAndGet to block until n entries arrive.
func startCollecting(t *testing.T, ctx context.Context, sub outbox.Subscriber, topic string, n int) *collector {
	t.Helper()
	c := &collector{
		t:       t,
		done:    make(chan struct{}),
		subDone: make(chan struct{}),
		n:       n,
	}

	subCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	t.Cleanup(cancel)

	ready := make(chan struct{})
	go func() {
		defer close(c.subDone)
		close(ready) // signal: goroutine is running, Subscribe call is imminent
		err := sub.Subscribe(subCtx, outbox.Subscription{Topic: topic}, func(_ context.Context, entry outbox.Entry) outbox.HandleResult {
			c.mu.Lock()
			c.collected = append(c.collected, entry)
			count := len(c.collected)
			c.mu.Unlock()
			if count >= c.n {
				c.closeOnce.Do(func() { close(c.done) })
			}
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			c.t.Errorf("unexpected Subscribe error: %v", err)
		}
	}()
	<-ready
	// Wait for subscription to register. Uses SubscriberInitializer if available,
	// otherwise falls back to a brief sleep to cover the window between goroutine
	// start and Subscribe's internal registration.
	waitForSubscription(t, ctx, sub, topic, "")
	return c
}

// waitAndGet blocks until n entries are collected or timeout elapses.
// Cancels the subscriber and returns the collected entries.
func (c *collector) waitAndGet(timeout time.Duration) []outbox.Entry {
	c.t.Helper()
	select {
	case <-c.done:
	case <-time.After(timeout):
		c.mu.Lock()
		got := len(c.collected)
		c.mu.Unlock()
		c.t.Fatalf("collector: timed out after %v, collected %d/%d", timeout, got, c.n)
	}
	c.cancel()
	<-c.subDone

	c.mu.Lock()
	defer c.mu.Unlock()
	return c.collected
}

// ---------------------------------------------------------------------------
// pubSubHarness — single-message test scaffold to reduce conformance.go duplication
// ---------------------------------------------------------------------------

// pubSubHarness wires up the common subscribe→publish→wait→teardown pattern
// used by most single-message conformance tests.
//
// Each delivery captured via subscribe() produces one non-blocking send on
// deliveryEvents; tests use assertNoMoreDeliveries / checkNoMoreDeliveries to
// verify "no further delivery" via select+timeout (Watermill pattern) rather
// than time.Sleep — enabling fail-fast detection of unexpected redeliveries.
//
// All struct fields beyond the already-exported Pub/Sub/T/Topic are package-
// internal; tests in this package may set drainTimeout directly to shorten
// the prior-delivery drain phase. External callers should not depend on the
// internal fields.
type pubSubHarness struct {
	T              *testing.T
	Pub            outbox.Publisher
	Sub            outbox.Subscriber
	Topic          string
	done           chan struct{}
	once           sync.Once
	subDone        chan struct{}
	cancel         context.CancelFunc
	deliveryEvents chan struct{}
	drainTimeout   time.Duration
}

// deliveryEventsBuffer is the buffered size of the delivery-tracking channel.
// Single-message conformance tests expect <10 deliveries; 100 is ample and
// bounds memory. Non-blocking sends drop overflow to avoid perturbing the
// handler under test — which is safe for tests exercising low delivery counts
// but means assertNoMoreDeliveries is NOT appropriate for scenarios producing
// >100 legitimate deliveries in a single subtest (e.g. high-frequency
// requeue loops); for those, use counter-based assertions directly.
const deliveryEventsBuffer = 100

// newHarness creates a pubSubHarness from a PubSubConstructor.
func newHarness(t *testing.T, constructor PubSubConstructor) *pubSubHarness {
	t.Helper()
	pub, sub := constructor(t)
	return &pubSubHarness{
		T:              t,
		Pub:            pub,
		Sub:            sub,
		Topic:          TestTopic(t),
		done:           make(chan struct{}),
		subDone:        make(chan struct{}),
		deliveryEvents: make(chan struct{}, deliveryEventsBuffer),
		drainTimeout:   defaultTimeout,
	}
}

// subscribe launches a Subscribe goroutine with the given handler.
// Waits for the goroutine to start and Subscribe to be called.
// Every handler invocation emits a non-blocking send on deliveryEvents so that
// negative-assertion helpers can detect redeliveries via select+timeout.
// Subscribe errors (other than context.Canceled) are surfaced via t.Errorf.
func (h *pubSubHarness) subscribe(handler outbox.EntryHandler) {
	h.T.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.T.Cleanup(cancel)
	wrapped := func(hctx context.Context, entry outbox.Entry) outbox.HandleResult {
		select {
		case h.deliveryEvents <- struct{}{}:
		default:
		}
		return handler(hctx, entry)
	}
	ready := make(chan struct{})
	go func() {
		defer close(h.subDone)
		close(ready)
		err := h.Sub.Subscribe(ctx, outbox.Subscription{Topic: h.Topic}, wrapped)
		if err != nil && !errors.Is(err, context.Canceled) {
			h.T.Errorf("unexpected Subscribe error: %v", err)
		}
	}()
	<-ready
	waitForSubscription(h.T, ctx, h.Sub, h.Topic, "")
}

// publishAndWait wraps payload in a v1 wire envelope and publishes it, then
// blocks until signalDone or timeout. Wrapping is required because relay-aware
// implementations (e.g. InMemoryEventBus after P1-14) reject non-v1 payloads
// at the consumer entry point — they dead-letter the message instead of
// delivering it to subscribers.
func (h *pubSubHarness) publishAndWait(payload []byte) {
	h.T.Helper()
	assertNoError(h.T, h.Pub.Publish(context.Background(), h.Topic, wrapV1Envelope(h.T, h.Topic, payload)))
	select {
	case <-h.done:
	case <-time.After(defaultTimeout):
		h.T.Fatal("timed out")
	}
}

// wrapV1Envelope constructs a minimal v1 wire envelope JSON for the given topic
// and business payload. Used by conformance helpers so implementations that
// enforce schemaVersion:"v1" (e.g. InMemoryEventBus after P1-14) can receive
// test messages. The subscriber will receive the unwrapped business payload.
//
// Constructed manually to avoid importing runtime/outbox from the kernel layer.
func wrapV1Envelope(t testing.TB, topic string, payload []byte) []byte {
	t.Helper()
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := "conf-" + hex.EncodeToString(b)

	type wireMsg struct {
		SchemaVersion string          `json:"schemaVersion"`
		ID            string          `json:"id"`
		EventType     string          `json:"eventType"`
		Topic         string          `json:"topic"`
		Payload       json.RawMessage `json:"payload"`
		CreatedAt     string          `json:"createdAt"`
	}
	msg := wireMsg{
		SchemaVersion: "v1",
		ID:            id,
		EventType:     topic,
		Topic:         topic,
		Payload:       json.RawMessage(payload),
		CreatedAt:     "2024-01-01T00:00:00Z",
	}
	out, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("wrapV1Envelope: %v", err)
	}
	return out
}

// signalDone closes the done channel (safe for concurrent calls via sync.Once).
func (h *pubSubHarness) signalDone() {
	h.once.Do(func() { close(h.done) })
}

// teardown cancels the subscriber and waits for the goroutine to exit.
func (h *pubSubHarness) teardown() {
	h.cancel()
	<-h.subDone
}

// assertNoMoreDeliveries drains priorCount expected deliveries, then verifies
// no additional delivery arrives within window. Fails the test via t.Fatalf on
// drain timeout or unexpected redelivery. This replaces the legacy
// `time.Sleep(window); assertEqual(counter)` pattern with fail-fast select.
//
// ref: ThreeDotsLabs/watermill pubsub/tests/test_pubsub.go — select+time.After
// negative-assertion pattern.
func (h *pubSubHarness) assertNoMoreDeliveries(priorCount int, window time.Duration, msg string) {
	h.T.Helper()
	if err := h.checkNoMoreDeliveries(priorCount, window); err != nil {
		h.T.Fatalf("%s: %v", msg, err)
	}
}

// checkNoMoreDeliveries is the testable core of assertNoMoreDeliveries. It
// first drains priorCount events (expected prior deliveries), then blocks for
// `window` to detect any unexpected redelivery. Returns nil on success; a
// descriptive error if drain times out or a redelivery arrives.
//
// Drain timeout is controlled by h.drainTimeout (default: defaultTimeout);
// the `window` parameter only bounds the negative-assertion wait after the
// drain succeeds. Tests may shorten h.drainTimeout to exercise the shortfall
// error path without waiting the full default.
func (h *pubSubHarness) checkNoMoreDeliveries(priorCount int, window time.Duration) error {
	for i := range priorCount {
		select {
		case <-h.deliveryEvents:
		case <-time.After(h.drainTimeout):
			return fmt.Errorf("expected %d prior deliveries, only got %d", priorCount, i)
		}
	}
	select {
	case <-h.deliveryEvents:
		return fmt.Errorf("unexpected delivery within %s", window)
	case <-time.After(window):
		return nil
	}
}

// ---------------------------------------------------------------------------
// Internal assertion helpers — stdlib only, no testify in kernel/ non-test files
// ---------------------------------------------------------------------------

func assertNoError(t *testing.T, err error, msgAndArgs ...any) {
	t.Helper()
	if err != nil {
		if len(msgAndArgs) > 0 {
			t.Fatalf("unexpected error: %v — %s", err, fmt.Sprint(msgAndArgs...))
		}
		t.Fatalf("unexpected error: %v", err)
	}
}

func assertEqual(t *testing.T, want, got any, msgAndArgs ...any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		suffix := ""
		if len(msgAndArgs) > 0 {
			suffix = " — " + fmt.Sprint(msgAndArgs...)
		}
		t.Fatalf("want %v, got %v%s", want, got, suffix)
	}
}

func assertBytesEqual(t *testing.T, want, got []byte, msgAndArgs ...any) {
	t.Helper()
	if string(want) != string(got) {
		suffix := ""
		if len(msgAndArgs) > 0 {
			suffix = " — " + fmt.Sprint(msgAndArgs...)
		}
		t.Fatalf("want %q, got %q%s", want, got, suffix)
	}
}

func assertTrue(t *testing.T, condition bool, msgAndArgs ...any) {
	t.Helper()
	if !condition {
		if len(msgAndArgs) > 0 {
			t.Fatalf("expected true: %s", fmt.Sprint(msgAndArgs...))
		}
		t.Fatal("expected true")
	}
}

func assertFalse(t *testing.T, condition bool, msgAndArgs ...any) {
	t.Helper()
	if condition {
		if len(msgAndArgs) > 0 {
			t.Fatalf("expected false: %s", fmt.Sprint(msgAndArgs...))
		}
		t.Fatal("expected false")
	}
}

func assertLen(t *testing.T, length, want int, msgAndArgs ...any) {
	t.Helper()
	if length != want {
		if len(msgAndArgs) > 0 {
			t.Fatalf("want length %d, got %d — %s", want, length, fmt.Sprint(msgAndArgs...))
		}
		t.Fatalf("want length %d, got %d", want, length)
	}
}

func assertEventually(t *testing.T, condition func() bool, timeout, poll time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(poll)
	}
	t.Fatalf("condition not met within %v: %s", timeout, msg)
}

func assertNotPanics(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic: %v", r)
		}
	}()
	f()
}
