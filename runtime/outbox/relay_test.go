package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/outbox/outboxtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
}

func newFakePublisher() *fakePublisher { return &fakePublisher{} }

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
	return nil
}

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
		PollInterval:        5 * time.Millisecond,
		ReclaimInterval:     10 * time.Millisecond,
		BatchSize:           10,
		MaxAttempts:         3,
		BaseRetryDelay:      1 * time.Millisecond,
		MaxRetryDelay:       10 * time.Millisecond,
		ClaimTTL:            100 * time.Millisecond,
		RetentionPeriod:     1 * time.Hour,
		DeadRetentionPeriod: 24 * time.Hour,
		CleanupWaitFloor:    5 * time.Millisecond,
	}
}

// startRelay starts relay in a goroutine and returns the errCh + a stop function.
func startRelay(t *testing.T, relay *outbox.Relay) (errCh chan error, stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan error, 1)
	go func() { ch <- relay.Start(ctx) }()
	return ch, func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		require.NoError(t, relay.Stop(stopCtx))
		cancel()
	}
}

// waitUntil polls cond until it returns true or timeout is exceeded.
func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRelay_HappyPath_ClaimPublishMarkPublished(t *testing.T) {
	store := outboxtest.NewFakeStore()
	store.Seed(makeEntry("e1", "order.created"), makeEntry("e2", "order.updated"), makeEntry("e3", "order.deleted"))

	pub := newFakePublisher()
	relay := outbox.NewRelay(store, pub, fastCfg())

	_, stop := startRelay(t, relay)
	defer stop()

	// Wait until all 3 entries are published.
	waitUntil(t, 500*time.Millisecond, func() bool {
		return len(pub.Captured()) >= 3
	})
	stop()

	// All store rows must be published.
	snap := store.Snapshot()
	require.Len(t, snap, 3)
	for _, row := range snap {
		assert.Equal(t, "published", row.Status, "entry %s should be published", row.Entry.ID)
	}

	// Verify wire envelope contains correct payload.
	captured := pub.Captured()
	require.Len(t, captured, 3)
	var msg outbox.WireMessage
	require.NoError(t, json.Unmarshal(captured[0].payload, &msg))
	assert.NotEmpty(t, msg.ID)
	assert.NotEmpty(t, msg.EventType)
	assert.True(t, len(msg.Payload) > 0)
}

func TestRelay_TransientFailure_MarkRetryWithBackoff(t *testing.T) {
	store := outboxtest.NewFakeStore()
	entry := makeEntry("e-fail", "order.created")
	store.Seed(entry)

	pub := newFakePublisher().WithFailN(1) // first publish fails
	relay := outbox.NewRelay(store, pub, fastCfg())

	_, stop := startRelay(t, relay)
	defer stop()

	// Wait until the entry is retried (status=pending with attempts>0).
	waitUntil(t, 500*time.Millisecond, func() bool {
		snap := store.Snapshot()
		if len(snap) == 0 {
			return false
		}
		return snap[0].Status == "pending" && snap[0].Attempts == 1
	})
	stop()

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "pending", snap[0].Status)
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
	relay := outbox.NewRelay(store, pub, fastCfg())

	_, stop := startRelay(t, relay)
	defer stop()

	waitUntil(t, 500*time.Millisecond, func() bool {
		snap := store.Snapshot()
		return len(snap) > 0 && snap[0].Status == "dead"
	})
	stop()

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "dead", snap[0].Status)
	assert.Equal(t, 3, snap[0].Attempts)
	assert.Contains(t, snap[0].LastError, "permanent broker failure")
	assert.NotNil(t, snap[0].DeadAt)
}

func TestRelay_Shutdown_CleanStop(t *testing.T) {
	store := outboxtest.NewFakeStore()
	pub := newFakePublisher()
	relay := outbox.NewRelay(store, pub, fastCfg())

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- relay.Start(ctx) }()

	// Wait for relay to reach running state via Ready() instead of time.Sleep.
	require.Eventually(t, func() bool {
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
	}, time.Second, time.Millisecond, "relay not ready")

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()

	err := relay.Stop(stopCtx)
	require.NoError(t, err)
	cancel()

	startErr := <-errCh
	assert.NoError(t, startErr, "Start should return nil on graceful stop")
}

func TestRelay_StopBeforeStart_IsNoop(t *testing.T) {
	store := outboxtest.NewFakeStore()
	relay := outbox.NewRelay(store, newFakePublisher(), fastCfg())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := relay.Stop(ctx)
	assert.NoError(t, err, "Stop on never-started relay must be a no-op")
}

func TestRelay_DoubleStart_Error(t *testing.T) {
	store := outboxtest.NewFakeStore()
	relay := outbox.NewRelay(store, newFakePublisher(), fastCfg())

	go func() { _ = relay.Start(t.Context()) }()
	// Wait for relay to reach running state.
	require.Eventually(t, func() bool {
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
	}, time.Second, time.Millisecond, "relay not ready for DoubleStart test")

	err := relay.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestRelay_CanRestartAfterStop(t *testing.T) {
	store := outboxtest.NewFakeStore()
	relay := outbox.NewRelay(store, newFakePublisher(), fastCfg())

	for i := range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() { errCh <- relay.Start(ctx) }()

		// Wait for relay to be ready before stopping.
		require.Eventually(t, func() bool {
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
		}, time.Second, time.Millisecond, "relay not ready in iteration %d", i)

		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
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
	cfg.ClaimTTL = 5 * time.Millisecond
	cfg.ReclaimInterval = 5 * time.Millisecond

	// Publisher that blocks indefinitely (simulates crash during publish).
	blockPub := &blockingPublisher{}
	relay := outbox.NewRelay(store, blockPub, cfg)

	_, stop := startRelay(t, relay)
	defer stop()

	// Wait until the entry is reclaimed (back to pending with attempts > 0).
	waitUntil(t, 500*time.Millisecond, func() bool {
		snap := store.Snapshot()
		if len(snap) == 0 {
			return false
		}
		return snap[0].Status == "pending" && snap[0].Attempts > 0
	})
	stop()

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "pending", snap[0].Status)
	assert.Greater(t, snap[0].Attempts, 0)
}

// TestRelay_StoreCleanup_DirectCall verifies that Store.CleanupPublished deletes
// a published entry when called directly. The relay cleanupLoop fires at
// max(PollInterval*10, 10s) which exceeds the unit-test time budget; the loop
// tick behaviour is covered by TestRelay_CleanupLoop_ActuallyRunsPeriodically.
func TestRelay_StoreCleanup_DirectCall(t *testing.T) {
	store := outboxtest.NewFakeStore()

	entry := makeEntry("e-cleanup", "order.created")
	store.Seed(entry)

	// Publish it by hand.
	ctx := context.Background()
	claimed, err := store.ClaimPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	_, err = store.MarkPublished(ctx, claimed[0].ID)
	require.NoError(t, err)

	// Verify it is published.
	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "published", snap[0].Status)

	// Manually invoke CleanupPublished with a future cutoff.
	deleted, err := store.CleanupPublished(ctx, time.Now().Add(time.Hour), 1000)
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)

	snap = store.Snapshot()
	assert.Empty(t, snap, "published entry must be deleted by cleanup")
}

// TestRelay_CleanupLoop_RunsImmediatelyAtStart verifies that the data-driven
// cleanupLoop runs cleanup() on the very first iteration (before the first
// sleep), so a relay starting against a backlog of expired rows drains them
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
	_, err = store.MarkPublished(ctx, claimed[0].ID)
	require.NoError(t, err)

	// retention=1ns so the just-published entry is immediately past cutoff
	// (RelayConfig.WithDefaults treats 0 as "missing" and substitutes the
	// 72h default; use a tiny positive value to keep the override).
	// The relay must delete the entry on the very first cleanupLoop pass.
	cfg := fastCfg()
	cfg.RetentionPeriod = 1 * time.Nanosecond
	cfg.DeadRetentionPeriod = 1 * time.Nanosecond
	relay := outbox.NewRelay(store, newFakePublisher(), cfg)

	_, stop := startRelay(t, relay)
	defer stop()

	waitUntil(t, 500*time.Millisecond, func() bool {
		return len(store.Snapshot()) == 0
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
	relay := outbox.NewRelay(store, pub, fastCfg())

	_, stop := startRelay(t, relay)
	defer stop()

	waitUntil(t, 500*time.Millisecond, func() bool {
		return len(pub.Captured()) >= 1
	})
	stop()

	captured := pub.Captured()
	require.Len(t, captured, 1)
	assert.Equal(t, "orders.v1", captured[0].topic, "topic from entry.Topic must be used")

	var msg outbox.WireMessage
	require.NoError(t, json.Unmarshal(captured[0].payload, &msg))
	assert.Equal(t, "env-test", msg.ID)
	assert.Equal(t, "agg-1", msg.AggregateID)
	assert.Equal(t, "order", msg.AggregateType)
	assert.Equal(t, "order.created", msg.EventType)
	assert.JSONEq(t, `{"amount":42}`, string(msg.Payload))
}

func TestRelay_Metrics_RecordedOnPollCycle(t *testing.T) {
	store := outboxtest.NewFakeStore()
	store.Seed(makeEntry("m1", "ev.type"), makeEntry("m2", "ev.type"))

	pub := newFakePublisher()
	mc := &testCollector{}
	cfg := fastCfg()
	cfg.Metrics = mc
	relay := outbox.NewRelay(store, pub, cfg)

	_, stop := startRelay(t, relay)
	defer stop()

	waitUntil(t, 500*time.Millisecond, func() bool {
		return len(pub.Captured()) >= 2
	})
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
	relay := outbox.NewRelay(store, pub, cfg)

	_, stop := startRelay(t, relay)
	defer stop()

	waitUntil(t, 500*time.Millisecond, func() bool {
		return len(pub.Captured()) >= 1
	})
	stop()

	snap := store.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "published", snap[0].Status)
}

func TestRelay_SanitizesError_InLastError(t *testing.T) {
	store := outboxtest.NewFakeStore()
	entry := makeEntry("e-sensitive", "order.created")
	store.Seed(entry)

	pub := newFakePublisher()
	pub.WithError(errors.New("dial failed: password=secret123 host=db.internal"))
	relay := outbox.NewRelay(store, pub, fastCfg())

	_, stop := startRelay(t, relay)
	defer stop()

	waitUntil(t, 500*time.Millisecond, func() bool {
		snap := store.Snapshot()
		return len(snap) > 0 && (snap[0].Status == "pending" || snap[0].Status == "dead") && snap[0].LastError != ""
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

func (s *failingStore) ReclaimStale(ctx context.Context, claimTTL time.Duration, maxAttempts int, base, maxDelay time.Duration) (int, error) {
	s.mu.Lock()
	err := s.reclaimErr
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return s.FakeStore.ReclaimStale(ctx, claimTTL, maxAttempts, base, maxDelay)
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
func (s *failingStore) OldestEligibleAt(ctx context.Context, status string) (time.Time, bool, error) {
	s.mu.Lock()
	cpErr := s.cleanupPubErr
	s.mu.Unlock()
	if cpErr != nil && status == "published" {
		// Return a time just barely in the past so nextCleanupWait computes
		// near-zero and falls to cleanupWaitFloor (set to 5ms in tests).
		return time.Now().Add(-time.Millisecond), true, nil
	}
	return s.FakeStore.OldestEligibleAt(ctx, status)
}

func budgetCfg() outbox.RelayConfig {
	cfg := fastCfg()
	cfg.PollFailureBudget = 3
	cfg.ReclaimFailureBudget = 3
	cfg.CleanupFailureBudget = 3
	// Use a tiny RetentionPeriod so nextCleanupWait returns floor (5ms) when
	// OldestEligibleAt reports a row was published ~1ms ago.
	cfg.RetentionPeriod = time.Millisecond
	cfg.DeadRetentionPeriod = time.Millisecond
	return cfg
}

func TestRelay_PollFailureBudget_TripsAfterConsecutiveFailures(t *testing.T) {
	store := newFailingStore()
	store.setClaimErr(errors.New("db down"))

	relay := outbox.NewRelay(store, newFakePublisher(), budgetCfg())

	_, stop := startRelay(t, relay)
	defer stop()

	// Wait for the poll budget checker to become non-nil (trip).
	require.Eventually(t, func() bool {
		checkers := relay.HealthCheckers()
		fn, ok := checkers["outbox-relay-poll"]
		if !ok {
			return false
		}
		return fn() != nil
	}, 2*time.Second, 5*time.Millisecond, "poll budget must trip after consecutive failures")
}

func TestRelay_PollFailureBudget_ResetsOnSuccess(t *testing.T) {
	store := newFailingStore()
	store.setClaimErr(errors.New("db down"))

	relay := outbox.NewRelay(store, newFakePublisher(), budgetCfg())

	_, stop := startRelay(t, relay)
	defer stop()

	// Trip first.
	require.Eventually(t, func() bool {
		checkers := relay.HealthCheckers()
		fn, ok := checkers["outbox-relay-poll"]
		return ok && fn() != nil
	}, 2*time.Second, 5*time.Millisecond, "budget must trip")

	// Clear the error so poll succeeds.
	store.setClaimErr(nil)

	// Checker must recover.
	require.Eventually(t, func() bool {
		checkers := relay.HealthCheckers()
		fn, ok := checkers["outbox-relay-poll"]
		return ok && fn() == nil
	}, 2*time.Second, 5*time.Millisecond, "poll budget must reset after success")
}

func TestRelay_ReclaimFailureBudget_Independent(t *testing.T) {
	// Only reclaim fails — poll and cleanup must remain healthy.
	store := newFailingStore()
	store.setReclaimErr(errors.New("reclaim db down"))

	relay := outbox.NewRelay(store, newFakePublisher(), budgetCfg())

	_, stop := startRelay(t, relay)
	defer stop()

	require.Eventually(t, func() bool {
		checkers := relay.HealthCheckers()
		fn, ok := checkers["outbox-relay-reclaim"]
		return ok && fn() != nil
	}, 2*time.Second, 5*time.Millisecond, "reclaim budget must trip")

	// Poll checker must never become unhealthy while only reclaim fails.
	assert.Never(t, func() bool {
		checkers := relay.HealthCheckers()
		if fn, ok := checkers["outbox-relay-poll"]; ok {
			return fn() != nil
		}
		return false
	}, 100*time.Millisecond, 5*time.Millisecond, "poll budget should not trip while only reclaim fails")
}

func TestRelay_CleanupFailureBudget_Independent(t *testing.T) {
	// Only cleanup fails — poll budget must remain healthy.
	store := newFailingStore()
	store.setCleanupPubErr(errors.New("cleanup db down"))

	relay := outbox.NewRelay(store, newFakePublisher(), budgetCfg())

	_, stop := startRelay(t, relay)
	defer stop()

	require.Eventually(t, func() bool {
		checkers := relay.HealthCheckers()
		fn, ok := checkers["outbox-relay-cleanup"]
		return ok && fn() != nil
	}, 2*time.Second, 5*time.Millisecond, "cleanup budget must trip")

	// Poll checker must never become unhealthy while only cleanup fails.
	assert.Never(t, func() bool {
		checkers := relay.HealthCheckers()
		if fn, ok := checkers["outbox-relay-poll"]; ok {
			return fn() != nil
		}
		return false
	}, 100*time.Millisecond, 5*time.Millisecond, "poll budget should not trip while only cleanup fails")
}

func TestRelay_HealthCheckers_RegistersThree(t *testing.T) {
	relay := outbox.NewRelay(outboxtest.NewFakeStore(), newFakePublisher(), budgetCfg())
	checkers := relay.HealthCheckers()

	require.Contains(t, checkers, "outbox-relay-poll", "poll checker must be registered")
	require.Contains(t, checkers, "outbox-relay-reclaim", "reclaim checker must be registered")
	require.Contains(t, checkers, "outbox-relay-cleanup", "cleanup checker must be registered")
	assert.Len(t, checkers, 3)
}

func TestRelay_FailureBudgetThresholdZero_DisablesChecker(t *testing.T) {
	cfg := fastCfg()
	cfg.PollFailureBudget = 0    // disabled
	cfg.ReclaimFailureBudget = 3 // enabled
	cfg.CleanupFailureBudget = 3 // enabled

	relay := outbox.NewRelay(outboxtest.NewFakeStore(), newFakePublisher(), cfg)
	checkers := relay.HealthCheckers()

	assert.NotContains(t, checkers, "outbox-relay-poll",
		"threshold=0 must not register poll checker")
	assert.Contains(t, checkers, "outbox-relay-reclaim")
	assert.Contains(t, checkers, "outbox-relay-cleanup")
}

func TestRelay_CanRestartAfterTrip_ResetsBudget(t *testing.T) {
	// Threshold=3 so we trip quickly without a long poll loop.
	store := newFailingStore()
	store.setClaimErr(errors.New("db down"))

	cfg := budgetCfg()
	cfg.PollFailureBudget = 3
	relay := outbox.NewRelay(store, newFakePublisher(), cfg)

	// --- First run: trip the poll budget ---
	_, stop := startRelay(t, relay)

	require.Eventually(t, func() bool {
		checkers := relay.HealthCheckers()
		fn, ok := checkers["outbox-relay-poll"]
		return ok && fn() != nil
	}, 2*time.Second, 5*time.Millisecond, "poll budget must trip during first run")

	stop() // gracefully stop; defer in Start resets readyCh for next Start

	// Wait until state is relayStopped so we can restart.
	require.Eventually(t, func() bool {
		checkers := relay.HealthCheckers()
		fn, ok := checkers["outbox-relay-poll"]
		// The checker still exists; it reflects state at the time of the last run.
		// We just need the relay to have fully stopped.
		_ = fn
		_ = ok
		return true // we'll use Ready() channel approach below
	}, 100*time.Millisecond, 2*time.Millisecond)

	// Clear the error so the second run succeeds.
	store.setClaimErr(nil)

	// --- Second run: budget must be reset before first poll ---
	_, stop2 := startRelay(t, relay)
	defer stop2()

	// Wait for relay to be running.
	select {
	case <-relay.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not become ready for second run")
	}

	// Immediately after start (before any poll result), poll checker must be
	// healthy because Reset() cleared the stale trip from the first run.
	checkers := relay.HealthCheckers()
	require.Contains(t, checkers, "outbox-relay-poll", "poll checker must be registered on second run")
	assert.Nil(t, checkers["outbox-relay-poll"](), "poll checker must be healthy immediately after restart (Reset cleared stale trip)")
}

func TestRelay_Ready_ReturnsReadyChannel(t *testing.T) {
	relay := outbox.NewRelay(outboxtest.NewFakeStore(), newFakePublisher(), fastCfg())

	_, stop := startRelay(t, relay)
	defer stop()

	// Ready() never returns nil (B1: pre-allocated in NewRelay). Before Start()
	// completes, the channel is open (blocks); after relayRunning, it is closed.
	require.Eventually(t, func() bool {
		ch := relay.Ready()
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}, 2*time.Second, 2*time.Millisecond, "relay.Ready() must close after Start")
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

// testCollector records relay metric calls for assertions.
type testCollector struct {
	mu         sync.Mutex
	pollCycles []kout.PollCycleResult
	batchSizes []int
}

func (c *testCollector) RecordPollCycle(r kout.PollCycleResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pollCycles = append(c.pollCycles, r)
}

func (c *testCollector) RecordBatchSize(s int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batchSizes = append(c.batchSizes, s)
}

func (c *testCollector) RecordReclaim(_ int64)    {}
func (c *testCollector) RecordCleanup(_, _ int64) {}
