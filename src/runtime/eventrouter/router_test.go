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

func (s *blockingSubscriber) Subscribe(ctx context.Context, topic string, _ outbox.EntryHandler) error {
	s.mu.Lock()
	s.topics = append(s.topics, topic)
	s.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

func (s *blockingSubscriber) Close() error { return nil }

func (s *blockingSubscriber) Topics() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]string, len(s.topics))
	copy(cp, s.topics)
	return cp
}

// failingSubscriber returns an error immediately, simulating setup failure.
type failingSubscriber struct {
	err error
}

func (s *failingSubscriber) Subscribe(_ context.Context, _ string, _ outbox.EntryHandler) error {
	return s.err
}

func (s *failingSubscriber) Close() error { return nil }

// delayedFailSubscriber blocks briefly then returns an error (simulates
// runtime failure after startup).
type delayedFailSubscriber struct {
	delay time.Duration
	err   error
}

func (s *delayedFailSubscriber) Subscribe(ctx context.Context, _ string, _ outbox.EntryHandler) error {
	select {
	case <-time.After(s.delay):
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *delayedFailSubscriber) Close() error { return nil }

// --- Tests ---

func TestRouter_AddHandler_RegistersTopics(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(sub)

	r.AddHandler("topic.a", noopHandler)
	r.AddHandler("topic.b", noopHandler)

	assert.Equal(t, 2, r.HandlerCount())
}

func TestRouter_Run_StartsAllSubscriptions(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(sub, WithStartupTimeout(200*time.Millisecond))

	r.AddHandler("topic.a", noopHandler)
	r.AddHandler("topic.b", noopHandler)
	r.AddHandler("topic.c", noopHandler)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Wait for Running signal.
	select {
	case <-r.Running():
	case <-time.After(2 * time.Second):
		t.Fatal("Router did not become ready")
	}

	// Verify all 3 topics subscribed.
	topics := sub.Topics()
	assert.Len(t, topics, 3)
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

func TestRouter_Run_SetupError_ReturnsImmediately(t *testing.T) {
	setupErr := errcode.New(errcode.ErrBusClosed, "bus closed")
	sub := &failingSubscriber{err: setupErr}
	r := New(sub, WithStartupTimeout(500*time.Millisecond))

	r.AddHandler("topic.fail", noopHandler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := r.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "topic.fail")

	// Running() should NOT be closed on setup failure.
	select {
	case <-r.Running():
		t.Fatal("Running() should not be closed on setup failure")
	default:
		// expected
	}
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
	r := New(sub, WithStartupTimeout(200*time.Millisecond))

	r.AddHandler("topic.a", noopHandler)

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

	r := New(bus, WithStartupTimeout(200*time.Millisecond))
	r.AddHandler("test.topic", handler)

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

	r := New(bus, WithStartupTimeout(200*time.Millisecond))
	r.AddHandler("topic.a", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		countA.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})
	r.AddHandler("topic.b", func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		countB.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

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
	r := New(bus, WithStartupTimeout(200*time.Millisecond))

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
	// Subscribe blocks for 300ms (past startup timeout), then fails.
	sub := &delayedFailSubscriber{
		delay: 300 * time.Millisecond,
		err:   errors.New("connection lost"),
	}
	r := New(sub, WithStartupTimeout(100*time.Millisecond))
	r.AddHandler("topic.a", noopHandler)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := r.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection lost")
}

func TestRouter_Run_DoubleRun_ReturnsError(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(sub, WithStartupTimeout(100*time.Millisecond))
	r.AddHandler("topic.a", noopHandler)

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
	r := New(sub, WithStartupTimeout(100*time.Millisecond))
	r.AddHandler("topic.stuck", noopHandler)

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
		r.AddHandler("", noopHandler)
	})
}

func TestRouter_AddHandler_PanicsOnNilHandler(t *testing.T) {
	r := New(&blockingSubscriber{})
	assert.Panics(t, func() {
		r.AddHandler("topic", nil)
	})
}

func TestRouter_Run_PanicInSubscriber_CapturedAsError(t *testing.T) {
	// A subscriber whose Subscribe panics.
	panickySub := &panickingSubscriber{}
	r := New(panickySub, WithStartupTimeout(500*time.Millisecond))
	r.AddHandler("topic.panic", noopHandler)

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

func (s *stuckSubscriber) Subscribe(_ context.Context, _ string, _ outbox.EntryHandler) error {
	<-s.block // ignores ctx — simulates unresponsive subscriber
	return nil
}
func (s *stuckSubscriber) Close() error { return nil }

// panickingSubscriber panics on Subscribe.
type panickingSubscriber struct{}

func (s *panickingSubscriber) Subscribe(_ context.Context, _ string, _ outbox.EntryHandler) error {
	panic("boom")
}
func (s *panickingSubscriber) Close() error { return nil }

// --- Helpers ---

var noopHandler = func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
	return outbox.HandleResult{Disposition: outbox.DispositionAck}
}

// mockCell implements cell.EventRegistrar for testing.
type mockCell struct {
	handler outbox.EntryHandler
}

func (m *mockCell) RegisterSubscriptions(r cell.EventRouter) error {
	r.AddHandler("mock.topic", m.handler)
	return nil
}

// newTestEventBus creates an InMemoryEventBus for testing, registered for cleanup.
func newTestEventBus(t *testing.T) *testBus {
	t.Helper()
	b := &testBus{
		subs:    make(map[string][]*testSub),
		bufSize: 256,
	}
	t.Cleanup(func() { _ = b.Close() })
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

func (b *testBus) Subscribe(ctx context.Context, topic string, handler outbox.EntryHandler) error {
	subCtx, cancel := context.WithCancel(ctx)
	s := &testSub{ch: make(chan outbox.Entry, b.bufSize), cancel: cancel}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		cancel()
		return errcode.New(errcode.ErrBusClosed, "closed")
	}
	b.subs[topic] = append(b.subs[topic], s)
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

func (b *testBus) Close() error {
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
