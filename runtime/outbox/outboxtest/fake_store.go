// Package outboxtest provides a public in-memory Store implementation and a
// Store conformance test suite for use in unit tests.
//
// FakeStore implements runtime/outbox.Store in memory and is intended for unit
// tests in cells and runtime/outbox. The conformance suite (RunStoreConformanceSuite)
// verifies that any Store implementation produces identical observable behavior.
package outboxtest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/outbox"
)

// Compile-time assertions: FakeStore satisfies outbox.Store (relay side) and
// kout.Writer (producer side), so a producer using the standard WriterEmitter
// path and a relay polling the same store interoperate in unit tests.
var (
	_ outbox.Store = (*FakeStore)(nil)
	_ kout.Writer  = (*FakeStore)(nil)
)

// fakeRow holds the full mutable state of a single outbox entry in FakeStore.
type fakeRow struct {
	entry       kout.Entry
	status      kout.State
	attempts    int
	leaseID     string
	claimedAt   *time.Time
	publishedAt *time.Time
	deadAt      *time.Time
	nextRetryAt *time.Time
	lastError   string
}

// FakeRow is a read-only snapshot of a fakeRow, returned by Snapshot.
// For tests only, not for production use.
//
// Status is typed kout.State so test callers compare against named constants
// (e.g. kout.StatePending) rather than bare string literals, preserving the
// typed-state single-source guarantee enforced by OUTBOX-STATE-LITERAL-BAN-01.
type FakeRow struct {
	Entry       kout.Entry
	Status      kout.State
	Attempts    int
	LeaseID     string
	ClaimedAt   *time.Time
	PublishedAt *time.Time
	DeadAt      *time.Time
	NextRetryAt *time.Time
	LastError   string
}

// FakeStore is a thread-safe in-memory implementation of runtime/outbox.Store
// intended for unit tests in cells and runtime/outbox. Not for production use.
//
// Semantics exactly match the Store conformance suite; PGOutboxStore in
// adapters/postgres must produce identical observable behavior.
//
// ClaimPending ordering: next_retry_at ASC (nil first) + created_at ASC,
// consistent with idx_outbox_pending_v2 in the PG adapter.
type FakeStore struct {
	mu     sync.Mutex
	rows   map[string]*fakeRow
	now    func() time.Time
	notify chan struct{} // closed-and-recreated on every state mutation; powers WaitFor
}

// NewFakeStore creates an empty FakeStore using time.Now as clock.
func NewFakeStore() *FakeStore {
	return &FakeStore{
		rows:   make(map[string]*fakeRow),
		now:    time.Now,
		notify: make(chan struct{}),
	}
}

// WithClock replaces the clock (useful for ReclaimStale / Cleanup tests).
func (s *FakeStore) WithClock(now func() time.Time) *FakeStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
	return s
}

// notifyLocked closes the current notify channel (waking every goroutine
// blocked in WaitFor that captured it) and installs a fresh channel for the
// next wait cycle. Caller must hold s.mu so mutators serialize against
// notify-channel rotation.
//
// Channel-as-condvar (close + recreate) is the canonical Go alternative to
// sync.Cond when waits must be ctx-cancellable; sync.Cond has no native ctx
// integration. ref: golang.org/x/sync/singleflight, net/http h2_bundle.go.
func (s *FakeStore) notifyLocked() {
	close(s.notify)
	s.notify = make(chan struct{})
}

// Seed inserts rows directly (bypasses normal Writer). Used by test setup.
// Each row starts in "pending" status with the supplied Attempts; LeaseID,
// claimed_at / published_at / dead_at / next_retry_at, and last_error are
// reset to zero. Existing rows with the same ID are overwritten.
func (s *FakeStore) Seed(entries ...outbox.ClaimedEntry) {
	if len(entries) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ce := range entries {
		row := &fakeRow{
			entry:    ce.Entry,
			status:   kout.StatePending,
			attempts: ce.Attempts,
		}
		s.rows[ce.ID()] = row
	}
	s.notifyLocked()
}

// Write inserts entry as a single pending row (attempts 0, no lease), letting a
// producer emit via the standard kout.WriterEmitter path while the relay polls the
// same store. Demo/test use only — there is no transactional atomicity (the real
// PG Writer ties the write to the caller's business tx; FakeStore is in-memory).
// An existing row with the same ID is overwritten, mirroring Seed.
func (s *FakeStore) Write(_ context.Context, entry kout.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[entry.ID()] = &fakeRow{
		entry:    entry,
		status:   kout.StatePending,
		attempts: 0,
	}
	s.notifyLocked()
	return nil
}

// Snapshot returns a sorted (by ID) copy of all rows for test assertions.
func (s *FakeStore) Snapshot() []FakeRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// snapshotLocked is the unsynchronized core of Snapshot, also used by
// WaitFor to evaluate cond against a consistent view captured under s.mu.
// Caller must hold s.mu.
func (s *FakeStore) snapshotLocked() []FakeRow {
	out := make([]FakeRow, 0, len(s.rows))
	for _, r := range s.rows {
		fr := FakeRow{
			Entry:     r.entry,
			Status:    r.status,
			Attempts:  r.attempts,
			LeaseID:   r.leaseID,
			LastError: r.lastError,
		}
		if r.claimedAt != nil {
			t := *r.claimedAt
			fr.ClaimedAt = &t
		}
		if r.publishedAt != nil {
			t := *r.publishedAt
			fr.PublishedAt = &t
		}
		if r.deadAt != nil {
			t := *r.deadAt
			fr.DeadAt = &t
		}
		if r.nextRetryAt != nil {
			t := *r.nextRetryAt
			fr.NextRetryAt = &t
		}
		out = append(out, fr)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Entry.ID() < out[j].Entry.ID()
	})
	return out
}

// WaitFor blocks until cond evaluates true on a current FakeStore snapshot
// or ctx is canceled, returning ctx.Err() in the latter case. For use in
// tests only.
//
// cond is re-evaluated synchronously after every state mutation (Seed,
// ClaimPending, MarkPublished, MarkRetry, MarkDead, ReclaimStale,
// CleanupPublished, CleanupDead) — there is no polling and no timing
// coupling. Tests should pass a generous ctx deadline (typically
// testtime.EventuallyDefault) only as a deadlock backstop, never as the
// expected wait time.
//
// The notify channel is captured under s.mu before cond runs, so any
// mutation that races between the unlock and the select{} closes the
// captured channel and triggers immediate re-evaluation — race-free
// without sync.Cond (which lacks ctx integration).
func (s *FakeStore) WaitFor(ctx context.Context, cond func([]FakeRow) bool) error {
	for {
		s.mu.Lock()
		snap := s.snapshotLocked()
		notify := s.notify
		s.mu.Unlock()

		if cond(snap) {
			return nil
		}
		select {
		case <-notify:
			// state changed; re-evaluate
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// ---------------------------------------------------------------------------
// outbox.Store implementation
// ---------------------------------------------------------------------------

// ClaimPending atomically transitions up to batchSize rows from pending to
// claiming status. Returns empty slice + nil when nothing is claimable.
// Ordering: next_retry_at ASC (nil first) + created_at ASC.
func (s *FakeStore) ClaimPending(_ context.Context, batchSize int) ([]outbox.ClaimedEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()

	// Collect eligible rows.
	var candidates []*fakeRow
	for _, r := range s.rows {
		if r.status != kout.StatePending {
			continue
		}
		if r.nextRetryAt != nil && r.nextRetryAt.After(now) {
			continue
		}
		candidates = append(candidates, r)
	}

	// Sort: next_retry_at NULLS FIRST, then created_at ASC.
	sort.Slice(candidates, func(i, j int) bool {
		ri, rj := candidates[i], candidates[j]
		switch {
		case ri.nextRetryAt == nil && rj.nextRetryAt == nil:
			return ri.entry.CreatedAt().Before(rj.entry.CreatedAt())
		case ri.nextRetryAt == nil:
			return true
		case rj.nextRetryAt == nil:
			return false
		default:
			if ri.nextRetryAt.Equal(*rj.nextRetryAt) {
				return ri.entry.CreatedAt().Before(rj.entry.CreatedAt())
			}
			return ri.nextRetryAt.Before(*rj.nextRetryAt)
		}
	})

	if len(candidates) > batchSize {
		candidates = candidates[:batchSize]
	}

	// One lease per ClaimPending batch — mirrors the PG adapter where the
	// claimPendingQuery sets a single uuid for the materialized rows.
	leaseID := uuid.NewString()
	result := make([]outbox.ClaimedEntry, 0, len(candidates))
	for _, r := range candidates {
		r.status = kout.StateClaiming
		r.leaseID = leaseID
		t := now
		r.claimedAt = &t
		result = append(result, outbox.ClaimedEntry{
			Entry:    r.entry,
			Attempts: r.attempts,
			LeaseID:  leaseID,
		})
	}
	if len(result) > 0 {
		s.notifyLocked()
	}
	return result, nil
}

// MarkPublished transitions an entry from claiming to published.
// updated=false when the lease no longer matches (reclaimed or stale).
func (s *FakeStore) MarkPublished(_ context.Context, id, leaseID string) (updated bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rows[id]
	if !ok || r.status != kout.StateClaiming || r.leaseID != leaseID {
		return false, nil
	}
	now := s.now()
	r.status = kout.StatePublished
	r.publishedAt = &now
	r.claimedAt = nil
	s.notifyLocked()
	return true, nil
}

// MarkRetry transitions a failing entry back to pending.
// updated=false when the lease no longer matches (reclaimed or stale).
func (s *FakeStore) MarkRetry(
	_ context.Context, id, leaseID string, attempts int, nextRetryAt time.Time, lastError string,
) (updated bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rows[id]
	if !ok || r.status != kout.StateClaiming || r.leaseID != leaseID {
		return false, nil
	}
	r.status = kout.StatePending
	r.attempts = attempts
	r.nextRetryAt = &nextRetryAt
	r.lastError = lastError
	r.claimedAt = nil
	r.leaseID = ""
	s.notifyLocked()
	return true, nil
}

// MarkDead transitions a failing entry to dead.
// updated=false when the lease no longer matches (reclaimed or stale).
func (s *FakeStore) MarkDead(_ context.Context, id, leaseID string, attempts int, lastError string) (updated bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rows[id]
	if !ok || r.status != kout.StateClaiming || r.leaseID != leaseID {
		return false, nil
	}
	now := s.now()
	r.status = kout.StateDead
	r.attempts = attempts
	r.lastError = lastError
	r.deadAt = &now
	r.claimedAt = nil
	s.notifyLocked()
	return true, nil
}

// ReclaimStale transitions up to batchSize claiming rows whose claimed_at is
// older than claimTTL back to pending or to dead (when attempts+1 >= maxAttempts).
// Returns count of rows recovered across both destinations. Eligible rows are
// visited in claimed_at ASC order with id ASC tiebreaker, mirroring the PG
// adapter's ORDER BY claimed_at LIMIT N semantics so a queue larger than the
// cap is split across loop iterations deterministically.
func (s *FakeStore) ReclaimStale(
	_ context.Context,
	claimTTL time.Duration,
	maxAttempts int,
	baseDelay, maxDelay time.Duration,
	batchSize int,
) (count int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	cutoff := now.Add(-claimTTL)

	// Reuse the cleanup-path stable ordering helper so reclaim and cleanup
	// share a single source of "eligible rows in deterministic order".
	ids := s.eligibleIDsByTimeAsc(kout.StateClaiming, cutoff,
		func(r *fakeRow) *time.Time { return r.claimedAt })

	for _, id := range ids {
		if count >= batchSize {
			break
		}
		r := s.rows[id]
		newAttempts := r.attempts + 1
		if newAttempts >= maxAttempts {
			r.status = kout.StateDead
			r.attempts = newAttempts
			r.deadAt = &now
			r.claimedAt = nil
		} else {
			shift := min(newAttempts, 30)
			delay := cappedDelay(baseDelay<<shift, maxDelay)
			nextRetry := now.Add(delay)
			r.status = kout.StatePending
			r.attempts = newAttempts
			r.nextRetryAt = &nextRetry
			r.claimedAt = nil
			r.leaseID = ""
		}
		count++
	}
	if count > 0 {
		s.notifyLocked()
	}
	return count, nil
}

// CleanupPublished deletes up to batchSize published rows older than cutoff.
// Rows are deleted oldest-first (by published_at) to mirror PG's
// DELETE ... ORDER BY published_at LIMIT N semantics and keep test results
// deterministic when batchSize < eligible row count.
func (s *FakeStore) CleanupPublished(_ context.Context, cutoff time.Time, batchSize int) (deleted int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range s.eligibleIDsByTimeAsc(kout.StatePublished, cutoff, func(r *fakeRow) *time.Time { return r.publishedAt }) {
		if deleted >= batchSize {
			break
		}
		delete(s.rows, id)
		deleted++
	}
	if deleted > 0 {
		s.notifyLocked()
	}
	return deleted, nil
}

// CleanupDead deletes up to batchSize dead rows older than cutoff.
// Rows are deleted oldest-first (by dead_at) for the same determinism reason
// as CleanupPublished.
func (s *FakeStore) CleanupDead(_ context.Context, cutoff time.Time, batchSize int) (deleted int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range s.eligibleIDsByTimeAsc(kout.StateDead, cutoff, func(r *fakeRow) *time.Time { return r.deadAt }) {
		if deleted >= batchSize {
			break
		}
		delete(s.rows, id)
		deleted++
	}
	if deleted > 0 {
		s.notifyLocked()
	}
	return deleted, nil
}

// CountPending returns the count of rows eligible for ClaimPending:
// status=pending AND (next_retry_at IS NULL OR next_retry_at <= now()).
// Rows in backoff (next_retry_at > now()) are excluded, consistent with the
// ClaimPending eligibility predicate. Thread-safe. May be approximate if
// calls race with ClaimPending; callers must treat errors as transient and
// skip the metric update.
func (s *FakeStore) CountPending(_ context.Context) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	var n int64
	for _, r := range s.rows {
		if r.status != kout.StatePending {
			continue
		}
		if r.nextRetryAt != nil && r.nextRetryAt.After(now) {
			continue
		}
		n++
	}
	return n, nil
}

// OldestEligibleAt returns the smallest published_at (status=kout.StatePublished)
// or dead_at (status=kout.StateDead) across all rows. Returns ok=false when no
// rows of the given status exist or all such rows have a nil timestamp.
//
// status MUST be kout.StatePublished or kout.StateDead; any other value returns
// an error.
func (s *FakeStore) OldestEligibleAt(_ context.Context, status kout.State) (time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var (
		want kout.State
		tsOf func(*fakeRow) *time.Time
	)
	switch status {
	case kout.StatePublished:
		want = kout.StatePublished
		tsOf = func(r *fakeRow) *time.Time { return r.publishedAt }
	case kout.StateDead:
		want = kout.StateDead
		tsOf = func(r *fakeRow) *time.Time { return r.deadAt }
	default:
		return time.Time{}, false, fmt.Errorf("OldestEligibleAt: invalid status %s (want StatePublished or StateDead)", status)
	}

	var (
		oldest time.Time
		found  bool
	)
	for _, r := range s.rows {
		if r.status != want {
			continue
		}
		ts := tsOf(r)
		if ts == nil {
			continue
		}
		if !found || ts.Before(oldest) {
			oldest = *ts
			found = true
		}
	}
	return oldest, found, nil
}

// eligibleIDsByTimeAsc returns IDs of rows in the given status whose timestamp
// (extracted by tsOf) is non-nil and strictly before cutoff, sorted ascending.
// Caller must hold s.mu.
func (s *FakeStore) eligibleIDsByTimeAsc(status kout.State, cutoff time.Time, tsOf func(*fakeRow) *time.Time) []string {
	type entry struct {
		id string
		ts time.Time
	}
	candidates := make([]entry, 0, len(s.rows))
	for id, r := range s.rows {
		if r.status != status {
			continue
		}
		ts := tsOf(r)
		if ts == nil || !ts.Before(cutoff) {
			continue
		}
		candidates = append(candidates, entry{id: id, ts: *ts})
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].ts.Equal(candidates[j].ts) {
			return candidates[i].id < candidates[j].id
		}
		return candidates[i].ts.Before(candidates[j].ts)
	})
	ids := make([]string, len(candidates))
	for i, c := range candidates {
		ids[i] = c.id
	}
	return ids
}

// cappedDelay caps d at maxDelay, mirroring the Go-side backoff in the PG adapter.
func cappedDelay(d, maxDelay time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d > maxDelay {
		return maxDelay
	}
	return d
}
