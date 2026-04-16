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

// waitForSubscription replaces time.Sleep(subscribeInitDelay) with a
// deterministic readiness check when the subscriber implements
// outbox.SubscriberInitializer. For persistent brokers (e.g., RabbitMQ),
// InitializeSubscription pre-declares the topology so that published messages
// are queued before Subscribe starts consuming — eliminating the race.
// For in-memory implementations that lack InitializeSubscription, it falls
// back to the brief sleep.
//
// ref: Watermill message.SubscribeInitializer — synchronous topology pre-creation.
func waitForSubscription(t *testing.T, ctx context.Context, sub outbox.Subscriber, topic, consumerGroup string) {
	t.Helper()
	if init, ok := sub.(outbox.SubscriberInitializer); ok {
		err := init.InitializeSubscription(ctx, topic, consumerGroup)
		if err == nil {
			return // deterministic initialization succeeded
		}
		if !errors.Is(err, outbox.ErrInitializerNotSupported) {
			t.Fatalf("InitializeSubscription(%s, %s): %v", topic, consumerGroup, err)
		}
		// Inner does not support initialization — fall through to sleep.
	}
	time.Sleep(subscribeInitDelay)
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
func PublishN(t *testing.T, ctx context.Context, pub outbox.Publisher, topic string, n int) []outbox.Entry {
	t.Helper()
	entries := make([]outbox.Entry, 0, n)
	for i := range n {
		payload := testPayload(i)
		entry := NewEntry(topic, payload)
		if err := pub.Publish(ctx, topic, payload); err != nil {
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

	// Subscribe blocks — run in goroutine.
	subDone := make(chan struct{})
	go func() {
		defer close(subDone)
		_ = sub.Subscribe(subCtx, topic, handler, "")
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
		err := sub.Subscribe(subCtx, topic, func(_ context.Context, entry outbox.Entry) outbox.HandleResult {
			c.mu.Lock()
			c.collected = append(c.collected, entry)
			count := len(c.collected)
			c.mu.Unlock()
			if count >= c.n {
				c.closeOnce.Do(func() { close(c.done) })
			}
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		}, "")
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
type pubSubHarness struct {
	T       *testing.T
	Pub     outbox.Publisher
	Sub     outbox.Subscriber
	Topic   string
	done    chan struct{}
	once    sync.Once
	subDone chan struct{}
	cancel  context.CancelFunc
}

// newHarness creates a pubSubHarness from a PubSubConstructor.
func newHarness(t *testing.T, constructor PubSubConstructor) *pubSubHarness {
	t.Helper()
	pub, sub := constructor(t)
	return &pubSubHarness{
		T:       t,
		Pub:     pub,
		Sub:     sub,
		Topic:   TestTopic(t),
		done:    make(chan struct{}),
		subDone: make(chan struct{}),
	}
}

// subscribe launches a Subscribe goroutine with the given handler.
// Waits for the goroutine to start and Subscribe to be called.
// Subscribe errors (other than context.Canceled) are surfaced via t.Errorf.
func (h *pubSubHarness) subscribe(handler outbox.EntryHandler) {
	h.T.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.T.Cleanup(cancel)
	ready := make(chan struct{})
	go func() {
		defer close(h.subDone)
		close(ready)
		err := h.Sub.Subscribe(ctx, h.Topic, handler, "")
		if err != nil && !errors.Is(err, context.Canceled) {
			h.T.Errorf("unexpected Subscribe error: %v", err)
		}
	}()
	<-ready
	waitForSubscription(h.T, ctx, h.Sub, h.Topic, "")
}

// publishAndWait publishes payload and blocks until signalDone or timeout.
func (h *pubSubHarness) publishAndWait(payload []byte) {
	h.T.Helper()
	assertNoError(h.T, h.Pub.Publish(context.Background(), h.Topic, payload))
	select {
	case <-h.done:
	case <-time.After(defaultTimeout):
		h.T.Fatal("timed out")
	}
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
