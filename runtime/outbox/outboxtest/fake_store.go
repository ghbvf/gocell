// Package outboxtest provides a public in-memory Store implementation and a
// Store conformance test suite for use in unit tests.
//
// FakeStore implements runtime/outbox.Store in memory and is intended for unit
// tests in cells and runtime/outbox. The conformance suite (RunStoreConformanceSuite)
// verifies that any Store implementation produces identical observable behaviour.
package outboxtest

import (
	"context"
	"sort"
	"sync"
	"time"

	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/outbox"
)

// Compile-time assertion: FakeStore must satisfy outbox.Store.
var _ outbox.Store = (*FakeStore)(nil)

// rowStatus mirrors the four-state machine used by the PG adapter.
type rowStatus string

const (
	statusPending   rowStatus = "pending"
	statusClaiming  rowStatus = "claiming"
	statusPublished rowStatus = "published"
	statusDead      rowStatus = "dead"
)

// fakeRow holds the full mutable state of a single outbox entry in FakeStore.
type fakeRow struct {
	entry       kout.Entry
	status      rowStatus
	attempts    int
	claimedAt   *time.Time
	publishedAt *time.Time
	deadAt      *time.Time
	nextRetryAt *time.Time
	lastError   string
}

// FakeRow is a read-only snapshot of a fakeRow, returned by Snapshot.
// For tests only, not for production use.
type FakeRow struct {
	Entry       kout.Entry
	Status      string
	Attempts    int
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
	mu   sync.Mutex
	rows map[string]*fakeRow
	now  func() time.Time
}

// NewFakeStore creates an empty FakeStore using time.Now as clock.
func NewFakeStore() *FakeStore {
	return &FakeStore{
		rows: make(map[string]*fakeRow),
		now:  time.Now,
	}
}

// WithClock replaces the clock (useful for ReclaimStale / Cleanup tests).
func (s *FakeStore) WithClock(now func() time.Time) *FakeStore {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
	return s
}

// Seed inserts rows directly (bypasses normal Writer). Used by test setup.
// Rows with status "" default to "pending". Existing rows with the same ID
// are overwritten.
func (s *FakeStore) Seed(entries ...outbox.ClaimedEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ce := range entries {
		row := &fakeRow{
			entry:    ce.Entry,
			status:   statusPending,
			attempts: ce.Attempts,
		}
		s.rows[ce.ID] = row
	}
}

// Snapshot returns a sorted (by ID) copy of all rows for test assertions.
func (s *FakeStore) Snapshot() []FakeRow {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]FakeRow, 0, len(s.rows))
	for _, r := range s.rows {
		fr := FakeRow{
			Entry:     r.entry,
			Status:    string(r.status),
			Attempts:  r.attempts,
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
		return out[i].Entry.ID < out[j].Entry.ID
	})
	return out
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
		if r.status != statusPending {
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
			return ri.entry.CreatedAt.Before(rj.entry.CreatedAt)
		case ri.nextRetryAt == nil:
			return true
		case rj.nextRetryAt == nil:
			return false
		default:
			if ri.nextRetryAt.Equal(*rj.nextRetryAt) {
				return ri.entry.CreatedAt.Before(rj.entry.CreatedAt)
			}
			return ri.nextRetryAt.Before(*rj.nextRetryAt)
		}
	})

	if len(candidates) > batchSize {
		candidates = candidates[:batchSize]
	}

	result := make([]outbox.ClaimedEntry, 0, len(candidates))
	for _, r := range candidates {
		r.status = statusClaiming
		t := now
		r.claimedAt = &t
		result = append(result, outbox.ClaimedEntry{
			Entry:    r.entry,
			Attempts: r.attempts,
		})
	}
	return result, nil
}

// MarkPublished transitions an entry from claiming to published.
// updated=false when the row was reclaimed (no longer in claiming status).
func (s *FakeStore) MarkPublished(_ context.Context, id string) (updated bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rows[id]
	if !ok || r.status != statusClaiming {
		return false, nil
	}
	now := s.now()
	r.status = statusPublished
	r.publishedAt = &now
	r.claimedAt = nil
	return true, nil
}

// MarkRetry transitions a failing entry back to pending.
// updated=false when the row is no longer in claiming status.
func (s *FakeStore) MarkRetry(_ context.Context, id string, attempts int, nextRetryAt time.Time, lastError string) (updated bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rows[id]
	if !ok || r.status != statusClaiming {
		return false, nil
	}
	r.status = statusPending
	r.attempts = attempts
	r.nextRetryAt = &nextRetryAt
	r.lastError = lastError
	r.claimedAt = nil
	return true, nil
}

// MarkDead transitions a failing entry to dead.
// updated=false when the row is no longer in claiming status.
func (s *FakeStore) MarkDead(_ context.Context, id string, attempts int, lastError string) (updated bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.rows[id]
	if !ok || r.status != statusClaiming {
		return false, nil
	}
	now := s.now()
	r.status = statusDead
	r.attempts = attempts
	r.lastError = lastError
	r.deadAt = &now
	r.claimedAt = nil
	return true, nil
}

// ReclaimStale transitions claiming rows whose claimed_at is older than claimTTL
// back to pending or to dead (when attempts+1 >= maxAttempts).
// Returns count of rows recovered across both destinations.
func (s *FakeStore) ReclaimStale(_ context.Context, claimTTL time.Duration, maxAttempts int, baseDelay, maxDelay time.Duration) (count int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	cutoff := now.Add(-claimTTL)

	for _, r := range s.rows {
		if r.status != statusClaiming {
			continue
		}
		if r.claimedAt == nil || !r.claimedAt.Before(cutoff) {
			continue
		}
		newAttempts := r.attempts + 1
		if newAttempts >= maxAttempts {
			r.status = statusDead
			r.attempts = newAttempts
			r.deadAt = &now
			r.claimedAt = nil
		} else {
			delay := cappedDelay(baseDelay*(1<<newAttempts), maxDelay)
			nextRetry := now.Add(delay)
			r.status = statusPending
			r.attempts = newAttempts
			r.nextRetryAt = &nextRetry
			r.claimedAt = nil
		}
		count++
	}
	return count, nil
}

// CleanupPublished deletes up to batchSize published rows older than cutoff.
func (s *FakeStore) CleanupPublished(_ context.Context, cutoff time.Time, batchSize int) (deleted int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, r := range s.rows {
		if deleted >= batchSize {
			break
		}
		if r.status == statusPublished && r.publishedAt != nil && r.publishedAt.Before(cutoff) {
			delete(s.rows, id)
			deleted++
		}
	}
	return deleted, nil
}

// CleanupDead deletes up to batchSize dead rows older than cutoff.
func (s *FakeStore) CleanupDead(_ context.Context, cutoff time.Time, batchSize int) (deleted int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for id, r := range s.rows {
		if deleted >= batchSize {
			break
		}
		if r.status == statusDead && r.deadAt != nil && r.deadAt.Before(cutoff) {
			delete(s.rows, id)
			deleted++
		}
	}
	return deleted, nil
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
