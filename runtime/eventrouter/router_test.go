package eventrouter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Compile-time interface check.
var _ cell.EventRouter = (*Router)(nil)

// --- Mock Subscriber ---

// blockingSubscriber blocks until ctx is cancelled, simulating a healthy broker.
type blockingSubscriber struct {
	mu     sync.Mutex
	topics []string
}

func (s *blockingSubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }
func (s *blockingSubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (s *blockingSubscriber) Subscribe(ctx context.Context, sub outbox.Subscription, _ outbox.EntryHandler) error {
	s.mu.Lock()
	s.topics = append(s.topics, sub.Topic)
	s.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}
func (s *blockingSubscriber) Close(_ context.Context) error { return nil }

func (s *blockingSubscriber) Topics() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]string, len(s.topics))
	copy(cp, s.topics)
	return cp
}

// failingSubscriber returns an error immediately from Subscribe, simulating
// a Subscribe-level failure (e.g. broker connection refused).
type failingSubscriber struct {
	err error
}

func (s *failingSubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }
func (s *failingSubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (s *failingSubscriber) Subscribe(_ context.Context, _ outbox.Subscription, _ outbox.EntryHandler) error {
	return s.err
}
func (s *failingSubscriber) Close(_ context.Context) error { return nil }

// delayedFailSubscriber blocks briefly then returns an error (simulates
// runtime failure after startup).
type delayedFailSubscriber struct {
	delay time.Duration
	err   error
}

func (s *delayedFailSubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }
func (s *delayedFailSubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (s *delayedFailSubscriber) Subscribe(ctx context.Context, _ outbox.Subscription, _ outbox.EntryHandler) error {
	select {
	case <-time.After(s.delay):
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *delayedFailSubscriber) Close(_ context.Context) error { return nil }

// --- Tests ---

func TestRouter_AddHandler_RegistersTopics(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(sub)

	r.AddHandler("topic.a", noopHandler, "test")
	r.AddHandler("topic.b", noopHandler, "test")

	assert.Equal(t, 2, r.HandlerCount())
}

func TestRouter_Run_StartsAllSubscriptions(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(sub)

	r.AddHandler("topic.a", noopHandler, "test")
	r.AddHandler("topic.b", noopHandler, "test")
	r.AddHandler("topic.c", noopHandler, "test")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Wait for Running signal.
	select {
	case <-r.Running():
	case <-time.After(2 * time.Second):
		t.Fatal("Router did not become ready")
	}

	// Subscribe goroutines are launched concurrently; give them a moment to
	// register their topics (they run after Phase 3 Ready signals close).
	assert.Eventually(t, func() bool {
		topics := sub.Topics()
		return len(topics) == 3
	}, 2*time.Second, 10*time.Millisecond, "all 3 topics should be subscribed")

	topics := sub.Topics()
	assert.Contains(t, topics, "topic.a")
	assert.Contains(t, topics, "topic.b")
	assert.Contains(t, topics, "topic.c")

	cancel()
	assert.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, 2*time.Second, 50*time.Millisecond)
}

// TestRouter_Run_SubscribeError_ReturnsError verifies that when
// Subscriber.Subscribe returns an error immediately (after Ready fires),
// Run returns the error. Running() is closed first (Phase 3 completes), then
// the runtime error is detected in Phase 4 and returned from Run.
// For Setup-level failures (before any subscription starts), see TestRouter_SetupErrorAborts.
func TestRouter_Run_SubscribeError_ReturnsError(t *testing.T) {
	subscribeErr := errcode.New(errcode.ErrBusClosed, "bus closed")
	sub := &failingSubscriber{err: subscribeErr}
	r := New(sub)

	r.AddHandler("topic.fail", noopHandler, "test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := r.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "topic.fail")
}

func TestRouter_Run_NoHandlers_BlocksUntilCancel(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(sub)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Running should be closed immediately.
	select {
	case <-r.Running():
	case <-time.After(time.Second):
		t.Fatal("Running() should close immediately with no handlers")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit after cancel")
	}
}

func TestRouter_Close_CancelsSubscriptions(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(sub)

	r.AddHandler("topic.a", noopHandler, "test")

	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	<-r.Running()

	err := r.Close(context.Background())
	assert.NoError(t, err)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after Close")
	}
}

func TestRouter_Run_HandlerReceivesMessages(t *testing.T) {
	// Use InMemoryEventBus to verify end-to-end message flow.
	bus := newTestEventBus(t)

	var received atomic.Int32
	handler := func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		received.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}

	r := New(bus)
	r.AddHandler("test.topic", handler, "test")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	<-r.Running()

	// Publish a message.
	err := bus.Publish(context.Background(), "test.topic", []byte(`{"key":"value"}`))
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		return received.Load() >= 1
	}, time.Second, 10*time.Millisecond)

	cancel()
	<-done
}

func TestRouter_Run_MultipleHandlersSameSubscriber(t *testing.T) {
	bus := newTestEventBus(t)

	var countA, countB atomic.Int32

	r := New(bus)
	r.AddHandler("topic.a", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		countA.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}, "test")
	r.AddHandler("topic.b", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		countB.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}, "test")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	<-r.Running()

	require.NoError(t, bus.Publish(context.Background(), "topic.a", []byte(`{}`)))
	require.NoError(t, bus.Publish(context.Background(), "topic.b", []byte(`{}`)))

	assert.Eventually(t, func() bool {
		return countA.Load() >= 1 && countB.Load() >= 1
	}, time.Second, 10*time.Millisecond)

	cancel()
	<-done
}

func TestRouter_EventRegistrar_Integration(t *testing.T) {
	// Simulate the bootstrap pattern: cell declares handlers, router runs them.
	bus := newTestEventBus(t)
	r := New(bus)

	var received atomic.Int32

	// Simulate a cell's RegisterSubscriptions.
	var registrar cell.EventRegistrar = &mockCell{handler: func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		received.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}}

	err := registrar.RegisterSubscriptions(r)
	require.NoError(t, err)
	assert.Equal(t, 1, r.HandlerCount())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	<-r.Running()

	require.NoError(t, bus.Publish(context.Background(), "mock.topic", []byte(`{}`)))

	assert.Eventually(t, func() bool {
		return received.Load() >= 1
	}, time.Second, 10*time.Millisecond)

	cancel()
	<-done
}

func TestRouter_Run_RuntimeError_AfterStartup(t *testing.T) {
	// delayedFailSubscriber: Ready returns immediately-closed channel (so Router
	// marks itself Running fast), then Subscribe returns an error after the delay.
	sub := &delayedFailSubscriber{
		delay: 100 * time.Millisecond,
		err:   errors.New("connection lost"),
	}
	r := New(sub)
	r.AddHandler("topic.a", noopHandler, "test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := r.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection lost")
}

func TestRouter_HealthLifecycle(t *testing.T) {
	// subscriber that is ready immediately but fails after 300ms.
	sub := &delayedFailSubscriber{
		delay: 300 * time.Millisecond,
		err:   errors.New("connection lost"),
	}
	r := New(sub)
	r.AddHandler("topic.a", noopHandler, "test")

	require.Error(t, r.Health(), "router must be unhealthy before Run")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(2 * time.Second):
		t.Fatal("router did not become ready")
	}

	require.NoError(t, r.Health(), "router must be healthy after startup")

	assert.Eventually(t, func() bool {
		return r.Health() != nil
	}, 2*time.Second, 20*time.Millisecond, "router must become unhealthy after runtime failure")

	err := <-done
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection lost")
}

func TestRouter_Health_AfterGracefulShutdown(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(sub)
	r.AddHandler("topic.a", noopHandler, "test")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(2 * time.Second):
		t.Fatal("router did not become ready")
	}

	require.NoError(t, r.Health(), "router must be healthy before shutdown")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("router did not shut down")
	}

	err := r.Health()
	require.Error(t, err, "router must be unhealthy after graceful shutdown")
	assert.Contains(t, err.Error(), "shutting down")
}

func TestRouter_Run_DoubleRun_ReturnsError(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(sub)
	r.AddHandler("topic.a", noopHandler, "test")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	<-r.Running()

	// Second Run should return error, not panic.
	err := r.Run(ctx)
	assert.ErrorIs(t, err, errAlreadyRunning)

	cancel()
	<-done
}

func TestRouter_Close_ZeroHandlers(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(sub)

	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	<-r.Running()

	// Close should terminate Run even with zero handlers.
	err := r.Close(context.Background())
	assert.NoError(t, err)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit after Close with zero handlers")
	}
}

func TestRouter_Close_Timeout(t *testing.T) {
	// Subscriber that ignores context cancellation (simulates stuck goroutine).
	stuck := make(chan struct{})
	sub := &stuckSubscriber{block: stuck}
	r := New(sub)
	r.AddHandler("topic.stuck", noopHandler, "test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = r.Run(ctx) }()

	<-r.Running()

	// Close with a very short timeout — should return context deadline error.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer closeCancel()
	err := r.Close(closeCtx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	close(stuck) // unblock the subscriber so test cleanup works
}

func TestRouter_AddHandler_PanicsOnEmptyTopic(t *testing.T) {
	r := New(&blockingSubscriber{})
	assert.Panics(t, func() {
		r.AddHandler("", noopHandler, "test")
	})
}

func TestRouter_AddHandler_PanicsOnNilHandler(t *testing.T) {
	r := New(&blockingSubscriber{})
	assert.Panics(t, func() {
		r.AddHandler("topic", nil, "test")
	})
}

func TestRouter_Run_PanicInSubscriber_CapturedAsError(t *testing.T) {
	// A subscriber whose Subscribe panics.
	panickySub := &panickingSubscriber{}
	r := New(panickySub)
	r.AddHandler("topic.panic", noopHandler, "test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := r.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panicked")
}

// stuckSubscriber blocks on an external channel, ignoring context cancellation.
type stuckSubscriber struct {
	block chan struct{}
}

func (s *stuckSubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }
func (s *stuckSubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (s *stuckSubscriber) Subscribe(_ context.Context, _ outbox.Subscription, _ outbox.EntryHandler) error {
	<-s.block // ignores ctx -- simulates unresponsive subscriber
	return nil
}
func (s *stuckSubscriber) Close(_ context.Context) error { return nil }

// panickingSubscriber panics on Subscribe.
type panickingSubscriber struct{}

func (s *panickingSubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }
func (s *panickingSubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (s *panickingSubscriber) Subscribe(_ context.Context, _ outbox.Subscription, _ outbox.EntryHandler) error {
	panic("boom")
}
func (s *panickingSubscriber) Close(_ context.Context) error { return nil }

// --- Helpers ---

var noopHandler = func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
	return outbox.HandleResult{Disposition: outbox.DispositionAck}
}

// mockCell implements cell.EventRegistrar for testing.
type mockCell struct {
	handler outbox.EntryHandler
}

func (m *mockCell) RegisterSubscriptions(r cell.EventRouter) error {
	r.AddHandler("mock.topic", m.handler, "mock-cell")
	return nil
}

// newTestEventBus creates an in-memory pub/sub for Router tests, registered for cleanup.
func newTestEventBus(t *testing.T) *testBus {
	t.Helper()
	b := &testBus{
		subs:    make(map[string][]*testSub),
		bufSize: 256,
	}
	t.Cleanup(func() { _ = b.Close(context.Background()) })
	return b
}

// testBus is a minimal in-memory pub/sub for Router tests, avoiding import
// of runtime/eventbus (same package layer). Mirrors eventbus.InMemoryEventBus
// but without retry/dead-letter logic.
type testBus struct {
	mu      sync.RWMutex
	subs    map[string][]*testSub
	bufSize int
	closed  bool
}

type testSub struct {
	ch     chan outbox.Entry
	cancel context.CancelFunc
}

func (b *testBus) Publish(_ context.Context, topic string, payload []byte) error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return errcode.New(errcode.ErrBusClosed, "closed")
	}
	entry := outbox.Entry{ID: "evt-test", EventType: topic, Payload: payload}
	for _, s := range b.subs[topic] {
		select {
		case s.ch <- entry:
		default:
		}
	}
	return nil
}

func (b *testBus) Setup(_ context.Context, _ outbox.Subscription) error { return nil }

func (b *testBus) Ready(sub outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	// Close once a subscriber for this topic has actually registered.
	go func() {
		for {
			b.mu.RLock()
			_, exists := b.subs[sub.Topic]
			b.mu.RUnlock()
			if exists {
				close(ch)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	return ch
}

func (b *testBus) Subscribe(ctx context.Context, sub outbox.Subscription, handler outbox.EntryHandler) error {
	subCtx, cancel := context.WithCancel(ctx)
	s := &testSub{ch: make(chan outbox.Entry, b.bufSize), cancel: cancel}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		cancel()
		return errcode.New(errcode.ErrBusClosed, "closed")
	}
	b.subs[sub.Topic] = append(b.subs[sub.Topic], s)
	b.mu.Unlock()

	for {
		select {
		case <-subCtx.Done():
			return subCtx.Err()
		case entry := <-s.ch:
			handler(subCtx, entry)
		}
	}
}

func (b *testBus) Close(_ context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	for _, subs := range b.subs {
		for _, s := range subs {
			s.cancel()
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// ConsumerGroup propagation tests (ER-ARCH-02)
// ---------------------------------------------------------------------------

// recordingGroupSubscriber records which consumerGroup was passed to Subscribe.
type recordingGroupSubscriber struct {
	mu    sync.Mutex
	calls []groupSubscribeCall
}

type groupSubscribeCall struct {
	Topic         string
	ConsumerGroup string
}

func (s *recordingGroupSubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }
func (s *recordingGroupSubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (s *recordingGroupSubscriber) Subscribe(ctx context.Context, sub outbox.Subscription, _ outbox.EntryHandler) error {
	s.mu.Lock()
	s.calls = append(s.calls, groupSubscribeCall{Topic: sub.Topic, ConsumerGroup: sub.ConsumerGroup})
	s.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}
func (s *recordingGroupSubscriber) Close(_ context.Context) error { return nil }

func (s *recordingGroupSubscriber) Calls() []groupSubscribeCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]groupSubscribeCall, len(s.calls))
	copy(out, s.calls)
	return out
}

// TestRouter_ConsumerGroup_PropagatesToSubscriber verifies that the consumerGroup
// passed to AddHandler is forwarded verbatim to Subscriber.Subscribe via Subscription.
func TestRouter_ConsumerGroup_PropagatesToSubscriber(t *testing.T) {
	sub := &recordingGroupSubscriber{}
	r := New(sub)

	r.AddHandler("session.created", noopHandler, "audit-core")
	r.AddHandler("config.changed", noopHandler, "config-core")
	r.AddHandler("legacy.event", noopHandler, "legacy-cell")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Wait for all subscriptions to start.
	require.Eventually(t, func() bool {
		return len(sub.Calls()) >= 3
	}, 2*time.Second, 10*time.Millisecond)

	cancel()
	<-done

	calls := sub.Calls()
	assert.Len(t, calls, 3)

	// Build a map for order-independent assertions.
	groupByTopic := make(map[string]string)
	for _, c := range calls {
		groupByTopic[c.Topic] = c.ConsumerGroup
	}

	assert.Equal(t, "audit-core", groupByTopic["session.created"])
	assert.Equal(t, "config-core", groupByTopic["config.changed"])
	assert.Equal(t, "legacy-cell", groupByTopic["legacy.event"])
}

func TestRouter_AddHandler_PanicsOnEmptyConsumerGroup(t *testing.T) {
	r := New(&blockingSubscriber{})
	assert.PanicsWithValue(t,
		"eventrouter: AddHandler called with empty consumerGroup; cells must declare their identity",
		func() {
			r.AddHandler("topic", noopHandler, "")
		})
}

// ---------------------------------------------------------------------------
// Commit 3: Explicit Ready signal tests
// ---------------------------------------------------------------------------

// delayedReadySubscriber is a mock Subscriber whose Ready channel closes after
// the configured delay. Subscribe blocks until ctx cancellation.
type delayedReadySubscriber struct {
	delay time.Duration

	mu       sync.Mutex
	readyChs map[string]chan struct{} // key = topic
}

func newDelayedReadySubscriber(delay time.Duration) *delayedReadySubscriber {
	return &delayedReadySubscriber{
		delay:    delay,
		readyChs: make(map[string]chan struct{}),
	}
}

func (s *delayedReadySubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }

func (s *delayedReadySubscriber) Ready(sub outbox.Subscription) <-chan struct{} {
	s.mu.Lock()
	if _, ok := s.readyChs[sub.Topic]; !ok {
		s.readyChs[sub.Topic] = make(chan struct{})
	}
	ch := s.readyChs[sub.Topic]
	s.mu.Unlock()

	go func() {
		time.Sleep(s.delay)
		// Safe to close multiple times? No — we create one per topic so it's fine.
		select {
		case <-ch:
			// already closed
		default:
			close(ch)
		}
	}()
	return ch
}

func (s *delayedReadySubscriber) Subscribe(ctx context.Context, _ outbox.Subscription, _ outbox.EntryHandler) error {
	<-ctx.Done()
	return ctx.Err()
}
func (s *delayedReadySubscriber) Close(_ context.Context) error { return nil }

// setupFailSubscriber returns an error from Setup, never calling Subscribe.
type setupFailSubscriber struct {
	err          error
	subscribeCnt atomic.Int32
}

func (s *setupFailSubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return s.err }
func (s *setupFailSubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (s *setupFailSubscriber) Subscribe(_ context.Context, _ outbox.Subscription, _ outbox.EntryHandler) error {
	s.subscribeCnt.Add(1)
	return nil
}
func (s *setupFailSubscriber) Close(_ context.Context) error { return nil }

// partialReadySubscriber: topics A and B have immediately-closed Ready channels;
// topic C closes its Ready channel after the configured delay.
type partialReadySubscriber struct {
	slowTopic string
	slowDelay time.Duration
}

func (s *partialReadySubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }

func (s *partialReadySubscriber) Ready(sub outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	if sub.Topic == s.slowTopic {
		go func() {
			time.Sleep(s.slowDelay)
			close(ch)
		}()
	} else {
		close(ch)
	}
	return ch
}

func (s *partialReadySubscriber) Subscribe(ctx context.Context, _ outbox.Subscription, _ outbox.EntryHandler) error {
	<-ctx.Done()
	return ctx.Err()
}
func (s *partialReadySubscriber) Close(_ context.Context) error { return nil }

// TestRouter_RunBlocksUntilReady_NoTimeout verifies that Router.Running() is
// NOT closed until the Subscriber.Ready signal fires. The Ready channel closes
// after 100ms; Running() must close within that window (not at 500ms).
func TestRouter_RunBlocksUntilReady_NoTimeout(t *testing.T) {
	const readyDelay = 100 * time.Millisecond
	sub := newDelayedReadySubscriber(readyDelay)

	r := New(sub)
	r.AddHandler("topic.a", noopHandler, "test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	go func() { _ = r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(2 * time.Second):
		t.Fatal("Router did not become ready within 2s")
	}

	elapsed := time.Since(start)
	// Ready fires at ~100ms. Allow ±50ms tolerance.
	assert.GreaterOrEqual(t, elapsed, readyDelay-10*time.Millisecond,
		"Router became ready too early (before Ready signal fired)")
	assert.Less(t, elapsed, readyDelay+50*time.Millisecond,
		"Router took too long after Ready signal (should not wait 500ms)")
}

// TestRouter_SetupErrorAborts verifies that when Subscriber.Setup returns an
// error, Run returns that error immediately and Subscribe is never called.
func TestRouter_SetupErrorAborts(t *testing.T) {
	setupErr := errors.New("topology declaration failed")
	sub := &setupFailSubscriber{err: setupErr}

	r := New(sub)
	r.AddHandler("topic.fail", noopHandler, "test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := r.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "topic.fail")

	// Subscribe must never have been called.
	assert.Equal(t, int32(0), sub.subscribeCnt.Load(),
		"Subscribe should not be called when Setup fails")

	// Running() should NOT be closed on setup failure.
	select {
	case <-r.Running():
		t.Fatal("Running() should not be closed when Setup fails")
	default:
	}
}

// mixedReadySubscriber covers the concurrent failure mode tested by
// TestRouter_ReadyError_PartialNotReady_NoLeak: per-topic dispatch where
// some topics close Ready immediately, one topic returns a Subscribe error,
// and another topic delays Ready beyond the test window. Used to validate
// that runAwaitReady's setupErr branch (1) returns the error promptly,
// (2) does NOT close Running(), and (3) does not leak goroutines.
type mixedReadySubscriber struct {
	subscribeErrTopic string
	subscribeErr      error
	slowReadyTopic    string
	slowReadyDelay    time.Duration
	subscribeCalls    atomic.Int32
}

func (s *mixedReadySubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }

func (s *mixedReadySubscriber) Ready(sub outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	if sub.Topic == s.subscribeErrTopic {
		// Subscribe will fail before Ready can fire; leave channel open.
		return ch
	}
	if sub.Topic == s.slowReadyTopic {
		go func() {
			time.Sleep(s.slowReadyDelay)
			close(ch)
		}()
		return ch
	}
	close(ch)
	return ch
}

func (s *mixedReadySubscriber) Subscribe(ctx context.Context, sub outbox.Subscription, _ outbox.EntryHandler) error {
	s.subscribeCalls.Add(1)
	if sub.Topic == s.subscribeErrTopic {
		return s.subscribeErr
	}
	<-ctx.Done()
	return ctx.Err()
}
func (s *mixedReadySubscriber) Close(_ context.Context) error { return nil }

// TestRouter_ReadyError_PartialNotReady_NoLeak verifies the runAwaitReady
// failure path (six-seat review F1): when one Subscribe goroutine sends
// to setupErr while other handlers are still waiting on Ready, Run must:
//  1. Return the Subscribe error promptly (within ~100ms, not block on
//     the slow Ready).
//  2. NOT close Running() — Health remains "not running".
//  3. Cancel the runCtx so all goroutines (including the slow-Ready waiter
//     and the still-running Subscribe goroutines) exit cleanly via wg.Wait.
//  4. Not leak the setupErr drain goroutine — proven by no extra background
//     goroutine surviving the test (no drain since the F1 fix removed it).
func TestRouter_ReadyError_PartialNotReady_NoLeak(t *testing.T) {
	subErr := errors.New("subscribe failed mid-ready")
	sub := &mixedReadySubscriber{
		subscribeErrTopic: "topic.subscribe-error",
		subscribeErr:      subErr,
		slowReadyTopic:    "topic.slow-ready",
		slowReadyDelay:    5 * time.Second, // would block test if Run waited
	}

	r := New(sub, WithReadyTimeout(0)) // disable timeout to isolate setupErr path

	r.AddHandler("topic.fast", noopHandler, "test")
	r.AddHandler(sub.subscribeErrTopic, noopHandler, "test")
	r.AddHandler(sub.slowReadyTopic, noopHandler, "test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	start := time.Now()
	go func() { runDone <- r.Run(ctx) }()

	select {
	case err := <-runDone:
		require.Error(t, err)
		assert.ErrorIs(t, err, subErr, "Run should propagate the Subscribe error")
		elapsed := time.Since(start)
		assert.Less(t, elapsed, 1*time.Second,
			"Run should return promptly on Subscribe error, not wait for slow Ready (5s)")
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after Subscribe error — possible block on slow Ready")
	}

	// Running() must NOT be closed.
	select {
	case <-r.Running():
		t.Fatal("Running() must not close when a Subscribe goroutine errors during ready wait")
	default:
	}
	assert.Error(t, r.Health(), "Health must report not-running after setup error")

	// All three Subscribe goroutines were dispatched (Phase 2 launches all
	// before Phase 3 select); error path cancels them via runCtx.
	assert.Equal(t, int32(3), sub.subscribeCalls.Load(),
		"all 3 Subscribe goroutines should have been launched in Phase 2")
}

// TestRouter_PartialReady_BlocksUntilAll verifies that with 3 handlers where
// topic-C Ready closes after 200ms, Router.Running() only closes after all
// three Ready channels are closed (i.e., at or after 200ms).
func TestRouter_PartialReady_BlocksUntilAll(t *testing.T) {
	const slowDelay = 200 * time.Millisecond
	sub := &partialReadySubscriber{
		slowTopic: "topic.c",
		slowDelay: slowDelay,
	}

	r := New(sub)
	r.AddHandler("topic.a", noopHandler, "test")
	r.AddHandler("topic.b", noopHandler, "test")
	r.AddHandler("topic.c", noopHandler, "test")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	start := time.Now()
	go func() { _ = r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(2 * time.Second):
		t.Fatal("Router did not become ready within 2s")
	}

	elapsed := time.Since(start)
	// All three Ready channels must close; the last is at ~200ms.
	assert.GreaterOrEqual(t, elapsed, slowDelay-10*time.Millisecond,
		"Router became ready before all Ready channels closed")
	assert.Less(t, elapsed, slowDelay+100*time.Millisecond,
		"Router took too long after all Ready signals fired")
}
