package outboxtest

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
)

func TestTestTopic_UniquePerTest(t *testing.T) {
	topic1 := TestTopic(t)
	topic2 := TestTopic(t)

	if topic1 == topic2 {
		t.Fatal("TestTopic should return unique values per call")
	}
	if !strings.HasPrefix(topic1, "test-") {
		t.Fatalf("TestTopic should start with 'test-', got %q", topic1)
	}
	if !strings.Contains(topic1, t.Name()) {
		t.Fatalf("TestTopic should contain test name, got %q", topic1)
	}
}

func TestNewEntry_ValidFields(t *testing.T) {
	entry := NewEntry("my.topic", []byte(`{"k":"v"}`))

	if entry.ID == "" {
		t.Fatal("NewEntry ID must not be empty")
	}
	if !strings.HasPrefix(entry.ID, "evt-") {
		t.Fatalf("NewEntry ID should start with 'evt-', got %q", entry.ID)
	}
	if entry.Topic != "my.topic" {
		t.Fatalf("want topic 'my.topic', got %q", entry.Topic)
	}
	if entry.EventType != "my.topic" {
		t.Fatalf("want EventType 'my.topic', got %q", entry.EventType)
	}
	if string(entry.Payload) != `{"k":"v"}` {
		t.Fatalf("payload mismatch: %q", entry.Payload)
	}
	if entry.CreatedAt.IsZero() {
		t.Fatal("CreatedAt must not be zero")
	}
}

func TestNewEntry_UniqueIDs(t *testing.T) {
	e1 := NewEntry("t", []byte(`{}`))
	e2 := NewEntry("t", []byte(`{}`))
	if e1.ID == e2.ID {
		t.Fatal("NewEntry should generate unique IDs")
	}
}

func TestNewEntryWithMetadata(t *testing.T) {
	md := map[string]string{"trace_id": "abc"}
	entry := NewEntryWithMetadata("t", []byte(`{}`), md)

	if entry.Metadata == nil {
		t.Fatal("Metadata must not be nil")
	}
	if entry.Metadata["trace_id"] != "abc" {
		t.Fatalf("want trace_id 'abc', got %q", entry.Metadata["trace_id"])
	}
}

func TestMockReceipt_Commit(t *testing.T) {
	r := NewMockReceipt()

	if r.Committed() {
		t.Fatal("should not be committed initially")
	}
	if r.Released() {
		t.Fatal("should not be released initially")
	}

	if err := r.Commit(context.Background()); err != nil {
		t.Fatalf("Commit error: %v", err)
	}

	if !r.Committed() {
		t.Fatal("should be committed after Commit()")
	}
	if r.Released() {
		t.Fatal("should not be released after Commit()")
	}
}

func TestMockReceipt_Release(t *testing.T) {
	r := NewMockReceipt()

	if err := r.Release(context.Background()); err != nil {
		t.Fatalf("Release error: %v", err)
	}

	if !r.Released() {
		t.Fatal("should be released after Release()")
	}
	if r.Committed() {
		t.Fatal("should not be committed after Release()")
	}
}

func TestMockReceipt_WithErrors(t *testing.T) {
	commitErr := context.DeadlineExceeded
	releaseErr := context.Canceled

	r := NewMockReceiptWithErrors(commitErr, releaseErr)

	if err := r.Commit(context.Background()); err != commitErr {
		t.Fatalf("want commitErr, got %v", err)
	}
	if err := r.Release(context.Background()); err != releaseErr {
		t.Fatalf("want releaseErr, got %v", err)
	}

	// Even with errors, the state should be recorded.
	if !r.Committed() {
		t.Fatal("should be committed even when Commit returns error")
	}
	if !r.Released() {
		t.Fatal("should be released even when Release returns error")
	}
}

func TestFeatures_SetDefaults(t *testing.T) {
	f := Features{}
	f.setDefaults()

	if f.MessageCount == 0 {
		t.Fatal("MessageCount should have a non-zero default")
	}
	if f.MessageCount != 100 {
		t.Fatalf("want default MessageCount 100, got %d", f.MessageCount)
	}
}

func TestFeatures_SetDefaults_PreservesExplicit(t *testing.T) {
	f := Features{MessageCount: 42}
	f.setDefaults()

	if f.MessageCount != 42 {
		t.Fatalf("explicit MessageCount should be preserved, got %d", f.MessageCount)
	}
}

// ---------------------------------------------------------------------------
// waitForSubscription tests
// ---------------------------------------------------------------------------

// fakeInitializerSub implements both Subscriber and SubscriberInitializer.
type fakeInitializerSub struct {
	fakePubSub
	initCalled bool
	initTopic  string
	initGroup  string
}

func (f *fakeInitializerSub) InitializeSubscription(_ context.Context, topic, group string) error {
	f.initCalled = true
	f.initTopic = topic
	f.initGroup = group
	return nil
}

func TestWaitForSubscription_UsesInitializerWhenAvailable(t *testing.T) {
	sub := &fakeInitializerSub{}
	ctx := context.Background()

	waitForSubscription(t, ctx, sub, "my.topic", "cg-1")

	if !sub.initCalled {
		t.Fatal("expected InitializeSubscription to be called")
	}
	if sub.initTopic != "my.topic" {
		t.Fatalf("want topic 'my.topic', got %q", sub.initTopic)
	}
	if sub.initGroup != "cg-1" {
		t.Fatalf("want group 'cg-1', got %q", sub.initGroup)
	}
}

func TestWaitForSubscription_FallsBackToSleep(t *testing.T) {
	// fakePubSub does NOT implement SubscriberInitializer.
	sub := &fakePubSub{}
	ctx := context.Background()

	start := time.Now()
	waitForSubscription(t, ctx, sub, "any.topic", "")
	elapsed := time.Since(start)

	// Should have slept at least subscribeInitDelay (50ms).
	if elapsed < 40*time.Millisecond {
		t.Fatalf("expected sleep fallback (~50ms), but returned in %v", elapsed)
	}
}

// ---------------------------------------------------------------------------
// collector tests — uses a minimal in-test fake subscriber (no runtime/ import)
// ---------------------------------------------------------------------------

// fakePubSub is a minimal channel-based Publisher+Subscriber for testing
// the collector helper without importing runtime/eventbus.
type fakePubSub struct {
	mu   sync.Mutex
	subs []chan outbox.Entry
}

func (f *fakePubSub) Publish(_ context.Context, topic string, payload []byte) error {
	entry := outbox.Entry{
		ID:        "fake-" + topic,
		EventType: topic,
		Payload:   payload,
		CreatedAt: time.Now(),
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.subs {
		ch <- entry
	}
	return nil
}

func (f *fakePubSub) Subscribe(ctx context.Context, _ string, handler outbox.EntryHandler, _ string) error {
	ch := make(chan outbox.Entry, 64)
	f.mu.Lock()
	f.subs = append(f.subs, ch)
	f.mu.Unlock()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case entry := <-ch:
			handler(ctx, entry)
		}
	}
}

func (f *fakePubSub) Close() error { return nil }

func TestCollector_CollectsAllEntries(t *testing.T) {
	bus := &fakePubSub{}
	ctx := context.Background()
	topic := "test-collector"

	c := startCollecting(t, ctx, bus, topic, 3)

	// Publish 3 messages after collector is ready.
	for i := range 3 {
		if err := bus.Publish(ctx, topic, testPayload(i)); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	collected := c.waitAndGet(5 * time.Second)
	if len(collected) != 3 {
		t.Fatalf("want 3 entries, got %d", len(collected))
	}
}

func TestCollector_ConcurrentPublish(t *testing.T) {
	bus := &fakePubSub{}
	ctx := context.Background()
	topic := "test-collector-concurrent"
	n := 20

	c := startCollecting(t, ctx, bus, topic, n)

	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(seq int) {
			defer wg.Done()
			_ = bus.Publish(ctx, topic, testPayload(seq))
		}(i)
	}
	wg.Wait()

	collected := c.waitAndGet(5 * time.Second)
	if len(collected) != n {
		t.Fatalf("want %d entries, got %d", n, len(collected))
	}
}
