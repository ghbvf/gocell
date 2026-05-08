package eventrouter

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// routerReadyDelay is the time a delayed subscriber takes to signal ready; used
// to verify Router.Running() timing in RunBlocksUntilReady tests.
const routerReadyDelay = testtime.D100ms

// routerSlowHandlerDelay is the time a slow handler takes to process a message;
// used to verify drain-during-close timing.
const routerSlowHandlerDelay = testtime.D200ms

// Compile-time interface check: Router implements SubscriptionValidatorAdder.
var _ cell.SubscriptionValidatorAdder = (*Router)(nil)

// --- Mock Subscriber ---

// blockingSubscriber blocks until ctx is canceled, simulating a healthy broker.
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

func (s *blockingSubscriber) Subscribe(ctx context.Context, sub outbox.Subscription, _ outbox.SubscriberHandler) error {
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

func (s *failingSubscriber) Subscribe(_ context.Context, _ outbox.Subscription, _ outbox.SubscriberHandler) error {
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

func (s *delayedFailSubscriber) Subscribe(ctx context.Context, _ outbox.Subscription, _ outbox.SubscriberHandler) error {
	select {
	case <-time.After(s.delay):
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *delayedFailSubscriber) Close(_ context.Context) error { return nil }

// --- Tests ---

func TestRouter_AddContractHandler_RegistersTopics(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real())

	_ = r.AddContractHandler(testEventSpec("topic.a"), noopHandler, "test", "test")
	_ = r.AddContractHandler(testEventSpec("topic.b"), noopHandler, "test", "test")

	assert.Equal(t, 2, r.HandlerCount())
}

func TestRouter_Run_StartsAllSubscriptions(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real())

	_ = r.AddContractHandler(testEventSpec("topic.a"), noopHandler, "test", "test")
	_ = r.AddContractHandler(testEventSpec("topic.b"), noopHandler, "test", "test")
	_ = r.AddContractHandler(testEventSpec("topic.c"), noopHandler, "test", "test")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Wait for Running signal.
	select {
	case <-r.Running():
	case <-time.After(testtime.D2s):
		t.Fatal("Router did not become ready")
	}

	// Subscribe goroutines are launched concurrently; give them a moment to
	// register their topics (they run after Phase 3 Ready signals close).
	assert.Eventually(t, func() bool {
		topics := sub.Topics()
		return len(topics) == 3
	}, testtime.D2s, testtime.D10ms, "all 3 topics should be subscribed")

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
	}, testtime.D2s, testtime.MediumPoll)
}

// TestRouter_Run_SubscribeError_ReturnsError verifies that when
// Subscriber.Subscribe returns an error immediately (after Ready fires),
// Run returns the error. Running() is closed first (Phase 3 completes), then
// the runtime error is detected in Phase 4 and returned from Run.
// For Setup-level failures (before any subscription starts), see TestRouter_SetupErrorAborts.
func TestRouter_Run_SubscribeError_ReturnsError(t *testing.T) {
	subscribeErr := errcode.New(errcode.KindInternal, errcode.ErrBusClosed, "bus closed")
	sub := &failingSubscriber{err: subscribeErr}
	r := New(wrap(sub), clock.Real())

	_ = r.AddContractHandler(testEventSpec("topic.fail"), noopHandler, "test", "test")

	ctx := t.Context()

	err := r.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "topic.fail")
}

func TestRouter_Run_NoHandlers_BlocksUntilCancel(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Running should be closed immediately.
	select {
	case <-r.Running():
	case <-time.After(testtime.EventuallyShort):
		t.Fatal("Running() should close immediately with no handlers")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(testtime.EventuallyShort):
		t.Fatal("Run did not exit after cancel")
	}
}

func TestRouter_Close_CancelsSubscriptions(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real())

	_ = r.AddContractHandler(testEventSpec("topic.a"), noopHandler, "test", "test")

	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	<-r.Running()

	err := r.Close(context.Background())
	assert.NoError(t, err)

	select {
	case <-done:
	case <-time.After(testtime.D2s):
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

	r := New(wrap(bus), clock.Real())
	_ = r.AddContractHandler(testEventSpec("test.topic"), handler, "test", "test")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	<-r.Running()

	// Publish a message.
	err := bus.Publish(context.Background(), "test.topic", []byte(`{"key":"value"}`))
	require.NoError(t, err)

	assert.Eventually(t, func() bool {
		return received.Load() >= 1
	}, testtime.EventuallyShort, testtime.D10ms)

	cancel()
	<-done
}

func TestRouter_Run_MultipleHandlersSameSubscriber(t *testing.T) {
	bus := newTestEventBus(t)

	var countA, countB atomic.Int32

	r := New(wrap(bus), clock.Real())
	require.NoError(t, r.AddContractHandler(testEventSpec("topic.a"), func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		countA.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}, "test", "test"))
	require.NoError(t, r.AddContractHandler(testEventSpec("topic.b"), func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		countB.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}, "test", "test"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	<-r.Running()

	require.NoError(t, bus.Publish(context.Background(), "topic.a", []byte(`{}`)))
	require.NoError(t, bus.Publish(context.Background(), "topic.b", []byte(`{}`)))

	assert.Eventually(t, func() bool {
		return countA.Load() >= 1 && countB.Load() >= 1
	}, testtime.EventuallyShort, testtime.D10ms)

	cancel()
	<-done
}

func TestRouter_RegistryRecorder_Integration(t *testing.T) {
	// Simulate the bootstrap pattern: cell registers handlers via RegistryRecorder,
	// bootstrap drains them into the Router, then Run delivers messages.
	bus := newTestEventBus(t)
	r := New(wrap(bus), clock.Real())

	var received atomic.Int32

	handler := outbox.EntryHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		received.Add(1)
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	// Simulate bootstrap drain: AddContractHandler directly (mirrors phase6 loop).
	require.NoError(t, r.AddContractHandler(testEventSpec("mock.topic"), handler, "mock-cell", "mock-cell"))
	assert.Equal(t, 1, r.HandlerCount())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	<-r.Running()

	require.NoError(t, bus.Publish(context.Background(), "mock.topic", []byte(`{}`)))

	assert.Eventually(t, func() bool {
		return received.Load() >= 1
	}, testtime.EventuallyShort, testtime.D10ms)

	cancel()
	<-done
}

func TestRouter_Run_RuntimeError_AfterStartup(t *testing.T) {
	// delayedFailSubscriber: Ready returns immediately-closed channel (so Router
	// marks itself Running fast), then Subscribe returns an error after the delay.
	sub := &delayedFailSubscriber{
		delay: testtime.D100ms,
		err:   errors.New("connection lost"),
	}
	r := New(wrap(sub), clock.Real())
	_ = r.AddContractHandler(testEventSpec("topic.a"), noopHandler, "test", "test")

	ctx := t.Context()

	err := r.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection lost")
}

func TestRouter_HealthLifecycle(t *testing.T) {
	// subscriber that is ready immediately but fails after 300ms.
	sub := &delayedFailSubscriber{
		delay: testtime.D300ms,
		err:   errors.New("connection lost"),
	}
	r := New(wrap(sub), clock.Real())
	_ = r.AddContractHandler(testEventSpec("topic.a"), noopHandler, "test", "test")

	require.Error(t, r.Health(), "router must be unhealthy before Run")

	ctx := t.Context()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(testtime.D2s):
		t.Fatal("router did not become ready")
	}

	require.NoError(t, r.Health(), "router must be healthy after startup")

	assert.Eventually(t, func() bool {
		return r.Health() != nil
	}, testtime.D2s, testtime.D20ms, "router must become unhealthy after runtime failure")

	err := <-done
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection lost")
}

func TestRouter_Health_AfterGracefulShutdown(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real())
	_ = r.AddContractHandler(testEventSpec("topic.a"), noopHandler, "test", "test")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(testtime.D2s):
		t.Fatal("router did not become ready")
	}

	require.NoError(t, r.Health(), "router must be healthy before shutdown")

	cancel()
	select {
	case <-done:
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("router did not shut down")
	}

	err := r.Health()
	require.Error(t, err, "router must be unhealthy after graceful shutdown")
	assert.Contains(t, err.Error(), "shutting down")
}

func TestRouter_Run_DoubleRun_ReturnsError(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real())
	_ = r.AddContractHandler(testEventSpec("topic.a"), noopHandler, "test", "test")

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
	r := New(wrap(sub), clock.Real())

	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	<-r.Running()

	// Close should terminate Run even with zero handlers.
	err := r.Close(context.Background())
	assert.NoError(t, err)

	select {
	case <-done:
	case <-time.After(testtime.D2s):
		t.Fatal("Run did not exit after Close with zero handlers")
	}
}

func TestRouter_Close_Timeout(t *testing.T) {
	// Subscriber that ignores context cancellation (simulates stuck goroutine).
	stuck := make(chan struct{})
	sub := &stuckSubscriber{block: stuck}
	r := New(wrap(sub), clock.Real())
	_ = r.AddContractHandler(testEventSpec("topic.stuck"), noopHandler, "test", "test")

	ctx := t.Context()
	go func() { _ = r.Run(ctx) }()

	<-r.Running()

	// Close with a very short timeout — should return context deadline error.
	closeCtx, closeCancel := context.WithTimeout(context.Background(), testtime.MediumPoll)
	defer closeCancel()
	err := r.Close(closeCtx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)

	close(stuck) // unblock the subscriber so test cleanup works
}

func TestRouter_AddContractHandler_ReturnsErrorOnEmptyTopic(t *testing.T) {
	r := New(wrap(&blockingSubscriber{}), clock.Real())
	assert.Error(t, r.AddContractHandler(testEventSpec(""), noopHandler, "test", "test"))
}

func TestRouter_AddContractHandler_ReturnsErrorOnNilHandler(t *testing.T) {
	r := New(wrap(&blockingSubscriber{}), clock.Real())
	assert.Error(t, r.AddContractHandler(testEventSpec("topic"), nil, "test", "test"))
}

func TestRouter_Run_PanicInSubscriber_CapturedAsError(t *testing.T) {
	// A subscriber whose Subscribe panics.
	panickySub := &panickingSubscriber{}
	r := New(wrap(panickySub), clock.Real())
	_ = r.AddContractHandler(testEventSpec("topic.panic"), noopHandler, "test", "test")

	ctx := t.Context()

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

func (s *stuckSubscriber) Subscribe(_ context.Context, _ outbox.Subscription, _ outbox.SubscriberHandler) error {
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

func (s *panickingSubscriber) Subscribe(_ context.Context, _ outbox.Subscription, _ outbox.SubscriberHandler) error {
	panic("boom")
}
func (s *panickingSubscriber) Close(_ context.Context) error { return nil }

// --- Helpers ---

var noopHandler = func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
	return outbox.HandleResult{Disposition: outbox.DispositionAck}
}

// wrap wraps an outbox.Subscriber in a *outbox.SubscriberWithMiddleware with
// the minimal ConsumerBase required by SubscribeEntry.
func wrap(sub outbox.Subscriber) *outbox.SubscriberWithMiddleware {
	cb, err := outbox.NewConsumerBase(
		routerTestClaimer{},
		outbox.ConsumerBaseConfig{},
		clock.Real(),
	)
	if err != nil {
		panic(err)
	}
	swm, err := outbox.NewSubscriberWithMiddleware(sub, cb)
	if err != nil {
		panic(err)
	}
	return swm
}

type routerTestClaimer struct{}

func (routerTestClaimer) Claim(
	context.Context, string, time.Duration, time.Duration,
) (idempotency.ClaimState, idempotency.Receipt, error) {
	return idempotency.ClaimAcquired, routerTestReceipt{}, nil
}

type routerTestReceipt struct{}

func (routerTestReceipt) Commit(context.Context) error                { return nil }
func (routerTestReceipt) Release(context.Context) error               { return nil }
func (routerTestReceipt) Extend(context.Context, time.Duration) error { return nil }

// ---------------------------------------------------------------------------
// SubscriptionValidator tests (Finding 2 — registration-time validation)
// ---------------------------------------------------------------------------

// TestRouter_AddSubscriptionValidator_ChainSucceeds verifies that when all
// registered validators return nil, AddContractHandler succeeds.
func TestRouter_AddSubscriptionValidator_ChainSucceeds(t *testing.T) {
	r := New(wrap(&blockingSubscriber{}), clock.Real())

	r.AddSubscriptionValidator(func(_ outbox.Subscription) error { return nil })
	r.AddSubscriptionValidator(func(_ outbox.Subscription) error { return nil })

	err := r.AddContractHandler(testEventSpec("event.config.entry-upserted.v1"), noopHandler, "accesscore", "accesscore",
		cell.WithSubscriptionSliceID("configreceive"))
	require.NoError(t, err)
}

// TestRouter_AddSubscriptionValidator_FirstFailureAggregated verifies that when
// multiple validators fail, both errors are surfaced via errors.Join.
func TestRouter_AddSubscriptionValidator_FirstFailureAggregated(t *testing.T) {
	r := New(wrap(&blockingSubscriber{}), clock.Real())

	sentinel1 := errors.New("validator-1 failed")
	sentinel2 := errors.New("validator-2 failed")
	r.AddSubscriptionValidator(func(_ outbox.Subscription) error { return sentinel1 })
	r.AddSubscriptionValidator(func(_ outbox.Subscription) error { return sentinel2 })

	err := r.AddContractHandler(testEventSpec("event.config.entry-upserted.v1"), noopHandler, "accesscore", "accesscore")
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel1, "first validator error must be included")
	assert.ErrorIs(t, err, sentinel2, "second validator error must be included")
}

// TestRouter_AddSubscriptionValidator_NilSkipped verifies that a nil validator
// in the chain does not panic and that a non-nil validator still fires.
func TestRouter_AddSubscriptionValidator_NilSkipped(t *testing.T) {
	r := New(wrap(&blockingSubscriber{}), clock.Real())

	sentinel := errors.New("non-nil validator failed")
	r.AddSubscriptionValidator(nil)
	r.AddSubscriptionValidator(func(_ outbox.Subscription) error { return sentinel })

	err := r.AddContractHandler(testEventSpec("event.config.entry-upserted.v1"), noopHandler, "accesscore", "accesscore")
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
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
		return errcode.New(errcode.KindInternal, errcode.ErrBusClosed, "closed")
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
			time.Sleep(testtime.D1ms) //archtest:allow:test-sleep poll loop waiting for subscription to register; no notification API
		}
	}()
	return ch
}

func (b *testBus) Subscribe(ctx context.Context, sub outbox.Subscription, handler outbox.SubscriberHandler) error {
	subCtx, cancel := context.WithCancel(ctx)
	s := &testSub{ch: make(chan outbox.Entry, b.bufSize), cancel: cancel}

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		cancel()
		return errcode.New(errcode.KindInternal, errcode.ErrBusClosed, "closed")
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
	CellID        string
	SliceID       string
}

func (s *recordingGroupSubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }
func (s *recordingGroupSubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (s *recordingGroupSubscriber) Subscribe(ctx context.Context, sub outbox.Subscription, _ outbox.SubscriberHandler) error {
	s.mu.Lock()
	s.calls = append(s.calls, groupSubscribeCall{
		Topic:         sub.Topic,
		ConsumerGroup: sub.ConsumerGroup,
		CellID:        sub.CellID,
		SliceID:       sub.SliceID,
	})
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
// passed to AddContractHandler is forwarded verbatim to Subscriber.Subscribe via Subscription.
func TestRouter_ConsumerGroup_PropagatesToSubscriber(t *testing.T) {
	sub := &recordingGroupSubscriber{}
	r := New(wrap(sub), clock.Real())

	_ = r.AddContractHandler(testEventSpec("session.created"), noopHandler, "auditcore", "auditcore")
	_ = r.AddContractHandler(testEventSpec("config.entry-upserted"), noopHandler,
		"configcore", "configcore", cell.WithSubscriptionSliceID("configsubscribe"))
	_ = r.AddContractHandler(testEventSpec("legacy.event"), noopHandler, "legacy-cell", "legacy-cell")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Wait for all subscriptions to start.
	require.Eventually(t, func() bool {
		return len(sub.Calls()) >= 3
	}, testtime.D2s, testtime.D10ms)

	cancel()
	<-done

	calls := sub.Calls()
	assert.Len(t, calls, 3)

	// Build a map for order-independent assertions.
	groupByTopic := make(map[string]string)
	for _, c := range calls {
		groupByTopic[c.Topic] = c.ConsumerGroup
	}

	assert.Equal(t, "auditcore", groupByTopic["session.created"])
	assert.Equal(t, "configcore", groupByTopic["config.entry-upserted"])
	assert.Equal(t, "legacy-cell", groupByTopic["legacy.event"])

	var configCall groupSubscribeCall
	for _, c := range calls {
		if c.Topic == "config.entry-upserted" {
			configCall = c
		}
	}
	assert.Equal(t, "configcore", configCall.CellID)
	assert.Equal(t, "configsubscribe", configCall.SliceID)
}

func TestRouter_AddContractHandler_ReturnsErrorOnEmptyConsumerGroup(t *testing.T) {
	r := New(wrap(&blockingSubscriber{}), clock.Real())
	err := r.AddContractHandler(testEventSpec("topic"), noopHandler, "", "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty consumerGroup")
}

// TestRouter_OwnerCellID_DistinctFromConsumerGroup verifies that when
// ownerCellID differs from consumerGroup, Subscription.CellID carries
// ownerCellID — not consumerGroup. This is the accesscore RBAC sync scenario:
//
//	ConsumerGroup = "accesscore-rbac-session-sync"   (role-specific queue name)
//	OwnerCellID   = "accesscore"                     (true owning cell)
//
// ref: watermill router.AddHandler handlerName / NATS subscription metadata.
func TestRouter_OwnerCellID_DistinctFromConsumerGroup(t *testing.T) {
	sub := &recordingGroupSubscriber{}
	r := New(wrap(sub), clock.Real())

	const consumerGroup = "accesscore-rbac-session-sync"
	const ownerCellID = "accesscore"

	err := r.AddContractHandler(testEventSpec("session.created"), noopHandler, consumerGroup, ownerCellID)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	require.Eventually(t, func() bool {
		return len(sub.Calls()) >= 1
	}, testtime.D2s, testtime.D10ms)

	cancel()
	<-done

	calls := sub.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, consumerGroup, calls[0].ConsumerGroup,
		"ConsumerGroup must be forwarded verbatim to the broker")
	assert.Equal(t, ownerCellID, calls[0].CellID,
		"Subscription.CellID must carry ownerCellID, not consumerGroup")
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
		time.Sleep(s.delay) //archtest:allow:test-sleep sleep IS the test parameter
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

func (s *delayedReadySubscriber) Subscribe(ctx context.Context, _ outbox.Subscription, _ outbox.SubscriberHandler) error {
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

func (s *setupFailSubscriber) Subscribe(_ context.Context, _ outbox.Subscription, _ outbox.SubscriberHandler) error {
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
			time.Sleep(s.slowDelay) //archtest:allow:test-sleep sleep IS the test parameter
			close(ch)
		}()
	} else {
		close(ch)
	}
	return ch
}

func (s *partialReadySubscriber) Subscribe(ctx context.Context, _ outbox.Subscription, _ outbox.SubscriberHandler) error {
	<-ctx.Done()
	return ctx.Err()
}
func (s *partialReadySubscriber) Close(_ context.Context) error { return nil }

// TestRouter_RunBlocksUntilReady_NoTimeout verifies that Router.Running() is
// NOT closed until the Subscriber.Ready signal fires. The Ready channel closes
// after 100ms; Running() must close within that window (not at 500ms).
func TestRouter_RunBlocksUntilReady_NoTimeout(t *testing.T) {
	const readyDelay = routerReadyDelay
	sub := newDelayedReadySubscriber(readyDelay)

	r := New(wrap(sub), clock.Real())
	_ = r.AddContractHandler(testEventSpec("topic.a"), noopHandler, "test", "test")

	ctx := t.Context()

	start := time.Now()
	go func() { _ = r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(testtime.D2s):
		t.Fatal("Router did not become ready within 2s")
	}

	elapsed := time.Since(start)
	// Ready fires at ~100ms. Allow ±50ms tolerance.
	assert.GreaterOrEqual(t, elapsed, readyDelay-testtime.D10ms,
		"Router became ready too early (before Ready signal fired)")
	assert.Less(t, elapsed, readyDelay+testtime.MediumPoll,
		"Router took too long after Ready signal (should not wait 500ms)")
}

// TestRouter_SetupErrorAborts verifies that when Subscriber.Setup returns an
// error, Run returns that error immediately and Subscribe is never called.
func TestRouter_SetupErrorAborts(t *testing.T) {
	setupErr := errors.New("topology declaration failed")
	sub := &setupFailSubscriber{err: setupErr}

	r := New(wrap(sub), clock.Real())
	_ = r.AddContractHandler(testEventSpec("topic.fail"), noopHandler, "test", "test")

	ctx := t.Context()

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
			time.Sleep(s.slowReadyDelay) //archtest:allow:test-sleep sleep IS the test parameter
			close(ch)
		}()
		return ch
	}
	close(ch)
	return ch
}

func (s *mixedReadySubscriber) Subscribe(ctx context.Context, sub outbox.Subscription, _ outbox.SubscriberHandler) error {
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
		slowReadyDelay:    testtime.EventuallyLong, // would block test if Run waited
	}

	r := New(wrap(sub), clock.Real(), WithReadyTimeout(0)) // disable timeout to isolate setupErr path

	_ = r.AddContractHandler(testEventSpec("topic.fast"), noopHandler, "test", "test")
	_ = r.AddContractHandler(testEventSpec(sub.subscribeErrTopic), noopHandler, "test", "test")
	_ = r.AddContractHandler(testEventSpec(sub.slowReadyTopic), noopHandler, "test", "test")

	ctx := t.Context()

	runDone := make(chan error, 1)
	start := time.Now()
	go func() { runDone <- r.Run(ctx) }()

	select {
	case err := <-runDone:
		require.Error(t, err)
		assert.ErrorIs(t, err, subErr, "Run should propagate the Subscribe error")
		elapsed := time.Since(start)
		assert.Less(t, elapsed, testtime.D1s,
			"Run should return promptly on Subscribe error, not wait for slow Ready (5s)")
	case <-time.After(testtime.D2s):
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
	const slowDelay = routerSlowHandlerDelay
	sub := &partialReadySubscriber{
		slowTopic: "topic.c",
		slowDelay: slowDelay,
	}

	r := New(wrap(sub), clock.Real())
	_ = r.AddContractHandler(testEventSpec("topic.a"), noopHandler, "test", "test")
	_ = r.AddContractHandler(testEventSpec("topic.b"), noopHandler, "test", "test")
	_ = r.AddContractHandler(testEventSpec("topic.c"), noopHandler, "test", "test")

	ctx := t.Context()

	start := time.Now()
	go func() { _ = r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(testtime.D2s):
		t.Fatal("Router did not become ready within 2s")
	}

	elapsed := time.Since(start)
	// All three Ready channels must close; the last is at ~200ms.
	assert.GreaterOrEqual(t, elapsed, slowDelay-testtime.D10ms,
		"Router became ready before all Ready channels closed")
	assert.Less(t, elapsed, slowDelay+testtime.D100ms,
		"Router took too long after all Ready signals fired")
}
