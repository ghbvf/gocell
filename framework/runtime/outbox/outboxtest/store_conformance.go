package outboxtest

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	kout "github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/runtime/outbox"
)

const (
	// conformDNeg3s is used to seed entries 3 seconds in the past.
	conformDNeg3s = -testtime.D3s
	// conformDNeg2s is used to seed entries 2 seconds in the past.
	conformDNeg2s = -testtime.D2s
	// conformDNeg1s is used to seed entries 1 second in the past.
	conformDNeg1s = testtime.DNeg1s
	// conformReclaimBaseDelay is the baseDelay passed to ReclaimStale.
	conformReclaimBaseDelay = testtime.D5s
	// conformReclaimMaxDelay is the maxDelay passed to ReclaimStale.
	conformReclaimMaxDelay = testtime.D5min
	// conformMarkRetryDelay is the nextRetry offset used in conformMarkRetry.
	conformMarkRetryDelay = testtime.D10s
	// conformMarkRetryFieldsDelay is the nextRetry offset used in conformMarkRetryFields.
	conformMarkRetryFieldsDelay = testtime.D5s
)

// deduplicated per SonarCloud S1192.
const (
	testEventType          = "test.v1"
	msgClaimPending        = "ClaimPending: %v"
	msgClaimPendingWithLen = "ClaimPending: err=%v len=%d"
	msgReclaimStale        = "ReclaimStale: %v"
	msgMarkPublished       = "MarkPublished: %v"
	msgExpect1Claimed      = "expected 1 claimed, got %d"
	idEntryRace            = "e-race"
	errSome                = "some error"
	// Entry IDs for conformCountPendingExcludesFutureRetry. All three are
	// declared as constants so future edits adding a third occurrence cannot
	// silently trip Sonar S1192 again.
	idCPExclE1 = "cp-excl-e1"
	idCPExclE2 = "cp-excl-e2"
	idCPExclE3 = "cp-excl-e3"
)

// StoreFactory constructs a fresh Store (typically with pre-seeded rows)
// for each test subcase. Called at the START of each subcase.
type StoreFactory func(t *testing.T, seed []outbox.ClaimedEntry) outbox.Store

// mustEntry builds a sealed kout.Entry from an EntryScan, panicking on error.
// For test-seed use only — it is a programmer error to pass an invalid scan.
func mustEntry(s kout.EntryScan) kout.Entry {
	e, err := s.ToEntry()
	if err != nil {
		panic(panicregister.Approved("outboxtest-must-entry", errcode.Assertion("outboxtest.mustEntry: %v", err)))
	}
	return e
}

// newEntry creates a ClaimedEntry with sensible defaults for seeding.
func newEntry(id string, attempts int) outbox.ClaimedEntry {
	return outbox.ClaimedEntry{
		Entry: mustEntry(kout.EntryScan{
			ID:         id,
			EventType:  testEventType,
			Topic:      testEventType,
			Payload:    []byte(`{"data":"test"}`),
			CreatedAt:  time.Now(),
			OccurredAt: time.Now(),
		}),
		Attempts: attempts,
	}
}

// newEntryAt creates a ClaimedEntry with explicit CreatedAt for ordering tests.
func newEntryAt(id string, createdAt time.Time) outbox.ClaimedEntry {
	return outbox.ClaimedEntry{
		Entry: mustEntry(kout.EntryScan{
			ID:         id,
			EventType:  testEventType,
			Topic:      testEventType,
			Payload:    []byte(`{"data":"test"}`),
			CreatedAt:  createdAt,
			OccurredAt: createdAt,
		}),
		Attempts: 0,
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
	t.Run("CountPending_ReflectsSeeded", func(t *testing.T) { conformCountPendingSeeded(t, factory) })
	t.Run("CountPending_DecreasesAfterMarkPublished", func(t *testing.T) { conformCountPendingAfterPublish(t, factory) })
	t.Run("CountPending_ExcludesFutureRetry", func(t *testing.T) { conformCountPendingExcludesFutureRetry(t, factory) })
	t.Run("ClaimPending_Empty", func(t *testing.T) { conformClaimPendingEmpty(t, factory) })
	t.Run("ClaimPending_BatchCap", func(t *testing.T) { conformClaimPendingBatchCap(t, factory) })
	t.Run("ClaimPending_SecondCallReturnsRemaining", func(t *testing.T) { conformClaimPendingSecondCall(t, factory) })
	t.Run("ClaimPending_ConcurrentNoDuplicate", func(t *testing.T) { conformClaimPendingConcurrent(t, factory) })
	t.Run("MarkPublished_TransitionsClaimingToPublished", func(t *testing.T) { conformMarkPublished(t, factory) })
	t.Run("MarkPublished_AlreadyReclaimed_UpdatedFalse", func(t *testing.T) { conformMarkPublishedReclaimed(t, factory) })
	t.Run("MarkPublished_StaleLease_UpdatedFalse", func(t *testing.T) { conformMarkPublishedStaleLease(t, factory) })
	t.Run("Fencing_OldWorkerCannotOverwriteNewClaim", func(t *testing.T) { conformFencingRace(t, factory) })
	t.Run("MarkRetry_TransitionsClaimingToPending", func(t *testing.T) { conformMarkRetry(t, factory) })
	t.Run("MarkRetry_SetsAttemptsAndNextRetryAt", func(t *testing.T) { conformMarkRetryFields(t) })
	t.Run("MarkRetry_NonExistentEntry_ReturnsFalseNoError", func(t *testing.T) { conformMarkRetryNotExist(t, factory) })
	t.Run("MarkDead_TransitionsClaimingToDead", func(t *testing.T) { conformMarkDead(t, factory) })
	t.Run("MarkDead_NonExistentEntry_ReturnsFalseNoError", func(t *testing.T) { conformMarkDeadNotExist(t, factory) })
	t.Run("ReclaimStale_RecoversExpiredClaims", func(t *testing.T) { conformReclaimStaleRecovers(t, factory) })
	t.Run("ReclaimStale_IgnoresFreshClaims", func(t *testing.T) { conformReclaimStaleFresh(t, factory) })
	t.Run("ReclaimStale_EscalatesToDeadOnMaxAttempts", func(t *testing.T) { conformReclaimStaleEscalates(t, factory) })
	t.Run("CleanupPublished_DeletesOlderThanCutoff", func(t *testing.T) { conformCleanupPublished(t, factory) })
	t.Run("CleanupPublished_BatchLimit", func(t *testing.T) { conformCleanupPublishedBatch(t, factory) })
	t.Run("CleanupDead_DeletesOlderThanCutoff", func(t *testing.T) { conformCleanupDead(t, factory) })
	t.Run("OldestEligibleAt_PublishedEmpty_ReturnsFalse", func(t *testing.T) { conformOldestEligibleAtEmpty(t, factory, kout.StatePublished) })
	t.Run("OldestEligibleAt_DeadEmpty_ReturnsFalse", func(t *testing.T) { conformOldestEligibleAtEmpty(t, factory, kout.StateDead) })
	t.Run("OldestEligibleAt_Published_ReturnsMin", func(t *testing.T) { conformOldestEligibleAtPublished(t, factory) })
	t.Run("OldestEligibleAt_Dead_ReturnsMin", func(t *testing.T) { conformOldestEligibleAtDead(t, factory) })
	t.Run("OldestEligibleAt_InvalidStatus_ReturnsError", func(t *testing.T) { conformOldestEligibleAtInvalid(t, factory) })
	t.Run("Principal_OccurredAt_RoundTrip", func(t *testing.T) { RunPrincipalRoundTripConformance(t, factory) })
}

func conformClaimPendingEmpty(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	store := factory(t, nil)
	got, err := store.ClaimPending(ctx, 10)
	if err != nil {
		t.Fatalf(msgClaimPending, err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 entries, got %d", len(got))
	}
}

func conformClaimPendingBatchCap(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	seed := []outbox.ClaimedEntry{
		newEntry("e1", 0),
		newEntry("e2", 0),
		newEntry("e3", 0),
	}
	store := factory(t, seed)
	got, err := store.ClaimPending(ctx, 2)
	if err != nil {
		t.Fatalf(msgClaimPending, err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 entries with batchSize=2, got %d", len(got))
	}
}

func conformClaimPendingSecondCall(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	now := time.Now()
	seed := []outbox.ClaimedEntry{
		newEntryAt("e1", now.Add(conformDNeg3s)),
		newEntryAt("e2", now.Add(conformDNeg2s)),
		newEntryAt("e3", now.Add(conformDNeg1s)),
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
	firstIDs := map[string]bool{first[0].ID(): true, first[1].ID(): true}
	if firstIDs[second[0].ID()] {
		t.Errorf("duplicate claim: %s appeared in both calls", second[0].ID())
	}
}

func conformClaimPendingConcurrent(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	const total = 20
	seed := make([]outbox.ClaimedEntry, total)
	for i := range total {
		seed[i] = newEntry(fmt.Sprintf("e%02d", i), 0)
	}
	store := factory(t, seed)

	const goroutines = 5
	resultsCh := make(chan []outbox.ClaimedEntry, goroutines)
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			got, err := store.ClaimPending(ctx, total)
			if err != nil {
				t.Errorf(msgClaimPending, err)
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
			if seen[e.ID()] {
				t.Errorf("duplicate claim for entry %s", e.ID())
			}
			seen[e.ID()] = true
		}
	}
}

func conformMarkPublished(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	seed := []outbox.ClaimedEntry{newEntry("e1", 0)}
	store := factory(t, seed)

	claimed, err := store.ClaimPending(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf(msgClaimPendingWithLen, err, len(claimed))
	}
	updated, err := store.MarkPublished(ctx, "e1", claimed[0].LeaseID)
	if err != nil {
		t.Fatalf(msgMarkPublished, err)
	}
	if !updated {
		t.Error("expected updated=true for claiming→published transition")
	}
	got, _ := store.ClaimPending(ctx, 10)
	if len(got) != 0 {
		t.Errorf("expected 0 claimable after publish, got %d", len(got))
	}
}

// conformMarkPublishedStaleLease verifies the fencing token contract: a
// MarkPublished call carrying the wrong leaseID must return updated=false
// even when the row is still in claiming state. Without lease fencing the
// status='claiming' check alone would let a stale worker overwrite a fresh
// claim (B2-A-01).
func conformMarkPublishedStaleLease(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	store := factory(t, []outbox.ClaimedEntry{newEntry("e1", 0)})

	claimed, err := store.ClaimPending(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf(msgClaimPendingWithLen, err, len(claimed))
	}

	// Fresh UUID — guaranteed not to match the lease ClaimPending issued.
	updated, err := store.MarkPublished(ctx, "e1", uuid.NewString())
	if err != nil {
		t.Fatalf(msgMarkPublished, err)
	}
	if updated {
		t.Error("expected updated=false when leaseID does not match (fencing CAS must miss)")
	}

	// Real lease must still succeed — proves the row was untouched by the
	// stale-lease attempt.
	updatedReal, err := store.MarkPublished(ctx, "e1", claimed[0].LeaseID)
	if err != nil {
		t.Fatalf("MarkPublished (real lease): %v", err)
	}
	if !updatedReal {
		t.Error("expected updated=true when leaseID matches")
	}
}

// conformFencingRace simulates the production race: worker A claims, the
// reclaim sweep recovers the row (lease lost), worker B re-claims with a
// fresh lease, then worker A's publisher finally completes and tries to
// MarkPublished. With fencing, A's CAS must miss; without it, A would
// overwrite B's authoritative status (B2-A-01).
func conformFencingRace(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	store := factory(t, []outbox.ClaimedEntry{newEntry(idEntryRace, 0)})

	// Worker A claims.
	a, err := store.ClaimPending(ctx, 10)
	if err != nil || len(a) != 1 {
		t.Fatalf(msgClaimPendingWithLen, err, len(a))
	}
	leaseA := a[0].LeaseID

	// Reclaim sweep: TTL = -1h so the just-set claim is "expired" and the
	// row goes back to pending with lease cleared. Zero baseDelay so the
	// recovered row is immediately claimable.
	count, err := store.ReclaimStale(ctx, -time.Hour, 99, 0, 0, 1000)
	if err != nil {
		t.Fatalf(msgReclaimStale, err)
	}
	if count != 1 {
		t.Fatalf("ReclaimStale: expected 1, got %d", count)
	}

	// Worker B claims the recovered row with a fresh lease.
	b, err := store.ClaimPending(ctx, 10)
	if err != nil || len(b) != 1 {
		t.Fatalf(msgClaimPendingWithLen, err, len(b))
	}
	leaseB := b[0].LeaseID
	if leaseB == leaseA {
		t.Fatalf("expected B lease distinct from A lease (got %q)", leaseA)
	}

	// Worker A's stale publisher completes and tries to MarkPublished.
	// MUST fail the CAS — leaseA no longer owns the row.
	updatedA, err := store.MarkPublished(ctx, idEntryRace, leaseA)
	if err != nil {
		t.Fatalf("MarkPublished (stale): %v", err)
	}
	if updatedA {
		t.Fatal("FENCING VIOLATION: stale lease A overwrote new lease B's row")
	}

	// Worker B's lease still owns the row → MarkPublished succeeds.
	updatedB, err := store.MarkPublished(ctx, idEntryRace, leaseB)
	if err != nil {
		t.Fatalf("MarkPublished (current): %v", err)
	}
	if !updatedB {
		t.Error("expected updated=true for current lease")
	}
}

func conformMarkPublishedReclaimed(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	seed := []outbox.ClaimedEntry{newEntry("e1", 0)}
	store := factory(t, seed)

	claimed, _ := store.ClaimPending(ctx, 10)
	if len(claimed) != 1 {
		t.Fatalf(msgExpect1Claimed, len(claimed))
	}
	leaseID := claimed[0].LeaseID
	_, _ = store.MarkRetry(ctx, "e1", leaseID, 1, time.Now().Add(time.Minute), "transient error")

	updated, err := store.MarkPublished(ctx, "e1", leaseID)
	if err != nil {
		t.Fatalf(msgMarkPublished, err)
	}
	if updated {
		t.Error("expected updated=false for non-claiming entry")
	}
}

func conformMarkRetry(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	seed := []outbox.ClaimedEntry{newEntry("e1", 0)}
	store := factory(t, seed)

	claimed, _ := store.ClaimPending(ctx, 10)
	if len(claimed) != 1 {
		t.Fatalf(msgExpect1Claimed, len(claimed))
	}

	nextRetry := time.Now().Add(conformMarkRetryDelay)
	updated, err := store.MarkRetry(ctx, "e1", claimed[0].LeaseID, 1, nextRetry, "transient")
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
	ctx := t.Context()
	seed := []outbox.ClaimedEntry{newEntry("e1", 0)}
	fs := NewFakeStore()
	fs.Seed(seed...)

	claimed, _ := fs.ClaimPending(ctx, 10)
	if len(claimed) != 1 {
		t.Fatalf(msgExpect1Claimed, len(claimed))
	}

	nextRetry := time.Now().Add(conformMarkRetryFieldsDelay)
	_, _ = fs.MarkRetry(ctx, "e1", claimed[0].LeaseID, 2, nextRetry, errSome)

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
	if row.LastError != errSome {
		t.Errorf("LastError: got %q, want %q", row.LastError, errSome)
	}
}

// conformMarkRetryNotExist verifies that calling MarkRetry on an ID that does
// not exist returns updated=false and no error. This is the "tombstoned by
// reclaim" / "racing reclaim" path that publishers must tolerate.
func conformMarkRetryNotExist(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	store := factory(t, nil)

	// Lease value irrelevant — row does not exist; CAS necessarily fails.
	updated, err := store.MarkRetry(ctx, "does-not-exist", uuid.NewString(), 1, time.Now().Add(time.Second), "transient")
	if err != nil {
		t.Fatalf("MarkRetry on missing entry should not error, got: %v", err)
	}
	if updated {
		t.Error("expected updated=false for non-existent entry")
	}
}

func conformMarkDead(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	seed := []outbox.ClaimedEntry{newEntry("e1", 4)}
	store := factory(t, seed)

	claimed, _ := store.ClaimPending(ctx, 10)
	if len(claimed) != 1 {
		t.Fatalf(msgExpect1Claimed, len(claimed))
	}

	updated, err := store.MarkDead(ctx, "e1", claimed[0].LeaseID, 5, "permanent failure")
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

// conformMarkDeadNotExist verifies that calling MarkDead on an ID that does
// not exist returns updated=false and no error. Same race semantics as MarkRetry.
func conformMarkDeadNotExist(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	store := factory(t, nil)

	updated, err := store.MarkDead(ctx, "does-not-exist", uuid.NewString(), 5, "permanent")
	if err != nil {
		t.Fatalf("MarkDead on missing entry should not error, got: %v", err)
	}
	if updated {
		t.Error("expected updated=false for non-existent entry")
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
	ctx := t.Context()
	store := factory(t, []outbox.ClaimedEntry{newEntry("e1", 0)})

	claimed, err := store.ClaimPending(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf(msgClaimPendingWithLen, err, len(claimed))
	}

	// Negative TTL → every claiming entry is immediately stale.
	// Zero baseDelay + maxDelay so the recovered entry gets next_retry_at = now()
	// and is immediately claimable in the next ClaimPending call.
	count, err := store.ReclaimStale(ctx, -time.Hour, 99, 0, 0, 1000)
	if err != nil {
		t.Fatalf(msgReclaimStale, err)
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
	ctx := t.Context()
	store := factory(t, []outbox.ClaimedEntry{newEntry("e1", 0)})

	claimed, err := store.ClaimPending(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf(msgClaimPendingWithLen, err, len(claimed))
	}

	// 1-hour TTL: claimed_at (set just above) is well within TTL.
	count, err := store.ReclaimStale(ctx, time.Hour, 99, conformReclaimBaseDelay, conformReclaimMaxDelay, 1000)
	if err != nil {
		t.Fatalf(msgReclaimStale, err)
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
	ctx := t.Context()
	// attempts=4, maxAttempts=5 → attempts+1=5 >= maxAttempts → dead.
	store := factory(t, []outbox.ClaimedEntry{newEntry("e1", 4)})

	claimed, err := store.ClaimPending(ctx, 10)
	if err != nil || len(claimed) != 1 {
		t.Fatalf(msgClaimPendingWithLen, err, len(claimed))
	}

	count, err := store.ReclaimStale(ctx, -time.Hour, 5, conformReclaimBaseDelay, conformReclaimMaxDelay, 1000)
	if err != nil {
		t.Fatalf(msgReclaimStale, err)
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
		if snap[0].Status != kout.StateDead {
			t.Errorf("FakeStore: expected status=%s, got %s", kout.StateDead, snap[0].Status)
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
	ctx := t.Context()
	store := factory(t, []outbox.ClaimedEntry{newEntry("e1", 0)})

	claimed, _ := store.ClaimPending(ctx, 10)
	if len(claimed) != 1 {
		t.Fatalf(msgExpect1Claimed, len(claimed))
	}
	_, _ = store.MarkPublished(ctx, "e1", claimed[0].LeaseID)

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
	ctx := t.Context()
	seed := []outbox.ClaimedEntry{
		newEntry("e1", 0),
		newEntry("e2", 0),
		newEntry("e3", 0),
	}
	store := factory(t, seed)

	for _, ce := range seed {
		claimed, _ := store.ClaimPending(ctx, 1)
		if len(claimed) != 1 {
			t.Fatalf("ClaimPending(1): expected 1 entry, got %d", len(claimed))
		}
		_, _ = store.MarkPublished(ctx, ce.ID(), claimed[0].LeaseID)
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
	ctx := t.Context()
	store := factory(t, []outbox.ClaimedEntry{newEntry("e1", 4)})

	claimed, _ := store.ClaimPending(ctx, 10)
	if len(claimed) != 1 {
		t.Fatalf(msgExpect1Claimed, len(claimed))
	}
	_, _ = store.MarkDead(ctx, "e1", claimed[0].LeaseID, 5, "perm error")

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

// conformOldestEligibleAtEmpty verifies that an empty table (or one with no
// rows in the requested status) returns ok=false and a nil error — the
// "idle table" branch the relay's nextCleanupWait relies on to back off to
// the safety ceiling instead of tight-looping.
func conformOldestEligibleAtEmpty(t *testing.T, factory StoreFactory, status kout.State) {
	t.Helper()
	ctx := t.Context()
	store := factory(t, nil)

	at, ok, err := store.OldestEligibleAt(ctx, status)
	if err != nil {
		t.Fatalf("OldestEligibleAt(%s) on empty: %v", status, err)
	}
	if ok {
		t.Errorf("OldestEligibleAt(%s) on empty: ok=true, at=%v; want ok=false", status, at)
	}
}

// conformOldestEligibleAtPublished verifies that with multiple published rows,
// the smallest published_at (MIN) is returned, not a later row.
//
// The test publishes entries one at a time in ClaimPending order (which is
// created_at ASC), recording a time upper-bound after the FIRST MarkPublished
// returns. Because ClaimPending orders by created_at ASC, claimed[0] is the
// oldest entry; its published_at ≤ t_first_upper. The remaining entries are
// published after t_first_upper, so their published_at > t_first_upper.
// If the implementation returns MIN (oldest), at ≤ t_first_upper.
// If the implementation returns MAX or any later row, at > t_first_upper,
// causing the assertion to fail. This distinguishes correct MIN from any
// non-MIN implementation without requiring an injected clock or sleep.
//
// conformRFC: ClaimPending ORDER BY next_retry_at NULLS FIRST, created_at ASC
// guarantees the oldest entry is returned first — both FakeStore and
// PGOutboxStore observe this ordering.
func conformOldestEligibleAtPublished(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	now := time.Now()
	seed := []outbox.ClaimedEntry{
		newEntryAt("e1", now.Add(conformDNeg3s)),
		newEntryAt("e2", now.Add(conformDNeg2s)),
		newEntryAt("e3", now.Add(conformDNeg1s)),
	}
	store := factory(t, seed)

	// All eligible rows are claimed in a single batch (PG ClaimPending uses
	// MATERIALIZED with ORDER BY); per-iteration ClaimPending would return
	// empty after the first call. Claim once, then publish each entry in
	// order so we can establish a time boundary between the oldest and the rest.
	claimed, err := store.ClaimPending(ctx, 10)
	if err != nil {
		t.Fatalf(msgClaimPending, err)
	}
	if len(claimed) != len(seed) {
		t.Fatalf("ClaimPending: expected %d, got %d", len(seed), len(claimed))
	}

	// Publish the FIRST (oldest) entry separately and record the upper bound
	// of its published_at. ClaimPending returns entries sorted by
	// (next_retry_at NULLS FIRST, created_at ASC), so claimed[0] is e1 (oldest).
	firstEntry := claimed[0]
	if _, err := store.MarkPublished(ctx, firstEntry.ID(), firstEntry.LeaseID); err != nil {
		t.Fatalf("MarkPublished(first): %v", err)
	}
	// t_first_upper is recorded AFTER the first MarkPublished returns. The
	// remaining entries are published after this point, so their published_at
	// values are guaranteed to be ≥ t_first_upper.
	tFirstUpper := time.Now()

	// Publish the remaining entries.
	for _, ce := range claimed[1:] {
		if _, err := store.MarkPublished(ctx, ce.ID(), ce.LeaseID); err != nil {
			t.Fatalf("MarkPublished(%s): %v", ce.ID(), err)
		}
	}

	at, ok, err := store.OldestEligibleAt(ctx, kout.StatePublished)
	if err != nil {
		t.Fatalf("OldestEligibleAt: %v", err)
	}
	if !ok {
		t.Fatal("OldestEligibleAt: expected ok=true, got false")
	}
	// MIN assertion: the result must not be after tFirstUpper. Any implementation
	// that returns MAX or a later row's published_at (which is > tFirstUpper by
	// construction) will fail here.
	if at.After(tFirstUpper) {
		t.Errorf("OldestEligibleAt: returned %v, which is after the first published_at upper bound %v; "+
			"expected the MINIMUM published_at (oldest row) — implementation may be returning MAX instead of MIN",
			at, tFirstUpper)
	}
	// Sanity lower bound: result must not predate the test run. Entries are
	// seconds-old, so a one-minute window is ample margin.
	if at.Before(now.Add(-time.Minute)) {
		t.Errorf("OldestEligibleAt: returned %v which is unreasonably old (before test start - 1min)", at)
	}
}

// conformOldestEligibleAtDead verifies the same min semantics for dead rows.
func conformOldestEligibleAtDead(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	store := factory(t, []outbox.ClaimedEntry{newEntry("e1", 4)})

	claimed, err := store.ClaimPending(ctx, 10)
	if err != nil {
		t.Fatalf(msgClaimPending, err)
	}
	if len(claimed) != 1 {
		t.Fatalf(msgExpect1Claimed, len(claimed))
	}
	if _, err := store.MarkDead(ctx, "e1", claimed[0].LeaseID, 5, "perm"); err != nil {
		t.Fatalf("MarkDead: %v", err)
	}

	at, ok, err := store.OldestEligibleAt(ctx, kout.StateDead)
	if err != nil {
		t.Fatalf("OldestEligibleAt: %v", err)
	}
	if !ok {
		t.Fatal("OldestEligibleAt: expected ok=true after MarkDead, got false")
	}
	if at.IsZero() {
		t.Error("OldestEligibleAt: returned zero time after MarkDead")
	}
}

// conformCountPendingExcludesFutureRetry verifies that CountPending only counts
// rows eligible for ClaimPending — status=pending AND (next_retry_at IS NULL OR
// next_retry_at <= now()). A row with next_retry_at in the future must NOT be
// counted, matching ClaimPending semantics.
//
// GREEN: both backends now apply the next_retry_at predicate —
// FakeStore.CountPending (runtime/outbox/outboxtest/fake_store.go) skips rows
// with nextRetryAt > now(); PGOutboxStore.CountPending uses
// countPendingQuery `... WHERE status = $1 AND (next_retry_at IS NULL OR
// next_retry_at <= now())` (adapters/postgres/outbox_store.go). This
// conformance scenario exercises both backends.
func conformCountPendingExcludesFutureRetry(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()

	now := time.Now()
	futureRetry := now.Add(time.Hour) // well in the future — must be excluded

	// Seed three pending rows:
	//   e1: next_retry_at = nil          → eligible, CountPending must include
	//   e2: next_retry_at = nil          → eligible, CountPending must include
	//   e3: next_retry_at = now+1h       → NOT eligible, CountPending must exclude
	//
	// We seed e1/e2 as plain pending (no nextRetryAt).
	// e3 must be seeded as pending with a future nextRetryAt.
	// The easiest way: seed as plain pending then call MarkRetry to set the delay.
	seed := []outbox.ClaimedEntry{
		newEntry(idCPExclE1, 0),
		newEntry(idCPExclE2, 0),
		newEntry(idCPExclE3, 1), // attempts=1 to have a plausible retry scenario
	}
	store := factory(t, seed)

	// Claim e3 so we can MarkRetry with a future next_retry_at.
	// We claim one at a time to get a deterministic lease for e3.
	// First two claims are e1/e2 (FakeStore orders nil-nextRetryAt first).
	// Then claim e3.
	var e3LeaseID string
	for range 3 {
		claimed, err := store.ClaimPending(ctx, 1)
		if err != nil || len(claimed) != 1 {
			t.Fatalf("ClaimPending single: err=%v len=%d", err, len(claimed))
		}
		if claimed[0].ID() == idCPExclE3 {
			e3LeaseID = claimed[0].LeaseID
		} else {
			// Release e1/e2 back to pending via MarkRetry with past next_retry_at.
			_, _ = store.MarkRetry(ctx, claimed[0].ID(), claimed[0].LeaseID, 0, now.Add(-time.Second), "reset")
		}
	}
	if e3LeaseID == "" {
		t.Fatal("did not claim e3 — test setup error")
	}

	// Set e3's retry time to the future → it must not be counted.
	updated, err := store.MarkRetry(ctx, idCPExclE3, e3LeaseID, 2, futureRetry, "future retry")
	if err != nil || !updated {
		t.Fatalf("MarkRetry e3 future: err=%v updated=%v", err, updated)
	}

	// At this point: e1, e2 have next_retry_at <= now; e3 has next_retry_at = now+1h.
	// CountPending expected: 2 (e1 + e2). Both backends now apply the
	// next_retry_at predicate (see GREEN note on the test godoc above).
	got, err := store.CountPending(ctx)
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	if got != 2 {
		t.Errorf("CountPending must exclude rows with future next_retry_at: got %d, want 2"+
			" (row %s has next_retry_at=%v, must not count)", got, idCPExclE3, futureRetry)
	}
}

// conformOldestEligibleAtInvalid verifies that States other than StatePublished
// or StateDead return an error (the contract narrows the surface to exactly the
// two cleanup-eligible statuses).
func conformOldestEligibleAtInvalid(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	store := factory(t, nil)

	// StatePending and StateClaiming are valid State values but must be rejected.
	// State(0) and State(99) are invalid State values and must also be rejected.
	for _, bad := range []kout.State{kout.StatePending, kout.StateClaiming, kout.State(0), kout.State(99)} {
		_, _, err := store.OldestEligibleAt(ctx, bad)
		if err == nil {
			t.Errorf("OldestEligibleAt(%s): expected error, got nil", bad)
		}
	}
}

// conformCountPendingSeeded verifies that CountPending returns the number of
// seeded pending entries.
func conformCountPendingSeeded(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	const n = 3
	seed := make([]outbox.ClaimedEntry, n)
	for i := range n {
		seed[i] = newEntry(fmt.Sprintf("cp-e%d", i), 0)
	}
	store := factory(t, seed)

	got, err := store.CountPending(ctx)
	if err != nil {
		t.Fatalf("CountPending: %v", err)
	}
	if got != n {
		t.Errorf("CountPending: got %d, want %d", got, n)
	}
}

// conformCountPendingAfterPublish verifies that CountPending decreases after
// entries are published: seeding N entries and publishing M leaves N-M pending.
func conformCountPendingAfterPublish(t *testing.T, factory StoreFactory) {
	t.Helper()
	ctx := t.Context()
	const total = 4
	const publishN = 2
	seed := make([]outbox.ClaimedEntry, total)
	for i := range total {
		seed[i] = newEntry(fmt.Sprintf("cp-pub-e%d", i), 0)
	}
	store := factory(t, seed)

	// Claim and publish publishN entries.
	claimed, err := store.ClaimPending(ctx, publishN)
	if err != nil || len(claimed) != publishN {
		t.Fatalf(msgClaimPendingWithLen, err, len(claimed))
	}
	for _, ce := range claimed {
		if _, err := store.MarkPublished(ctx, ce.ID(), ce.LeaseID); err != nil {
			t.Fatalf("MarkPublished(%s): %v", ce.ID(), err)
		}
	}

	got, err := store.CountPending(ctx)
	if err != nil {
		t.Fatalf("CountPending after publish: %v", err)
	}
	want := int64(total - publishN)
	if got != want {
		t.Errorf("CountPending after publishing %d: got %d, want %d", publishN, got, want)
	}
}
