package eventrouter

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// ---------------------------------------------------------------------------
// Spy EventCollector
// ---------------------------------------------------------------------------

type spyEvent struct {
	cellID string
	topic  string
	reason string
}

type spyDuration struct {
	cellID   string
	duration time.Duration
}

type spyEventCollector struct {
	mu          sync.Mutex
	setupErrors []spyEvent
	inc         []string
	dec         []string
	readyWaits  []spyDuration
}

func (s *spyEventCollector) IncSubscriptionActive(cellID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inc = append(s.inc, cellID)
}

func (s *spyEventCollector) DecSubscriptionActive(cellID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dec = append(s.dec, cellID)
}

func (s *spyEventCollector) RecordSetupError(cellID, topic, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setupErrors = append(s.setupErrors, spyEvent{cellID: cellID, topic: topic, reason: reason})
}

func (s *spyEventCollector) ObserveReadyWait(cellID string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readyWaits = append(s.readyWaits, spyDuration{cellID: cellID, duration: d})
}

func (s *spyEventCollector) incCopy() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.inc))
	copy(out, s.inc)
	return out
}

func (s *spyEventCollector) decCopy() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.dec))
	copy(out, s.dec)
	return out
}

func (s *spyEventCollector) errorsCopy() []spyEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]spyEvent, len(s.setupErrors))
	copy(out, s.setupErrors)
	return out
}

func (s *spyEventCollector) waitsCopy() []spyDuration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]spyDuration, len(s.readyWaits))
	copy(out, s.readyWaits)
	return out
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestRouter_Run_IncSubscriptionActive_PerHandler verifies that when two handlers
// with different cellIDs both become ready, IncSubscriptionActive is called once
// per cellID.
func TestRouter_Run_IncSubscriptionActive_PerHandler(t *testing.T) {
	spy := &spyEventCollector{}
	sub := &recordingGroupSubscriber{}
	r := New(wrap(sub), clock.Real(), WithEventRouterCollector(spy))

	require.NoError(t, r.AddContractHandler(testEventSpec("topic.a"), noopHandler, "cell-alpha", "cell-alpha"))
	require.NoError(t, r.AddContractHandler(testEventSpec("topic.b"), noopHandler, "cell-beta", "cell-beta"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(testtime.D2s):
		t.Fatal("Router did not become ready")
	}

	incs := spy.incCopy()
	assert.Len(t, incs, 2, "IncSubscriptionActive must be called once per handler")
	assert.Contains(t, incs, "cell-alpha")
	assert.Contains(t, incs, "cell-beta")

	cancel()
	<-done
}

// TestRouter_Run_ObserveReadyWait_PerHandler verifies that ObserveReadyWait is
// called for each handler and records a non-negative duration.
func TestRouter_Run_ObserveReadyWait_PerHandler(t *testing.T) {
	spy := &spyEventCollector{}
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real(), WithEventRouterCollector(spy))

	require.NoError(t, r.AddContractHandler(testEventSpec("topic.rw"), noopHandler, "cell-rw", "cell-rw"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(testtime.D2s):
		t.Fatal("Router did not become ready")
	}

	waits := spy.waitsCopy()
	require.Len(t, waits, 1, "ObserveReadyWait must be called once per handler")
	assert.Equal(t, "cell-rw", waits[0].cellID)
	assert.GreaterOrEqual(t, waits[0].duration, time.Duration(0))

	cancel()
	<-done
}

// TestRouter_runSetup_ObserveSetupError_OnSetupFailure verifies that when
// Subscriber.Setup returns an error, RecordSetupError is called with
// reason="setup_error".
func TestRouter_runSetup_ObserveSetupError_OnSetupFailure(t *testing.T) {
	spy := &spyEventCollector{}
	setupErr := &setupFailSubscriber{err: assert.AnError}
	r := New(wrap(setupErr), clock.Real(), WithEventRouterCollector(spy))

	require.NoError(t, r.AddContractHandler(testEventSpec("topic.fail"), noopHandler, "cell-fail", "cell-fail"))

	ctx := t.Context()
	err := r.Run(ctx)
	require.Error(t, err)

	errs := spy.errorsCopy()
	require.Len(t, errs, 1, "RecordSetupError must be called once on Setup failure")
	assert.Equal(t, "cell-fail", errs[0].cellID)
	assert.Equal(t, "topic.fail", errs[0].topic)
	assert.Equal(t, "setup_error", errs[0].reason)
}

// TestRouter_runAwaitReady_Timeout_ObserveSetupError_ReadyTimeout verifies that
// when the ready timeout fires and a handler has not readied, RecordSetupError
// is called with reason="ready_timeout".
func TestRouter_runAwaitReady_Timeout_ObserveSetupError_ReadyTimeout(t *testing.T) {
	spy := &spyEventCollector{}

	// neverReadySubscriber: Setup succeeds, Ready never closes, Subscribe blocks on ctx.
	neverReady := &neverReadySubscriber{}
	r := New(wrap(neverReady), clock.Real(),
		WithEventRouterCollector(spy),
		WithReadyTimeout(10*time.Millisecond), // very short timeout
	)

	require.NoError(t, r.AddContractHandler(testEventSpec("topic.stuck"), noopHandler, "cell-stuck", "cell-stuck"))

	ctx := t.Context()
	err := r.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not ready")

	errs := spy.errorsCopy()
	require.Len(t, errs, 1, "RecordSetupError must be called once for ready_timeout")
	assert.Equal(t, "cell-stuck", errs[0].cellID)
	assert.Equal(t, "topic.stuck", errs[0].topic)
	assert.Equal(t, "ready_timeout", errs[0].reason)
}

// TestRouter_Close_DecSubscriptionActive_PerHandler verifies that after Run
// completes and Close is called, DecSubscriptionActive is called for each
// handler that was previously Inc'd (balancing the gauge).
func TestRouter_Close_DecSubscriptionActive_PerHandler(t *testing.T) {
	spy := &spyEventCollector{}
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real(), WithEventRouterCollector(spy))

	require.NoError(t, r.AddContractHandler(testEventSpec("topic.x"), noopHandler, "cell-x", "cell-x"))
	require.NoError(t, r.AddContractHandler(testEventSpec("topic.y"), noopHandler, "cell-y", "cell-y"))

	ctx := context.Background()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(testtime.D2s):
		t.Fatal("Router did not become ready")
	}

	// Inc should have been called for both cells.
	assert.Len(t, spy.incCopy(), 2)

	err := r.Close(context.Background())
	require.NoError(t, err)

	select {
	case <-done:
	case <-time.After(testtime.D2s):
		t.Fatal("Run did not exit after Close")
	}

	// Dec should balance Inc exactly.
	decs := spy.decCopy()
	incs := spy.incCopy()
	assert.Len(t, decs, len(incs), "Dec count must match Inc count")
	assert.Contains(t, decs, "cell-x")
	assert.Contains(t, decs, "cell-y")
}

// TestRouter_NoCollector_DefaultsToNop verifies that constructing a Router
// without WithEventRouterCollector defaults to NopEventCollector and does not
// panic during normal Run/Close.
func TestRouter_NoCollector_DefaultsToNop(t *testing.T) {
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real()) // no collector option

	assert.IsType(t, NopEventCollector{}, r.collector, "default collector must be NopEventCollector")

	require.NoError(t, r.AddContractHandler(testEventSpec("topic.nop"), noopHandler, "cell-nop", "cell-nop"))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	select {
	case <-r.Running():
	case <-time.After(testtime.D2s):
		t.Fatal("Router did not become ready")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(testtime.D2s):
		t.Fatal("Run did not exit after cancel")
	}
}

// TestRouter_WithEventRouterCollector_TypedNilIgnored verifies that passing a
// typed-nil EventCollector to WithEventRouterCollector is silently ignored and
// the Router still falls back to NopEventCollector.
func TestRouter_WithEventRouterCollector_TypedNilIgnored(t *testing.T) {
	var c *spyEventCollector // typed nil
	sub := &blockingSubscriber{}
	r := New(wrap(sub), clock.Real(), WithEventRouterCollector(c))
	assert.IsType(t, NopEventCollector{}, r.collector, "typed-nil collector must be ignored; fallback to NopEventCollector")
}

// ---------------------------------------------------------------------------
// Helper subscriber: never signals Ready
// ---------------------------------------------------------------------------

// neverReadySubscriber: Setup succeeds, Ready never closes, Subscribe blocks ctx.
type neverReadySubscriber struct{}

func (s *neverReadySubscriber) Setup(_ context.Context, _ outbox.Subscription) error { return nil }
func (s *neverReadySubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	return make(chan struct{}) // never closes
}
func (s *neverReadySubscriber) Subscribe(ctx context.Context, _ outbox.Subscription, _ outbox.SubscriberHandler) error {
	<-ctx.Done()
	return ctx.Err()
}
func (s *neverReadySubscriber) Close(_ context.Context) error { return nil }
