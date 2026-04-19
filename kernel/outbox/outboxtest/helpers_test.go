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
	if testing.Short() {
		t.Skip("setDefaults uses reduced values in -short mode; full defaults tested in normal mode only")
	}
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

// recordingSubscriber records Setup/Ready calls to verify waitForSubscription
// uses the new Subscriber interface. Embeds immediateReadySub so Ready() returns
// a pre-closed channel and waitForSubscription exits immediately.
type recordingSubscriber struct {
	immediateReadySub
	mu          sync.Mutex
	setupCalled bool
	setupSub    outbox.Subscription
}

func (r *recordingSubscriber) Setup(_ context.Context, sub outbox.Subscription) error {
	r.mu.Lock()
	r.setupCalled = true
	r.setupSub = sub
	r.mu.Unlock()
	return nil
}

func TestWaitForSubscription_CallsSetupWithSubscription(t *testing.T) {
	// waitForSubscription must call Setup with the correct Subscription.
	sub := &recordingSubscriber{}
	ctx := context.Background()

	waitForSubscription(t, ctx, sub, "my.topic", "cg-1")

	sub.mu.Lock()
	defer sub.mu.Unlock()
	if !sub.setupCalled {
		t.Fatal("expected Setup to be called")
	}
	if sub.setupSub.Topic != "my.topic" {
		t.Fatalf("want topic 'my.topic', got %q", sub.setupSub.Topic)
	}
	if sub.setupSub.ConsumerGroup != "cg-1" {
		t.Fatalf("want group 'cg-1', got %q", sub.setupSub.ConsumerGroup)
	}
}

// immediateReadySub is a subscriber whose Ready channel is always pre-closed.
// Used to verify that waitForSubscription returns immediately when Ready is done.
type immediateReadySub struct {
	fakePubSub
}

func (s *immediateReadySub) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func TestWaitForSubscription_WaitsForReadyChannel(t *testing.T) {
	// When Ready returns a pre-closed channel, waitForSubscription returns fast.
	sub := &immediateReadySub{}
	ctx := context.Background()

	start := time.Now()
	waitForSubscription(t, ctx, sub, "any.topic", "")
	elapsed := time.Since(start)

	// Should return quickly since Ready() is already closed (pre-closed channel).
	if elapsed >= subscribeInitDelay {
		t.Fatalf("expected fast return (pre-closed Ready channel), but took %v", elapsed)
	}
}

func TestWaitForSubscription_MiddlewareWrappedSubscriber_UsesSetup(t *testing.T) {
	// SubscriberWithMiddleware uses Subscriber.Setup/Ready via Inner.
	// When Inner.Ready returns a pre-closed channel, the fast path is taken.
	inner := &immediateReadySub{} // Ready() returns pre-closed channel
	wrapped := &outbox.SubscriberWithMiddleware{
		Inner:      inner,
		Middleware: nil,
	}
	ctx := context.Background()

	start := time.Now()
	waitForSubscription(t, ctx, wrapped, "test.topic", "")
	elapsed := time.Since(start)

	// Must NOT fall back to sleep -- Setup returns nil immediately and
	// Inner.Ready returns a pre-closed channel.
	if elapsed >= subscribeInitDelay {
		t.Fatalf("middleware-wrapped subscriber must not sleep (Setup+Ready fast path), but took %v", elapsed)
	}
}

// ---------------------------------------------------------------------------
// collector tests — uses a minimal in-test fake subscriber (no runtime/ import)
// ---------------------------------------------------------------------------

// fakePubSub is a minimal channel-based Publisher+Subscriber for testing
// the collector helper without importing runtime/eventbus.
type fakePubSub struct {
	mu      sync.Mutex
	subs    []chan outbox.Entry
	readyCh chan struct{} // closed on first Subscribe call
	once    sync.Once
}

func (f *fakePubSub) readyChannel() chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readyCh == nil {
		f.readyCh = make(chan struct{})
	}
	return f.readyCh
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

func (f *fakePubSub) Setup(_ context.Context, _ outbox.Subscription) error { return nil }

// Ready returns a channel that closes once the first Subscribe call has
// registered its handler. This prevents publish-before-subscribe races in
// the test harness when waitForSubscription is used.
func (f *fakePubSub) Ready(_ outbox.Subscription) <-chan struct{} {
	return f.readyChannel()
}

func (f *fakePubSub) Subscribe(ctx context.Context, sub outbox.Subscription, handler outbox.EntryHandler) error {
	ch := make(chan outbox.Entry, 64)
	f.mu.Lock()
	f.subs = append(f.subs, ch)
	rch := f.readyCh
	if rch == nil {
		f.readyCh = make(chan struct{})
		rch = f.readyCh
	}
	f.mu.Unlock()

	// Signal readiness after registering the subscription.
	f.once.Do(func() { close(rch) })

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case entry := <-ch:
			handler(ctx, entry)
		}
	}
}

func (f *fakePubSub) Close(_ context.Context) error { return nil }

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

// ---------------------------------------------------------------------------
// pubSubHarness.checkNoMoreDeliveries tests
//
// The harness exposes two APIs for the "no further delivery" assertion:
//   - checkNoMoreDeliveries — returns (error) — unit-testable core
//   - assertNoMoreDeliveries — calls t.Fatalf on error — what conformance tests use
// testing.TB.private() forbids fakes, so these tests exercise the checkXxx
// variant directly.
// ---------------------------------------------------------------------------

// harnessConstructor returns a PubSubConstructor backed by the supplied
// in-test fakePubSub, letting unit tests exercise pubSubHarness methods
// without depending on runtime/eventbus or a real broker adapter.
func harnessConstructor(bus *fakePubSub) PubSubConstructor {
	return func(_ *testing.T) (outbox.Publisher, outbox.Subscriber) {
		return bus, bus
	}
}

// TestHarness_CheckNoMoreDeliveries_NoLeakReturnsNil verifies that when no
// redelivery occurs within the window, the check returns nil.
func TestHarness_CheckNoMoreDeliveries_NoLeakReturnsNil(t *testing.T) {
	bus := &fakePubSub{}
	h := newHarness(t, harnessConstructor(bus))

	h.subscribe(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		h.signalDone()
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})
	h.publishAndWait([]byte(`{"ok":1}`))

	if err := h.checkNoMoreDeliveries(1, 50*time.Millisecond); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h.teardown()
}

// TestHarness_CheckNoMoreDeliveries_DetectsRedelivery ensures that when a
// second delivery arrives during the window, the check returns a non-nil
// error fast (fail-fast via select, not full-window wait).
func TestHarness_CheckNoMoreDeliveries_DetectsRedelivery(t *testing.T) {
	bus := &fakePubSub{}
	h := newHarness(t, harnessConstructor(bus))

	h.subscribe(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})
	// Publish twice: first is expected prior, second simulates redelivery.
	if err := bus.Publish(context.Background(), h.Topic, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("publish 1: %v", err)
	}
	if err := bus.Publish(context.Background(), h.Topic, []byte(`{"a":2}`)); err != nil {
		t.Fatalf("publish 2: %v", err)
	}
	waitForCount(t, func() int { return len(h.deliveryEvents) }, 2, time.Second)

	start := time.Now()
	err := h.checkNoMoreDeliveries(1, 500*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected non-nil error for unexpected redelivery")
	}
	if !strings.Contains(err.Error(), "unexpected delivery") {
		t.Fatalf("error should mention 'unexpected delivery', got: %v", err)
	}
	// Fail-fast upper bound: select should return in ms on unexpected delivery.
	// 300ms accommodates CI goroutine-scheduling jitter while remaining far
	// below the full 500ms window (which would indicate the select-fast path
	// didn't trigger).
	if elapsed > 300*time.Millisecond {
		t.Fatalf("expected fail-fast (<300ms), got %v", elapsed)
	}
	h.teardown()
}

// TestHarness_CheckNoMoreDeliveries_DrainTimeout verifies that if fewer
// prior deliveries arrive than expected, the drain phase times out.
func TestHarness_CheckNoMoreDeliveries_DrainTimeout(t *testing.T) {
	bus := &fakePubSub{}
	h := newHarness(t, harnessConstructor(bus))

	h.subscribe(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})
	// Shorten drain timeout — no publish, drain should fail quickly.
	h.drainTimeout = 50 * time.Millisecond

	err := h.checkNoMoreDeliveries(1, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected drain timeout error")
	}
	if !strings.Contains(err.Error(), "expected 1 prior deliveries") {
		t.Fatalf("error should mention prior-deliveries shortfall, got: %v", err)
	}
	h.teardown()
}

// TestHarness_CheckNoMoreDeliveries_DrainsThenWaits verifies ordering:
// first N deliveries are drained without triggering the "unexpected" path,
// even when N>1 and they arrive in quick succession.
func TestHarness_CheckNoMoreDeliveries_DrainsThenWaits(t *testing.T) {
	bus := &fakePubSub{}
	h := newHarness(t, harnessConstructor(bus))

	h.subscribe(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})
	// Simulate 3 expected deliveries (competing-consumers scenario).
	for range 3 {
		if err := bus.Publish(context.Background(), h.Topic, []byte(`{}`)); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	waitForCount(t, func() int { return len(h.deliveryEvents) }, 3, time.Second)

	if err := h.checkNoMoreDeliveries(3, 50*time.Millisecond); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	h.teardown()
}

func waitForCount(t *testing.T, get func() int, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if get() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waitForCount: want %d, got %d after %v", want, get(), timeout)
}

// ---------------------------------------------------------------------------
// Conformance-function coverage against fakePubSub
//
// The conformance suite is designed to be driven by real adapters (RabbitMQ,
// in-memory eventbus) via TestPubSub. Unit tests in this package otherwise
// do not execute the individual conformance functions, so the helper-path
// logic (harness wiring, fail-fast assertions) lacks in-package coverage.
//
// These tests run the Disposition batch (plus PermanentErrorCausesReject)
// against fakePubSub, which does not redeliver on Reject/Requeue/Ack — the
// "no redelivery" invariant is therefore trivially true, and the tests
// exercise the helper wiring without requiring a real broker.
// ---------------------------------------------------------------------------

func TestConformance_DispositionAck_OnFakeBus(t *testing.T) {
	testDispositionAck(t, Features{}, harnessConstructor(&fakePubSub{}))
}

func TestConformance_DispositionReject_OnFakeBus(t *testing.T) {
	testDispositionReject(t, Features{SupportsReject: true}, harnessConstructor(&fakePubSub{}))
}

func TestConformance_PermanentErrorCausesReject_OnFakeBus(t *testing.T) {
	testPermanentErrorCausesReject(t, Features{SupportsReject: true}, harnessConstructor(&fakePubSub{}))
}

// TestConformance_DispositionReject_SkipsWhenUnsupported verifies the skip
// gate is honored when the adapter under test disables Reject support.
func TestConformance_DispositionReject_SkipsWhenUnsupported(t *testing.T) {
	// Use a sub-test so Skip does not terminate the parent.
	ran := t.Run("inner", func(inner *testing.T) {
		testDispositionReject(inner, Features{SupportsReject: false}, harnessConstructor(&fakePubSub{}))
	})
	if !ran {
		t.Fatal("sub-test should pass (Skip is not a failure)")
	}
}

// TestConformance_PermanentErrorCausesReject_SkipsWhenUnsupported mirrors the
// above for the PermanentError scenario.
func TestConformance_PermanentErrorCausesReject_SkipsWhenUnsupported(t *testing.T) {
	ran := t.Run("inner", func(inner *testing.T) {
		testPermanentErrorCausesReject(inner, Features{SupportsReject: false}, harnessConstructor(&fakePubSub{}))
	})
	if !ran {
		t.Fatal("sub-test should pass (Skip is not a failure)")
	}
}
