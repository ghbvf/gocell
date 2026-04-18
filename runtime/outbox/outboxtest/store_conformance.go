package outboxtest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/outbox"
)

// StoreFactory constructs a fresh Store (typically with pre-seeded rows)
// for each test subcase. Called at the START of each subcase.
type StoreFactory func(t *testing.T, seed []outbox.ClaimedEntry) outbox.Store

// newEntry creates a ClaimedEntry with sensible defaults for seeding.
func newEntry(id, eventType string, attempts int) outbox.ClaimedEntry {
	return outbox.ClaimedEntry{
		Entry: kout.Entry{
			ID:        id,
			EventType: eventType,
			Topic:     eventType,
			Payload:   []byte(`{"data":"test"}`),
			CreatedAt: time.Now(),
		},
		Attempts: attempts,
	}
}

// newEntryAt creates a ClaimedEntry with explicit CreatedAt for ordering tests.
func newEntryAt(id, eventType string, attempts int, createdAt time.Time) outbox.ClaimedEntry {
	return outbox.ClaimedEntry{
		Entry: kout.Entry{
			ID:        id,
			EventType: eventType,
			Topic:     eventType,
			Payload:   []byte(`{"data":"test"}`),
			CreatedAt: createdAt,
		},
		Attempts: attempts,
	}
}

// RunStoreConformanceSuite runs the full Store conformance suite against the
// supplied factory. Both FakeStore (runtime/outbox/outboxtest) and
// PGOutboxStore (adapters/postgres testcontainers) must pass this suite.
//
// outboxtest lives in runtime/outbox/outboxtest/ (not kernel/outbox/outboxtest/)
// because it imports runtime/outbox.Store and ClaimedEntry — it must sit in
// the same layer as the interface it tests.
func RunStoreConformanceSuite(t *testing.T, factory StoreFactory) {
	t.Helper()
	t.Run("ClaimPending_Empty", func(t *testing.T) { conformClaimPendingEmpty(t, factory) })
	t.Run("ClaimPending_BatchCap", func(t *testing.T) { conformClaimPendingBatchCap(t, factory) })
	t.Run("ClaimPending_SecondCallReturnsRemaining", func(t *testing.T) { conformClaimPendingSecondCall(t, factory) })
	t.Run("ClaimPending_ConcurrentNoDuplicate", func(t *testing.T) { conformClaimPendingConcurrent(t, factory) })
	t.Run("MarkPublished_TransitionsClaimingToPublished", func(t *testing.T) { conformMarkPublished(t, factory) })
	t.Run("MarkPublished_AlreadyReclaimed_UpdatedFalse", func(t *testing.T) { conformMarkPublishedReclaimed(t, factory) })
	t.Run("MarkRetry_TransitionsClaimingToPending", func(t *testing.T) { conformMarkRetry(t, factory) })
	t.Run("MarkRetry_SetsAttemptsAndNextRetryAt", func(t *testing.T) { conformMarkRetryFields(t) })
	t.Run("MarkDead_TransitionsClaimingToDead", func(t *testing.T) { conformMarkDead(t, factory) })
	t.Run("ReclaimStale_RecoversExpiredClaims", func(t *testing.T) { conformReclaimStaleRecovers(t, factory) })
	t.Run("ReclaimStale_IgnoresFreshClaims", func(t *testing.T) { conformReclaimStaleFresh(t, factory) })
	t.Run("ReclaimStale_EscalatesToDeadOnMaxAttempts", func(t *testing.T) { conformReclaimStaleEscalates(t, factory) })
	t.Run("CleanupPublished_DeletesOlderThanCutoff", func(t *testing.T) { conformCleanupPublished(t, factory) })
	t.Run("CleanupPublished_BatchLimit", func(t *testing.T) { conformCleanupPublishedBatch(t, factory) })
	t.Run("CleanupDead_DeletesOlderThanCutoff", func(t *testing.T) { conformCleanupDead(t, factory) })
}

func conformClaimPendingEmpty(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := context.Background()
	store := factory(t, nil)
	got, err := store.ClaimPending(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 entries, got %d", len(got))
	}
}

func conformClaimPendingBatchCap(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := context.Background()
	seed := []outbox.ClaimedEntry{
		newEntry("e1", "test.v1", 0),
		newEntry("e2", "test.v1", 0),
		newEntry("e3", "test.v1", 0),
	}
	store := factory(t, seed)
	got, err := store.ClaimPending(ctx, 2)
	if err != nil {
		t.Fatalf("ClaimPending: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 entries with batchSize=2, got %d", len(got))
	}
}

func conformClaimPendingSecondCall(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	seed := []outbox.ClaimedEntry{
		newEntryAt("e1", "test.v1", 0, now.Add(-3*time.Second)),
		newEntryAt("e2", "test.v1", 0, now.Add(-2*time.Second)),
		newEntryAt("e3", "test.v1", 0, now.Add(-1*time.Second)),
	}
	store := factory(t, seed)

	first, err := store.ClaimPending(ctx, 2)
	if err != nil {
		t.Fatalf("ClaimPending first: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("expected 2 from first call, got %d", len(first))
	}
	second, err := store.ClaimPending(ctx, 2)
	if err != nil {
		t.Fatalf("ClaimPending second: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("expected 1 from second call, got %d", len(second))
	}
	firstIDs := map[string]bool{first[0].ID: true, first[1].ID: true}
	if firstIDs[second[0].ID] {
		t.Errorf("duplicate claim: %s appeared in both calls", second[0].ID)
	}
}

func conformClaimPendingConcurrent(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := context.Background()
	const total = 20
	seed := make([]outbox.ClaimedEntry, total)
	for i := range total {
		seed[i] = newEntry(fmt.Sprintf("e%02d", i), "test.v1", 0)
	}
	store := factory(t, seed)

	const goroutines = 5
	resultsCh := make(chan []outbox.ClaimedEntry, goroutines)
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			got, err := store.ClaimPending(ctx, total)
			if err != nil {
				t.Errorf("ClaimPending: %v", err)
				resultsCh <- nil
				return
			}
			resultsCh <- got
		})
	}
	wg.Wait()
	close(resultsCh)

	seen := make(map[string]bool)
	for batch := range resultsCh {
		for _, e := range batch {
			if seen[e.ID] {
				t.Errorf("duplicate claim for entry %s", e.ID)
			}
			seen[e.ID] = true
		}
	}
}

func conformMarkPublished(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := context.Background()
	seed := []outbox.ClaimedEntry{newEntry("e1", "test.v1", 0)}
	store := factory(t, seed)

	claimed, err := store.ClaimPending(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimPending: err=%v len=%d", err, len(claimed))
	}
	updated, err := store.MarkPublished(ctx, "e1")
	if err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	if !updated {
		t.Error("expected updated=true for claiming→published transition")
	}
	got, _ := store.ClaimPending(ctx, 10)
	if len(got) != 0 {
		t.Errorf("expected 0 claimable after publish, got %d", len(got))
	}
}

func conformMarkPublishedReclaimed(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := context.Background()
	seed := []outbox.ClaimedEntry{newEntry("e1", "test.v1", 0)}
	store := factory(t, seed)

	_, _ = store.ClaimPending(ctx, 10)
	_, _ = store.MarkRetry(ctx, "e1", 1, time.Now().Add(time.Minute), "transient error")

	updated, err := store.MarkPublished(ctx, "e1")
	if err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	if updated {
		t.Error("expected updated=false for non-claiming entry")
	}
}

func conformMarkRetry(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := context.Background()
	seed := []outbox.ClaimedEntry{newEntry("e1", "test.v1", 0)}
	store := factory(t, seed)

	_, _ = store.ClaimPending(ctx, 10)

	nextRetry := time.Now().Add(10 * time.Second)
	updated, err := store.MarkRetry(ctx, "e1", 1, nextRetry, "transient")
	if err != nil {
		t.Fatalf("MarkRetry: %v", err)
	}
	if !updated {
		t.Error("expected updated=true")
	}
	got, _ := store.ClaimPending(ctx, 10)
	if len(got) != 0 {
		t.Errorf("expected 0 claimable before nextRetryAt, got %d", len(got))
	}
}

// conformMarkRetryFields is FakeStore-only because it relies on Snapshot() to
// inspect internal row fields (Attempts, NextRetryAt, LastError). There is no
// generic Store API to retrieve those fields; this sub-test intentionally does
// not accept a factory. Behavioral correctness (retry transitions pending) is
// already verified by conformMarkRetry which runs against all store factories.
func conformMarkRetryFields(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	seed := []outbox.ClaimedEntry{newEntry("e1", "test.v1", 0)}
	fs := NewFakeStore()
	fs.Seed(seed...)

	_, _ = fs.ClaimPending(ctx, 10)

	nextRetry := time.Now().Add(5 * time.Second)
	_, _ = fs.MarkRetry(ctx, "e1", 2, nextRetry, "some error")

	snap := fs.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("expected 1 row, got %d", len(snap))
	}
	row := snap[0]
	if row.Attempts != 2 {
		t.Errorf("Attempts: got %d, want 2", row.Attempts)
	}
	if row.NextRetryAt == nil {
		t.Error("NextRetryAt should not be nil")
	} else if !row.NextRetryAt.Equal(nextRetry) {
		t.Errorf("NextRetryAt: got %v, want %v", *row.NextRetryAt, nextRetry)
	}
	if row.LastError != "some error" {
		t.Errorf("LastError: got %q, want %q", row.LastError, "some error")
	}
}

func conformMarkDead(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := context.Background()
	seed := []outbox.ClaimedEntry{newEntry("e1", "test.v1", 4)}
	store := factory(t, seed)

	_, _ = store.ClaimPending(ctx, 10)

	updated, err := store.MarkDead(ctx, "e1", 5, "permanent failure")
	if err != nil {
		t.Fatalf("MarkDead: %v", err)
	}
	if !updated {
		t.Error("expected updated=true")
	}
	got, _ := store.ClaimPending(ctx, 10)
	if len(got) != 0 {
		t.Errorf("expected 0 claimable after dead, got %d", len(got))
	}
}

// conformReclaimStaleRecovers verifies that a claiming entry with an expired
// claim is transitioned back to pending by ReclaimStale.
//
// Clock strategy: pass claimTTL = -1h so that the condition
// "claimed_at < now() - claimTTL" becomes "claimed_at < now() + 1h", which
// is satisfied for any just-set claimed_at. This avoids clock-injection
// dependencies and works identically on FakeStore and PGOutboxStore.
func conformReclaimStaleRecovers(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := context.Background()
	store := factory(t, []outbox.ClaimedEntry{newEntry("e1", "test.v1", 0)})

	claimed, err := store.ClaimPending(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimPending: err=%v len=%d", err, len(claimed))
	}

	// Negative TTL → every claiming entry is immediately stale.
	// Zero baseDelay + maxDelay so the recovered entry gets next_retry_at = now()
	// and is immediately claimable in the next ClaimPending call.
	count, err := store.ReclaimStale(ctx, -time.Hour, 99, 0, 0)
	if err != nil {
		t.Fatalf("ReclaimStale: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 reclaimed, got %d", count)
	}

	// The recovered entry is pending with next_retry_at = now() (zero delay),
	// so ClaimPending must find it immediately.
	got, err := store.ClaimPending(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimPending after reclaim: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 claimable after reclaim, got %d", len(got))
	}
}

// conformReclaimStaleFresh verifies that a freshly claimed entry (claimTTL not
// yet elapsed) is NOT recovered by ReclaimStale.
//
// Uses a 1-hour TTL so that a just-set claimed_at is still fresh.
func conformReclaimStaleFresh(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := context.Background()
	store := factory(t, []outbox.ClaimedEntry{newEntry("e1", "test.v1", 0)})

	claimed, err := store.ClaimPending(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimPending: err=%v len=%d", err, len(claimed))
	}

	// 1-hour TTL: claimed_at (set just above) is well within TTL.
	count, err := store.ReclaimStale(ctx, time.Hour, 99, 5*time.Second, 5*time.Minute)
	if err != nil {
		t.Fatalf("ReclaimStale: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 reclaimed (fresh claim), got %d", count)
	}
}

// conformReclaimStaleEscalates verifies that a stale entry at maxAttempts-1 is
// escalated to dead (not retried) by ReclaimStale.
//
// Uses the same negative-TTL strategy as conformReclaimStaleRecovers.
// FakeStore-specific Snapshot checks are guarded by a type assertion so the
// test still exercises PGOutboxStore (via behavioral assertions on ClaimPending).
func conformReclaimStaleEscalates(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := context.Background()
	// attempts=4, maxAttempts=5 → attempts+1=5 >= maxAttempts → dead.
	store := factory(t, []outbox.ClaimedEntry{newEntry("e1", "test.v1", 4)})

	claimed, err := store.ClaimPending(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("ClaimPending: err=%v len=%d", err, len(claimed))
	}

	count, err := store.ReclaimStale(ctx, -time.Hour, 5, 5*time.Second, 5*time.Minute)
	if err != nil {
		t.Fatalf("ReclaimStale: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 reclaimed, got %d", count)
	}

	// Dead entries must not be claimable.
	got, err := store.ClaimPending(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimPending after escalation: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 claimable (entry escalated to dead), got %d", len(got))
	}

	// FakeStore-only: verify internal state via Snapshot.
	if fs, ok := store.(*FakeStore); ok {
		snap := fs.Snapshot()
		if len(snap) != 1 {
			t.Fatalf("FakeStore snapshot: expected 1 row, got %d", len(snap))
		}
		if snap[0].Status != "dead" {
			t.Errorf("FakeStore: expected status=dead, got %s", snap[0].Status)
		}
		if snap[0].Attempts != 5 {
			t.Errorf("FakeStore: expected attempts=5, got %d", snap[0].Attempts)
		}
	}
}

// conformCleanupPublished verifies that CleanupPublished deletes published
// entries older than the cutoff.
//
// Clock strategy: publish the entry first, then call Cleanup with
// cutoff = time.Now().Add(time.Hour) which covers any just-published row.
func conformCleanupPublished(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := context.Background()
	store := factory(t, []outbox.ClaimedEntry{newEntry("e1", "test.v1", 0)})

	_, _ = store.ClaimPending(ctx, 10)
	_, _ = store.MarkPublished(ctx, "e1")

	// cutoff 1 hour in the future covers the just-set published_at.
	cutoff := time.Now().Add(time.Hour)
	deleted, err := store.CleanupPublished(ctx, cutoff, 100)
	if err != nil {
		t.Fatalf("CleanupPublished: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	// FakeStore-only: verify row is gone via Snapshot.
	if fs, ok := store.(*FakeStore); ok {
		if snap := fs.Snapshot(); len(snap) != 0 {
			t.Errorf("FakeStore: expected 0 rows after cleanup, got %d", len(snap))
		}
	}
}

// conformCleanupPublishedBatch verifies that the batchSize limit is respected.
func conformCleanupPublishedBatch(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := context.Background()
	seed := []outbox.ClaimedEntry{
		newEntry("e1", "test.v1", 0),
		newEntry("e2", "test.v1", 0),
		newEntry("e3", "test.v1", 0),
	}
	store := factory(t, seed)

	for _, ce := range seed {
		_, _ = store.ClaimPending(ctx, 1)
		_, _ = store.MarkPublished(ctx, ce.ID)
	}

	cutoff := time.Now().Add(time.Hour)
	deleted, err := store.CleanupPublished(ctx, cutoff, 2)
	if err != nil {
		t.Fatalf("CleanupPublished: %v", err)
	}
	if deleted > 2 {
		t.Errorf("expected at most 2 deleted with batchSize=2, got %d", deleted)
	}
}

// conformCleanupDead verifies that CleanupDead deletes dead entries older than
// the cutoff.
func conformCleanupDead(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := context.Background()
	store := factory(t, []outbox.ClaimedEntry{newEntry("e1", "test.v1", 4)})

	_, _ = store.ClaimPending(ctx, 10)
	_, _ = store.MarkDead(ctx, "e1", 5, "perm error")

	cutoff := time.Now().Add(time.Hour)
	deleted, err := store.CleanupDead(ctx, cutoff, 100)
	if err != nil {
		t.Fatalf("CleanupDead: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	// FakeStore-only: verify row is gone via Snapshot.
	if fs, ok := store.(*FakeStore); ok {
		if snap := fs.Snapshot(); len(snap) != 0 {
			t.Errorf("FakeStore: expected 0 rows after cleanup, got %d", len(snap))
		}
	}
}
