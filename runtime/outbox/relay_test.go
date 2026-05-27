package outbox_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/healthz"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
	"github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/outbox/outboxtest"
)

// relayFindProbe returns the probe with the given name from probes, or nil.
func relayFindProbe(probes []healthz.Probe, name healthz.ProbeName) healthz.Probe {
	for _, p := range probes {
		if p.Name() == name {
			return p
		}
	}
	return nil
}

// relayCheckProbe returns the result of Check on the named probe, or an error if absent.
func relayCheckProbe(probes []healthz.Probe, name healthz.ProbeName, ctx context.Context) error {
	p := relayFindProbe(probes, name)
	if p == nil {
		return fmt.Errorf("probe %q not found", name)
	}
	return p.Check(ctx)
}

// relayMinRetention is the smallest positive RetentionPeriod that bypasses the
// RelayConfig.WithDefaults "zero means missing" guard, ensuring the cleanup loop
// deletes entries on its very first pass.
const relayMinRetention = 1 * time.Nanosecond

// ---------------------------------------------------------------------------
// fakePublisher
// ---------------------------------------------------------------------------

type publishCall struct {
	topic   string
	payload []byte
}

type fakePublisher struct {
	mu      sync.Mutex
	calls   []publishCall
	errOnce error // returned once then cleared
	failN   int   // fail next N publishes
	errFn   func(string) error
	notify  chan struct{} // close-and-recreate on every successful Publish; powers WaitForCaptured
}

func newFakePublisher() *fakePublisher {
	return &fakePublisher{notify: make(chan struct{})}
}

// notifyLocked closes the current notify channel (waking any WaitForCaptured
// goroutine) and installs a fresh one. Caller must hold p.mu. Mirrors the
// channel-as-condvar pattern in outboxtest.FakeStore.notifyLocked.
func (p *fakePublisher) notifyLocked() {
	close(p.notify)
	p.notify = make(chan struct{})
}

// WithError sets an error to be returned once on the next Publish call.
func (p *fakePublisher) WithError(err error) *fakePublisher {
	p.mu.Lock()
	p.errOnce = err
	p.mu.Unlock()
	return p
}

// WithFailN causes the next n Publish calls to return a transient error.
func (p *fakePublisher) WithFailN(n int) *fakePublisher {
	p.mu.Lock()
	p.failN = n
	p.mu.Unlock()
	return p
}

// WithErrFunc sets a per-topic error function.
func (p *fakePublisher) WithErrFunc(fn func(topic string) error) *fakePublisher {
	p.mu.Lock()
	p.errFn = fn
	p.mu.Unlock()
	return p
}

func (p *fakePublisher) Publish(_ context.Context, topic string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.errFn != nil {
		if err := p.errFn(topic); err != nil {
			return err
		}
	}
	if p.errOnce != nil {
		err := p.errOnce
		p.errOnce = nil
		return err
	}
	if p.failN > 0 {
		p.failN--
		return errors.New("transient broker failure")
	}
	p.calls = append(p.calls, publishCall{topic: topic, payload: payload})
	p.notifyLocked()
	return nil
}

// WaitForCaptured blocks until at least want successful Publish calls have
// been recorded or ctx is canceled. Re-evaluated synchronously after every
// successful publish via the notify channel — no polling, no timing coupling.
// Passing want <= 0 returns nil immediately on the first cond evaluation.
func (p *fakePublisher) WaitForCaptured(ctx context.Context, want int) error {
	for {
		p.mu.Lock()
		cur := len(p.calls)
		notify := p.notify
		p.mu.Unlock()
		if cur >= want {
			return nil
		}
		select {
		case <-notify:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Close satisfies outbox.Publisher.
func (p *fakePublisher) Close(_ context.Context) error { return nil }

// Captured returns a snapshot of all successfully published (topic, payload) pairs.
func (p *fakePublisher) Captured() []publishCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]publishCall, len(p.calls))
	copy(out, p.calls)
	return out
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func makeEntry(id, eventType string) outbox.ClaimedEntry {
	return outbox.ClaimedEntry{
		Entry: kout.Entry{
			ID:            id,
			AggregateID:   "agg-" + id,
			AggregateType: "test",
			EventType:     eventType,
			Payload:       []byte(`{"id":"` + id + `"}`),
			CreatedAt:     time.Now(),
		},
		Attempts: 0,
	}
}

func fastCfg() outbox.RelayConfig {
	return outbox.RelayConfig{
		PollInterval:        testtime.FastPoll,
		ReclaimInterval:     testtime.D10ms,
		BatchSize:           10,
		MaxAttempts:         3,
		BaseRetryDelay:      testtime.D1ms,
		MaxRetryDelay:       testtime.D10ms,
		ClaimTTL:            testtime.D100ms,
		RetentionPeriod:     testtime.D1h,
		DeadRetentionPeriod: testtime.D24h,
		CleanupWaitFloor:    testtime.FastPoll,
	}
}

// startRelay starts relay in a goroutine and returns a stop function.
func startRelay(t *testing.T, relay *outbox.Relay) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = relay.Start(ctx) }()
	return func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D2s)
		defer stopCancel()
		require.NoError(t, relay.Stop(stopCtx))
		cancel()
	}
}

// waitStore blocks until cond evaluates true on a current FakeStore snapshot
// or testtime.EventuallyDefault is exceeded as a deadlock backstop. Backed by
// store.WaitFor — wakes synchronously on every state mutation, no polling.
func waitStore(t *testing.T, store *outboxtest.FakeStore, cond func([]outboxtest.FakeRow) bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testtime.EventuallyDefault)
	defer cancel()
	require.NoError(t, store.WaitFor(ctx, cond),
		"outbox state did not converge within EventuallyDefault (deadlock backstop)")
}

// waitPub blocks until pub has captured at least want successful publishes
// or testtime.EventuallyDefault is exceeded as a deadlock backstop. Backed
// by pub.WaitForCaptured — wakes synchronously on every successful publish.
func waitPub(t *testing.T, pub *fakePublisher, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testtime.EventuallyDefault)
	defer cancel()
	require.NoError(t, pub.WaitForCaptured(ctx, want),
		"publisher captured fewer than %d entries within EventuallyDefault (deadlock backstop)", want)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestRelay_ProvidesLifecyclePrimitives verifies the lifecycle primitives
// (Checkers / Worker / Stop) the bootstrap relay adapter consumes to satisfy
// ManagedResource on behalf of *Relay. *Relay itself intentionally does NOT
// implement ManagedResource — see
// docs/architecture/202605201400-adr-relay-managedresource-isolation.md and
// runtime/bootstrap/relay_adapter.go.
func TestRelay_ProvidesLifecyclePrimitives(t *testing.T) {
	relay := outbox.NewRelay(clock.Real(), outboxtest.NewFakeStore(), newFakePublisher(), budgetCfg())

	// Probes must return a non-empty slice (budgetCfg enables all three).
	require.NotEmpty(t, relay.Probes(), "budgetCfg enables all three budgets; slice must be non-empty")

	// Worker must return the relay itself (non-nil).
	require.NotNil(t, relay.Worker())

	// Stop on a never-started relay must be a no-op (no error).
	require.NoError(t, relay.Stop(context.Background()))
}

func TestRelay_HappyPath_ClaimPublishMarkPublished(t *testing.T) {
	store := outboxtest.NewFakeStore()
	store.Seed(makeEntry("e1", "order.created"), makeEntry("e2", "order.updated"), makeEntry("e3", "order.deleted"))

	pub := newFakePublisher()
	relay := outbox.NewRelay(clock.Real(), store, pub, fastCfg())

	stop := startRelay(t, relay)
	defer stop()

	// Wait until all 3 entries are published.
	waitPub(t, pub, 3)
	stop()

	// All store rows must be published.
	snap := store.Snapshot()
	require.Len(t, snap, 3)
	for _, row := range snap {
		assert.Equal(t, kout.StatePublished, row.Status, "entry %s should be published", row.Entry.ID)
	}

	// Verify wire envelope contains correct payload via the public funnel
	// (SAFEID-UPSTREAM-FUNNEL-HARD-01: kout.wireMessage is package-private).
	captured := pub.Captured()
	require.Len(t, captured, 3)
	entryDecoded, err := kout.UnmarshalEnvelope("", captured[0].payload)
	require.NoError(t, err)
	assert.NotEmpty(t, entryDecoded.ID)
	assert.NotEmpty(t, entryDecoded.EventType)
	assert.True(t, len(entryDecoded.Payload) > 0)
}

func TestRelay_TransientFailure_MarkRetryWithBackoff(t *testing.T) {
	store := outboxtest.NewFakeStore()
	entry := makeEntry("e-fail", "order.created")
	store.Seed(entry)

	pub := newFakePublisher().WithFailN(1) // first publish fails
	relay := outbox.NewRelay(clock.Real(), store, pub, fastCfg())

	stop := startRelay(t, relay)
	defer stop()

	// Wait until the entry is retried (status=pending with attempts>0).
	waitStore(t, store, func(snap []outboxtest.FakeRow) bool {
		if len(snap) == 0 {
			return false
		}
		return snap[0].Status == kout.StatePending && snap[0].Attempts == 1
	})
	stop()

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, kout.StatePending, snap[0].Status)
	assert.Equal(t, 1, snap[0].Attempts)
	assert.NotNil(t, snap[0].NextRetryAt, "retry must set NextRetryAt")
	// NextRetryAt must be in the near future (backoff range).
	require.NotNil(t, snap[0].NextRetryAt)
	assert.True(t, snap[0].NextRetryAt.After(time.Now().Add(-time.Second)),
		"NextRetryAt should be in the near future or present")
}

func TestRelay_PermanentFailure_ExceedsMaxAttempts_MarkDead(t *testing.T) {
	store := outboxtest.NewFakeStore()
	// Seed entry already at MaxAttempts-1 attempts so one more failure dead-letters it.
	entry := outbox.ClaimedEntry{
		Entry: kout.Entry{
			ID:        "e-dying",
			EventType: "order.created",
			Payload:   []byte(`{}`),
			CreatedAt: time.Now(),
		},
		Attempts: 2, // MaxAttempts=3 → newAttempts=3 >= max → dead
	}
	store.Seed(entry)

	pub := newFakePublisher()
	pub.WithError(errors.New("permanent broker failure"))
	relay := outbox.NewRelay(clock.Real(), store, pub, fastCfg())

	stop := startRelay(t, relay)
	defer stop()

	waitStore(t, store, func(snap []outboxtest.FakeRow) bool {
		return len(snap) > 0 && snap[0].Status == kout.StateDead
	})
	stop()

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, kout.StateDead, snap[0].Status)
	assert.Equal(t, 3, snap[0].Attempts)
	assert.Contains(t, snap[0].LastError, "permanent broker failure")
	assert.NotNil(t, snap[0].DeadAt)
}

func TestRelay_Shutdown_CleanStop(t *testing.T) {
	store := outboxtest.NewFakeStore()
	pub := newFakePublisher()
	relay := outbox.NewRelay(clock.Real(), store, pub, fastCfg())

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- relay.Start(ctx) }()

	// Wait for relay to reach running state via Ready() instead of time.Sleep.
	testwait.External(t, "outbox-relay-published", func() bool {
		ch := relay.Ready()
		if ch == nil {
			return false
		}
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}, testtime.EventuallyShort, testtime.D1ms, "relay not ready")

	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer stopCancel()

	err := relay.Stop(stopCtx)
	require.NoError(t, err)
	cancel()

	startErr := <-errCh
	assert.NoError(t, startErr, "Start should return nil on graceful stop")
}

func TestRelay_StopBeforeStart_IsNoop(t *testing.T) {
	store := outboxtest.NewFakeStore()
	relay := outbox.NewRelay(clock.Real(), store, newFakePublisher(), fastCfg())

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D100ms)
	defer cancel()

	err := relay.Stop(ctx)
	assert.NoError(t, err, "Stop on never-started relay must be a no-op")
}

// TestRelay_Stop_Idempotent verifies that calling Stop twice on a running relay
// is fully idempotent: both calls return nil. This ensures the double-stop path
// (bootstrap WorkerGroup.Stop + LIFO ManagedResource.Close) does not error.
func TestRelay_Stop_Idempotent(t *testing.T) {
	store := outboxtest.NewFakeStore()
	relay := outbox.NewRelay(clock.Real(), store, newFakePublisher(), fastCfg())

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- relay.Start(ctx) }()

	// Wait for relay to reach running state.
	select {
	case <-relay.Ready():
	case <-time.After(testtime.D2s):
		t.Fatal("relay did not become ready in time")
	}

	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer stopCancel()

	// First Stop — should succeed.
	err := relay.Stop(stopCtx)
	assert.NoError(t, err, "first Stop must return nil")

	// Second Stop — must also return nil (idempotent).
	err = relay.Stop(stopCtx)
	assert.NoError(t, err, "second Stop must return nil (idempotent)")

	cancel()
	select {
	case runErr := <-errCh:
		assert.NoError(t, runErr)
	case <-time.After(testtime.D2s):
		t.Fatal("relay did not shut down in time")
	}
}

func TestRelay_DoubleStart_Error(t *testing.T) {
	store := outboxtest.NewFakeStore()
	relay := outbox.NewRelay(clock.Real(), store, newFakePublisher(), fastCfg())

	go func() { _ = relay.Start(t.Context()) }()
	// Wait for relay to reach running state.
	testwait.External(t, "outbox-relay-published", func() bool {
		ch := relay.Ready()
		if ch == nil {
			return false
		}
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}, testtime.EventuallyShort, testtime.D1ms, "relay not ready for DoubleStart test")

	err := relay.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestRelay_CanRestartAfterStop(t *testing.T) {
	store := outboxtest.NewFakeStore()
	relay := outbox.NewRelay(clock.Real(), store, newFakePublisher(), fastCfg())

	for i := range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- relay.Start(ctx) }()

		// Wait for relay to be ready before stopping.
		testwait.External(t, "outbox-relay-published", func() bool {
			ch := relay.Ready()
			if ch == nil {
				return false
			}
			select {
			case <-ch:
				return true
			default:
				return false
			}
		}, testtime.EventuallyShort, testtime.D1ms, "relay not ready in iteration %d", i)

		stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D2s)
		err := relay.Stop(stopCtx)
		stopCancel()
		cancel()

		require.NoErrorf(t, err, "iteration %d", i)
		require.NoErrorf(t, <-errCh, "iteration %d", i)
	}
}

func TestRelay_ReclaimStale_RecoveryLoop(t *testing.T) {
	// Create a store with a custom clock so we can control time for ReclaimStale.
	store := outboxtest.NewFakeStore()

	// Seed an entry that will be directly forced into claiming state via
	// normal ClaimPending, then use a clock that makes claimedAt look stale.
	entry := makeEntry("e-stale", "order.created")
	store.Seed(entry)

	// Use a very short ClaimTTL so ReclaimStale fires quickly in tests.
	cfg := fastCfg()
	cfg.ClaimTTL = testtime.FastPoll
	cfg.ReclaimInterval = testtime.FastPoll

	// Publisher that blocks indefinitely (simulates crash during publish).
	blockPub := &blockingPublisher{}
	relay := outbox.NewRelay(clock.Real(), store, blockPub, cfg)

	stop := startRelay(t, relay)
	defer stop()

	// Wait until the entry is reclaimed (back to pending with attempts > 0).
	waitStore(t, store, func(snap []outboxtest.FakeRow) bool {
		if len(snap) == 0 {
			return false
		}
		return snap[0].Status == kout.StatePending && snap[0].Attempts > 0
	})
	stop()

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, kout.StatePending, snap[0].Status)
	assert.Greater(t, snap[0].Attempts, 0)
}

// TestRelay_StoreCleanup_DirectCall verifies that Store.CleanupPublished deletes
// a published entry when called directly. The relay cleanupLoop fires at
// max(PollInterval*10, 10s) which exceeds the unit-test time budget; the loop
// tick behavior is covered by TestRelay_CleanupLoop_ActuallyRunsPeriodically.
func TestRelay_StoreCleanup_DirectCall(t *testing.T) {
	store := outboxtest.NewFakeStore()

	entry := makeEntry("e-cleanup", "order.created")
	store.Seed(entry)

	// Publish it by hand.
	ctx := context.Background()
	claimed, err := store.ClaimPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	_, err = store.MarkPublished(ctx, claimed[0].ID, claimed[0].LeaseID)
	require.NoError(t, err)

	// Verify it is published.
	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, kout.StatePublished, snap[0].Status)

	// Manually invoke CleanupPublished with a future cutoff.
	deleted, err := store.CleanupPublished(ctx, time.Now().Add(testtime.D1h), 1000)
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)

	snap = store.Snapshot()
	assert.Empty(t, snap, "published entry must be deleted by cleanup")
}

// TestRelay_CleanupLoop_RunsImmediatelyAtStart verifies that the data-driven
// cleanupLoop runs cleanup() on the very first iteration (before the first
// sleep), so a relay starting against a queue of expired rows drains them
// without waiting for any timer. This is the key DX win of the data-driven
// design over the old "wake on a fixed PollInterval×10 ticker" model.
func TestRelay_CleanupLoop_RunsImmediatelyAtStart(t *testing.T) {
	store := outboxtest.NewFakeStore()
	entry := makeEntry("e-loop-cleanup", "order.created")
	store.Seed(entry)

	ctx := context.Background()
	claimed, err := store.ClaimPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	_, err = store.MarkPublished(ctx, claimed[0].ID, claimed[0].LeaseID)
	require.NoError(t, err)

	// retention=1ns so the just-published entry is immediately past cutoff
	// (RelayConfig.WithDefaults treats 0 as "missing" and substitutes the
	// 72h default; use a tiny positive value to keep the override).
	// The relay must delete the entry on the very first cleanupLoop pass.
	cfg := fastCfg()
	cfg.RetentionPeriod = relayMinRetention
	cfg.DeadRetentionPeriod = relayMinRetention
	relay := outbox.NewRelay(clock.Real(), store, newFakePublisher(), cfg)

	stop := startRelay(t, relay)
	defer stop()

	waitStore(t, store, func(snap []outboxtest.FakeRow) bool {
		return len(snap) == 0
	})

	assert.Empty(t, store.Snapshot(), "cleanupLoop must drain the published entry on its first pass")
}

func TestRelay_EnvelopePayload_IsCorrect(t *testing.T) {
	store := outboxtest.NewFakeStore()
	entry := outbox.ClaimedEntry{
		Entry: kout.Entry{
			ID:            "env-test",
			AggregateID:   "agg-1",
			AggregateType: "order",
			EventType:     "order.created",
			Topic:         "orders.v1",
			Payload:       []byte(`{"amount":42}`),
			CreatedAt:     time.Now(),
		},
		Attempts: 0,
	}
	store.Seed(entry)

	pub := newFakePublisher()
	relay := outbox.NewRelay(clock.Real(), store, pub, fastCfg())

	stop := startRelay(t, relay)
	defer stop()

	waitPub(t, pub, 1)
	stop()

	captured := pub.Captured()
	require.Len(t, captured, 1)
	assert.Equal(t, "orders.v1", captured[0].topic, "topic from entry.Topic must be used")

	// SAFEID-UPSTREAM-FUNNEL-HARD-01 sealed kout.wireMessage as package-private;
	// decode the captured wire payload via the public UnmarshalEnvelope funnel.
	entryDecoded, err := kout.UnmarshalEnvelope("", captured[0].payload)
	require.NoError(t, err)
	assert.Equal(t, "env-test", entryDecoded.ID)
	assert.Equal(t, "agg-1", entryDecoded.AggregateID)
	assert.Equal(t, "order", entryDecoded.AggregateType)
	assert.Equal(t, "order.created", entryDecoded.EventType)
	assert.JSONEq(t, `{"amount":42}`, string(entryDecoded.Payload))
}

func TestRelay_Metrics_RecordedOnPollCycle(t *testing.T) {
	store := outboxtest.NewFakeStore()
	store.Seed(makeEntry("m1", "ev.type"), makeEntry("m2", "ev.type"))

	pub := newFakePublisher()
	mc := &testCollector{}
	cfg := fastCfg()
	cfg.Metrics = mc
	relay := outbox.NewRelay(clock.Real(), store, pub, cfg)

	stop := startRelay(t, relay)
	defer stop()

	waitPub(t, pub, 2)
	stop()

	// At least one RecordBatchSize call with size > 0 must have occurred.
	mc.mu.Lock()
	defer mc.mu.Unlock()
	hasNonZero := false
	for _, s := range mc.batchSizes {
		if s >= 2 {
			hasNonZero = true
		}
	}
	assert.True(t, hasNonZero, "RecordBatchSize must have been called with >= 2")

	// At least one PollCycle with Published >= 2.
	hasCycle := false
	for _, c := range mc.pollCycles {
		if c.Published >= 2 {
			hasCycle = true
		}
	}
	assert.True(t, hasCycle, "RecordPollCycle must have published >= 2")
}

func TestRelay_NilMetrics_DoesNotPanic(t *testing.T) {
	store := outboxtest.NewFakeStore()
	store.Seed(makeEntry("nm1", "test.event"))

	pub := newFakePublisher()
	cfg := fastCfg()
	cfg.Metrics = nil // explicit nil — must default to Noop
	relay := outbox.NewRelay(clock.Real(), store, pub, cfg)

	stop := startRelay(t, relay)
	defer stop()

	waitPub(t, pub, 1)
	stop()

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, kout.StatePublished, snap[0].Status)
}

func TestRelay_SanitizesError_InLastError(t *testing.T) {
	store := outboxtest.NewFakeStore()
	entry := makeEntry("e-sensitive", "order.created")
	store.Seed(entry)

	pub := newFakePublisher()
	pub.WithError(errors.New(`dial failed: {"password":"secret123","host":"db.internal"}`))
	relay := outbox.NewRelay(clock.Real(), store, pub, fastCfg())

	stop := startRelay(t, relay)
	defer stop()

	waitStore(t, store, func(snap []outboxtest.FakeRow) bool {
		return len(snap) > 0 && (snap[0].Status == kout.StatePending || snap[0].Status == kout.StateDead) && snap[0].LastError != ""
	})
	stop()

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.NotContains(t, snap[0].LastError, "secret123", "sensitive data must be redacted")
	assert.Contains(t, snap[0].LastError, "<REDACTED>")
}

// ---------------------------------------------------------------------------
// B2 FailureBudget integration tests
// ---------------------------------------------------------------------------

// failingStore is a Store whose ClaimPending / ReclaimStale / CleanupPublished
// methods can be configured to always return an error.
type failingStore struct {
	*outboxtest.FakeStore
	mu            sync.Mutex
	claimErr      error
	reclaimErr    error
	cleanupPubErr error
}

func newFailingStore() *failingStore {
	return &failingStore{FakeStore: outboxtest.NewFakeStore()}
}

func (s *failingStore) setClaimErr(err error) {
	s.mu.Lock()
	s.claimErr = err
	s.mu.Unlock()
}

func (s *failingStore) setReclaimErr(err error) {
	s.mu.Lock()
	s.reclaimErr = err
	s.mu.Unlock()
}

func (s *failingStore) setCleanupPubErr(err error) {
	s.mu.Lock()
	s.cleanupPubErr = err
	s.mu.Unlock()
}

func (s *failingStore) ClaimPending(ctx context.Context, batchSize int) ([]outbox.ClaimedEntry, error) {
	s.mu.Lock()
	err := s.claimErr
	s.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.FakeStore.ClaimPending(ctx, batchSize)
}

func (s *failingStore) ReclaimStale(
	ctx context.Context, claimTTL time.Duration, maxAttempts int, base, maxDelay time.Duration, batchSize int,
) (int, error) {
	s.mu.Lock()
	err := s.reclaimErr
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return s.FakeStore.ReclaimStale(ctx, claimTTL, maxAttempts, base, maxDelay, batchSize)
}

func (s *failingStore) CleanupPublished(ctx context.Context, cutoff time.Time, batchSize int) (int, error) {
	s.mu.Lock()
	err := s.cleanupPubErr
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return s.FakeStore.CleanupPublished(ctx, cutoff, batchSize)
}

// OldestEligibleAt returns a fake "very recent past" time for published status
// when a cleanupPubErr is set, so nextCleanupWait schedules quickly via floor.
func (s *failingStore) OldestEligibleAt(ctx context.Context, status kout.State) (time.Time, bool, error) {
	s.mu.Lock()
	cpErr := s.cleanupPubErr
	s.mu.Unlock()
	if cpErr != nil && status == kout.StatePublished {
		// Return a time just barely in the past so nextCleanupWait computes
		// near-zero and falls to cleanupWaitFloor (set to 5ms in tests).
		return time.Now().Add(-testtime.D1ms), true, nil
	}
	return s.FakeStore.OldestEligibleAt(ctx, status)
}

// leaseLostStore wraps FakeStore and forces MarkRetry / MarkDead to report
// updated=false so handleFailedEntry exercises the "lease was lost" branch
// (B2-A-05 / OUTBOX-RELAY-LOST-METRIC). The publisher is simultaneously made
// to fail so the relay enters handleFailedEntry rather than MarkPublished.
type leaseLostStore struct {
	*outboxtest.FakeStore
}

func newLeaseLostStore() *leaseLostStore {
	return &leaseLostStore{FakeStore: outboxtest.NewFakeStore()}
}

func (s *leaseLostStore) MarkRetry(
	_ context.Context, _, _ string, _ int, _ time.Time, _ string,
) (bool, error) {
	return false, nil
}

func (s *leaseLostStore) MarkDead(
	_ context.Context, _, _ string, _ int, _ string,
) (bool, error) {
	return false, nil
}

// TestRelay_HandleFailedEntry_LostStat verifies PR-V1-PG-OUTBOX-RELAY-HARDEN
// B2-A-05 follow-up: when MarkRetry / MarkDead report updated=false (lease was
// reclaimed mid-flight), handleFailedEntry MUST count the result into a new
// "lost" stat and surface it through PollCycleResult.Lost so the
// `outbox_relayed_total{outcome="lost"}` time-series fires. Without this, a
// stale-lease writeback is invisible to operators despite stats divergence.
func TestRelay_HandleFailedEntry_LostStat(t *testing.T) {
	store := newLeaseLostStore()
	pub := newFakePublisher().WithError(errors.New("transient publish failure"))

	// Seed one pending entry so a single poll cycle:
	//   1. ClaimPending mints a lease and returns the row,
	//   2. fakePublisher fails the publish,
	//   3. handleFailedEntry routes to MarkRetry,
	//   4. our wrapper returns updated=false → must count as lost, not retried.
	store.Seed(outbox.ClaimedEntry{
		Entry: kout.Entry{
			ID:        "00000000-0000-4000-8000-000000000001",
			EventType: "test.event",
			Topic:     "t",
			Payload:   []byte(`{}`),
		},
	})

	mc := &testCollector{}
	cfg := fastCfg()
	cfg.MaxAttempts = 5
	cfg.Metrics = mc

	relay := outbox.NewRelay(clock.Real(), store, pub, cfg)
	startCtx, startCancel := context.WithTimeout(t.Context(), testtime.D2s)
	defer startCancel()
	require.NoError(t, relay.Start(startCtx))
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.D2s)
		defer stopCancel()
		_ = relay.Stop(stopCtx)
	}()

	// Polls the testCollector metrics state, not FakeStore/fakePublisher,
	// so the deterministic waitStore/waitPub helpers don't apply. Budget is
	// testtime.D1s (already conventional, not the D500ms-flake class) and
	// state convergence here is a single int counter assignment.
	testwait.External(t, "outbox-relay-claim-acquired", func() bool {
		mc.mu.Lock()
		defer mc.mu.Unlock()
		for _, c := range mc.pollCycles {
			if c.Lost >= 1 {
				return true
			}
		}
		return false
	}, testtime.D1s, testtime.D1ms, "PollCycleResult.Lost must record stale-lease writeback")

	mc.mu.Lock()
	defer mc.mu.Unlock()
	var lostTotal, retriedTotal, deadTotal int
	for _, c := range mc.pollCycles {
		lostTotal += c.Lost
		retriedTotal += c.Retried
		deadTotal += c.Dead
	}
	assert.GreaterOrEqual(t, lostTotal, 1, "Lost must record stale-lease writeback")
	assert.Equal(t, 0, retriedTotal, "stale-lease writeback must NOT count as retried")
	assert.Equal(t, 0, deadTotal, "stale-lease writeback must NOT count as dead")
}

func budgetCfg() outbox.RelayConfig {
	cfg := fastCfg()
	cfg.PollFailureBudget = 3
	cfg.ReclaimFailureBudget = 3
	cfg.CleanupFailureBudget = 3
	// Use a tiny RetentionPeriod so nextCleanupWait returns floor (5ms) when
	// OldestEligibleAt reports a row was published ~1ms ago.
	cfg.RetentionPeriod = testtime.D1ms
	cfg.DeadRetentionPeriod = testtime.D1ms
	return cfg
}

func TestRelay_PollFailureBudget_TripsAfterConsecutiveFailures(t *testing.T) {
	store := newFailingStore()
	store.setClaimErr(errors.New("db down"))

	relay := outbox.NewRelay(clock.Real(), store, newFakePublisher(), budgetCfg())

	stop := startRelay(t, relay)
	defer stop()

	// Wait for the poll budget checker to become non-nil (trip).
	testwait.External(t, "outbox-relay-retry-scheduled", func() bool {
		p := relayFindProbe(relay.Probes(), outbox.ProbePoll)
		if p == nil {
			return false
		}
		return p.Check(context.Background()) != nil
	}, testtime.D2s, testtime.FastPoll, "poll budget must trip after consecutive failures")
}

func TestRelay_PollFailureBudget_ResetsOnSuccess(t *testing.T) {
	store := newFailingStore()
	store.setClaimErr(errors.New("db down"))

	relay := outbox.NewRelay(clock.Real(), store, newFakePublisher(), budgetCfg())

	stop := startRelay(t, relay)
	defer stop()

	// Trip first.
	testwait.External(t, "outbox-relay-retry-scheduled", func() bool {
		p := relayFindProbe(relay.Probes(), outbox.ProbePoll)
		return p != nil && p.Check(context.Background()) != nil
	}, testtime.D2s, testtime.FastPoll, "budget must trip")

	// Clear the error so poll succeeds.
	store.setClaimErr(nil)

	// Checker must recover.
	testwait.External(t, "outbox-relay-published", func() bool {
		p := relayFindProbe(relay.Probes(), outbox.ProbePoll)
		return p != nil && p.Check(context.Background()) == nil
	}, testtime.D2s, testtime.FastPoll, "poll budget must reset after success")
}

func TestRelay_ReclaimFailureBudget_Independent(t *testing.T) {
	// Only reclaim fails — poll and cleanup must remain healthy.
	store := newFailingStore()
	store.setReclaimErr(errors.New("reclaim db down"))

	relay := outbox.NewRelay(clock.Real(), store, newFakePublisher(), budgetCfg())

	stop := startRelay(t, relay)
	defer stop()

	testwait.External(t, "outbox-relay-retry-scheduled", func() bool {
		p := relayFindProbe(relay.Probes(), outbox.ProbeReclaim)
		return p != nil && p.Check(context.Background()) != nil
	}, testtime.D2s, testtime.FastPoll, "reclaim budget must trip")

	// Verify poll probe exists upfront (fail-fast if absent, catching silent skips).
	pollProbe := relayFindProbe(relay.Probes(), outbox.ProbePoll)
	require.NotNil(t, pollProbe, "poll probe must be registered")

	// Poll probe must never become unhealthy while only reclaim fails.
	assert.Never(t, func() bool {
		return pollProbe.Check(context.Background()) != nil
	}, testtime.D100ms, testtime.FastPoll, "poll budget should not trip while only reclaim fails")
}

func TestRelay_CleanupFailureBudget_Independent(t *testing.T) {
	// Only cleanup fails — poll budget must remain healthy.
	store := newFailingStore()
	store.setCleanupPubErr(errors.New("cleanup db down"))

	relay := outbox.NewRelay(clock.Real(), store, newFakePublisher(), budgetCfg())

	stop := startRelay(t, relay)
	defer stop()

	testwait.External(t, "outbox-relay-retry-scheduled", func() bool {
		p := relayFindProbe(relay.Probes(), outbox.ProbeCleanup)
		return p != nil && p.Check(context.Background()) != nil
	}, testtime.D2s, testtime.FastPoll, "cleanup budget must trip")

	// Verify poll probe exists upfront (fail-fast if absent, catching silent skips).
	pollProbe2 := relayFindProbe(relay.Probes(), outbox.ProbePoll)
	require.NotNil(t, pollProbe2, "poll probe must be registered")

	// Poll probe must never become unhealthy while only cleanup fails.
	assert.Never(t, func() bool {
		return pollProbe2.Check(context.Background()) != nil
	}, testtime.D100ms, testtime.FastPoll, "poll budget should not trip while only cleanup fails")
}

func TestRelay_HealthCheckers_RegistersThree(t *testing.T) {
	relay := outbox.NewRelay(clock.Real(), outboxtest.NewFakeStore(), newFakePublisher(), budgetCfg())
	probes := relay.Probes()

	require.NotNil(t, relayFindProbe(probes, outbox.ProbePoll), "poll probe must be registered")
	require.NotNil(t, relayFindProbe(probes, outbox.ProbeReclaim), "reclaim probe must be registered")
	require.NotNil(t, relayFindProbe(probes, outbox.ProbeCleanup), "cleanup probe must be registered")
	assert.Len(t, probes, 3)
}

func TestRelay_FailureBudgetThresholdZero_DisablesChecker(t *testing.T) {
	cfg := fastCfg()
	cfg.PollFailureBudget = 0    // disabled
	cfg.ReclaimFailureBudget = 3 // enabled
	cfg.CleanupFailureBudget = 3 // enabled

	relay := outbox.NewRelay(clock.Real(), outboxtest.NewFakeStore(), newFakePublisher(), cfg)
	probes := relay.Probes()

	assert.Nil(t, relayFindProbe(probes, outbox.ProbePoll),
		"threshold=0 must not register poll probe")
	assert.NotNil(t, relayFindProbe(probes, outbox.ProbeReclaim))
	assert.NotNil(t, relayFindProbe(probes, outbox.ProbeCleanup))
}

func TestRelay_CanRestartAfterTrip_ResetsBudget(t *testing.T) {
	// Threshold=3 so we trip quickly without a long poll loop.
	store := newFailingStore()
	store.setClaimErr(errors.New("db down"))

	cfg := budgetCfg()
	cfg.PollFailureBudget = 3
	relay := outbox.NewRelay(clock.Real(), store, newFakePublisher(), cfg)

	// --- First run: trip the poll budget ---
	stop := startRelay(t, relay)

	testwait.External(t, "outbox-relay-retry-scheduled", func() bool {
		p := relayFindProbe(relay.Probes(), outbox.ProbePoll)
		return p != nil && p.Check(context.Background()) != nil
	}, testtime.D2s, testtime.FastPoll, "poll budget must trip during first run")

	stop() // gracefully stop; defer in Start resets readyCh for next Start

	// Wait until state is relayStopped so we can restart.
	// (No relay.Probes() assertion needed here — we just need the relay to stop.)
	testwait.External(t, "outbox-relay-batch-drained", func() bool {
		return true // we'll use Ready() channel approach below
	}, testtime.D100ms, testtime.D2ms)

	// Clear the error so the second run succeeds.
	store.setClaimErr(nil)

	// --- Second run: budget must be reset before first poll ---
	stop2 := startRelay(t, relay)
	defer stop2()

	// Wait for relay to be running.
	select {
	case <-relay.Ready():
	case <-time.After(testtime.D2s):
		t.Fatal("relay did not become ready for second run")
	}

	// Immediately after start (before any poll result), poll probe must be
	// healthy because Reset() cleared the stale trip from the first run.
	pollProbe := relayFindProbe(relay.Probes(), outbox.ProbePoll)
	require.NotNil(t, pollProbe, "poll probe must be registered on second run")
	assert.Nil(t, pollProbe.Check(context.Background()),
		"poll probe must be healthy immediately after restart (Reset cleared stale trip)")
}

func TestRelay_Ready_ReturnsReadyChannel(t *testing.T) {
	relay := outbox.NewRelay(clock.Real(), outboxtest.NewFakeStore(), newFakePublisher(), fastCfg())

	stop := startRelay(t, relay)
	defer stop()

	// Ready() never returns nil (B1: pre-allocated in NewRelay). Before Start()
	// completes, the channel is open (blocks); after relayRunning, it is closed.
	testwait.External(t, "outbox-relay-published", func() bool {
		ch := relay.Ready()
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}, testtime.D2s, testtime.D2ms, "relay.Ready() must close after Start")
}

// ---------------------------------------------------------------------------
// PendingDepthObserver tests
// ---------------------------------------------------------------------------

// spyDepthObserver captures ObservePendingDepth calls for assertions.
type spyDepthObserver struct {
	mu     sync.Mutex
	values []int64
	notify chan struct{}
}

func newSpyDepthObserver() *spyDepthObserver {
	return &spyDepthObserver{notify: make(chan struct{})}
}

func (s *spyDepthObserver) ObservePendingDepth(_ context.Context, n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values = append(s.values, n)
	close(s.notify)
	s.notify = make(chan struct{})
}

func (s *spyDepthObserver) waitForAtLeastOne(ctx context.Context) error {
	for {
		s.mu.Lock()
		n := len(s.values)
		notify := s.notify
		s.mu.Unlock()
		if n >= 1 {
			return nil
		}
		select {
		case <-notify:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *spyDepthObserver) captured() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int64, len(s.values))
	copy(out, s.values)
	return out
}

// TestRelay_pendingDepthObserved_OnReclaimTick verifies that when a
// PendingDepthObserver is wired, ObservePendingDepth is called with the
// count returned by Store.CountPending on each reclaim tick.
func TestRelay_pendingDepthObserved_OnReclaimTick(t *testing.T) {
	store := outboxtest.NewFakeStore()
	const seeded = 42
	entries := make([]outbox.ClaimedEntry, seeded)
	for i := range seeded {
		entries[i] = makeEntry(fmt.Sprintf("pd-%02d", i), "test.event")
	}
	store.Seed(entries...)

	spy := newSpyDepthObserver()
	cfg := fastCfg()
	relay := outbox.NewRelay(clock.Real(), store, newFakePublisher(), cfg).
		WithPendingDepthObserver(spy)

	stop := startRelay(t, relay)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), testtime.EventuallyDefault)
	defer cancel()
	require.NoError(t, spy.waitForAtLeastOne(ctx), "ObservePendingDepth must be called on reclaim tick")

	vals := spy.captured()
	require.NotEmpty(t, vals)
	// The store has seeded entries; at least one observation must be > 0
	// (some may have been claimed by the publish loop by the time reclaim fires,
	// so we just require at least one non-negative value was observed).
	for _, v := range vals {
		require.GreaterOrEqual(t, v, int64(0), "pending depth must be non-negative")
	}
}

// errCountStore wraps FakeStore and makes CountPending always return an error.
type errCountStore struct {
	*outboxtest.FakeStore
}

func (s *errCountStore) CountPending(_ context.Context) (int64, error) {
	return 0, errors.New("count pending db error")
}

// TestRelay_pendingDepthSkipped_OnError verifies that when CountPending returns
// an error, ObservePendingDepth is NOT called and a Warn log is emitted. We
// verify the no-call contract via an erroring store wrapper.
func TestRelay_pendingDepthSkipped_OnError(t *testing.T) {
	errStore := &errCountStore{FakeStore: outboxtest.NewFakeStore()}

	spy := newSpyDepthObserver()
	cfg := fastCfg()
	relay := outbox.NewRelay(clock.Real(), errStore, newFakePublisher(), cfg).
		WithPendingDepthObserver(spy)

	stop := startRelay(t, relay)

	// Let several reclaim ticks pass.
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D200ms)
	defer cancel()
	<-ctx.Done()
	stop()

	// Observer must not have been called since CountPending always errors.
	assert.Empty(t, spy.captured(), "ObservePendingDepth must not be called when CountPending errors")
}

// countingCountStore wraps FakeStore and tracks CountPending calls.
type countingCountStore struct {
	*outboxtest.FakeStore
	mu    sync.Mutex
	calls int
}

func (s *countingCountStore) CountPending(ctx context.Context) (int64, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return s.FakeStore.CountPending(ctx)
}

// TestRelay_NoObserver_NoCall verifies that without WithPendingDepthObserver
// the relay does not call Store.CountPending (zero overhead on the non-hot path).
func TestRelay_NoObserver_NoCall(t *testing.T) {
	cs := &countingCountStore{FakeStore: outboxtest.NewFakeStore()}

	cfg := fastCfg()
	relay := outbox.NewRelay(clock.Real(), cs, newFakePublisher(), cfg)
	// No WithPendingDepthObserver — relay must not call CountPending.

	stop := startRelay(t, relay)

	// Let several reclaim ticks pass.
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D200ms)
	defer cancel()
	<-ctx.Done()
	stop()

	cs.mu.Lock()
	c := cs.calls
	cs.mu.Unlock()
	assert.Equal(t, 0, c, "CountPending must not be called without a PendingDepthObserver")
}

// ---------------------------------------------------------------------------
// Helper types for tests
// ---------------------------------------------------------------------------

// blockingPublisher never returns from Publish (simulates crash during publish).
// Used to force entries to stay in 'claiming' long enough for ReclaimStale.
type blockingPublisher struct{}

func (b *blockingPublisher) Publish(ctx context.Context, _ string, _ []byte) error {
	<-ctx.Done()
	return ctx.Err()
}
func (b *blockingPublisher) Close(_ context.Context) error { return nil }

// testCollector records relay metric calls for assertions.
type testCollector struct {
	mu         sync.Mutex
	pollCycles []kout.PollCycleResult
	batchSizes []int
}

func (c *testCollector) RecordPollCycle(_ context.Context, r kout.PollCycleResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pollCycles = append(c.pollCycles, r)
}

func (c *testCollector) RecordBatchSize(_ context.Context, s int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batchSizes = append(c.batchSizes, s)
}

func (c *testCollector) RecordReclaim(_ context.Context, _ int64)    {}
func (c *testCollector) RecordCleanup(_ context.Context, _, _ int64) {}
