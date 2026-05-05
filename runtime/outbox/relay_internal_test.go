// Package outbox — white-box tests for unexported relay helpers.
// These tests live in the same package (not outbox_test) so they can reach
// unexported methods (cleanup, cappedDelay, truncateError, sanitizeError).
package outbox

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// relayStaleAge is the age used to seed "old enough to delete" entries.
const relayStaleAge = -testtime.D24h - testtime.D24h // -48h

// relayMinRetentionInternal is the retention period that allows 48h-old entries to pass the cutoff.
const relayMinRetentionInternal = testtime.D1ms

// ---------------------------------------------------------------------------
// minimalStore — tiny in-package Store for white-box tests
// (cannot import outboxtest here due to import cycle)
// ---------------------------------------------------------------------------

type minimalRow struct {
	entry       kout.Entry
	status      string
	attempts    int
	leaseID     string
	claimedAt   *time.Time
	publishedAt *time.Time
	deadAt      *time.Time
	nextRetryAt *time.Time
	lastError   string
}

type minimalStore struct {
	mu   sync.Mutex
	rows map[string]*minimalRow
}

func newMinimalStore() *minimalStore { return &minimalStore{rows: make(map[string]*minimalRow)} }

func (s *minimalStore) seedPending(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	past := time.Now().Add(relayStaleAge)
	s.rows[id] = &minimalRow{
		entry:  kout.Entry{ID: id, EventType: "ev", Payload: []byte(`{}`), CreatedAt: past},
		status: "pending",
	}
}

func (s *minimalStore) forcePublished(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok {
		return
	}
	past := time.Now().Add(relayStaleAge)
	r.status = "published"
	r.publishedAt = &past
	r.claimedAt = nil
}

func (s *minimalStore) forceDead(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok {
		return
	}
	past := time.Now().Add(relayStaleAge)
	r.status = "dead"
	r.deadAt = &past
	r.claimedAt = nil
}

func (s *minimalStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.rows)
}

// Store interface implementation.

func (s *minimalStore) ClaimPending(_ context.Context, batchSize int) ([]ClaimedEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	leaseID := uuid.NewString()
	var out []ClaimedEntry
	for _, r := range s.rows {
		if len(out) >= batchSize {
			break
		}
		if r.status != "pending" {
			continue
		}
		now := time.Now()
		r.status = "claiming"
		r.leaseID = leaseID
		r.claimedAt = &now
		out = append(out, ClaimedEntry{Entry: r.entry, Attempts: r.attempts, LeaseID: leaseID})
	}
	return out, nil
}

func (s *minimalStore) MarkPublished(_ context.Context, id, leaseID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.status != "claiming" || r.leaseID != leaseID {
		return false, nil
	}
	now := time.Now()
	r.status = "published"
	r.publishedAt = &now
	r.claimedAt = nil
	return true, nil
}

func (s *minimalStore) MarkRetry(
	_ context.Context, id, leaseID string,
	attempts int, nextRetryAt time.Time, lastError string,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.status != "claiming" || r.leaseID != leaseID {
		return false, nil
	}
	r.status = "pending"
	r.attempts = attempts
	r.nextRetryAt = &nextRetryAt
	r.lastError = lastError
	r.claimedAt = nil
	r.leaseID = ""
	return true, nil
}

func (s *minimalStore) MarkDead(_ context.Context, id, leaseID string, attempts int, lastError string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.status != "claiming" || r.leaseID != leaseID {
		return false, nil
	}
	now := time.Now()
	r.status = "dead"
	r.attempts = attempts
	r.lastError = lastError
	r.deadAt = &now
	r.claimedAt = nil
	return true, nil
}

func (s *minimalStore) ReclaimStale(_ context.Context, claimTTL time.Duration, maxAttempts int, _, _ time.Duration) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-claimTTL)
	count := 0
	for _, r := range s.rows {
		if r.status != "claiming" || r.claimedAt == nil || !r.claimedAt.Before(cutoff) {
			continue
		}
		newAttempts := r.attempts + 1
		if newAttempts >= maxAttempts {
			now := time.Now()
			r.status = "dead"
			r.attempts = newAttempts
			r.deadAt = &now
		} else {
			r.status = "pending"
			r.attempts = newAttempts
			r.leaseID = ""
		}
		r.claimedAt = nil
		count++
	}
	return count, nil
}

func (s *minimalStore) CleanupPublished(_ context.Context, cutoff time.Time, batchSize int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for id, r := range s.rows {
		if deleted >= batchSize {
			break
		}
		if r.status == "published" && r.publishedAt != nil && r.publishedAt.Before(cutoff) {
			delete(s.rows, id)
			deleted++
		}
	}
	return deleted, nil
}

func (s *minimalStore) CleanupDead(_ context.Context, cutoff time.Time, batchSize int) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for id, r := range s.rows {
		if deleted >= batchSize {
			break
		}
		if r.status == "dead" && r.deadAt != nil && r.deadAt.Before(cutoff) {
			delete(s.rows, id)
			deleted++
		}
	}
	return deleted, nil
}

func (s *minimalStore) OldestEligibleAt(_ context.Context, status string) (time.Time, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var (
		want string
		tsOf func(*minimalRow) *time.Time
	)
	switch status {
	case "published":
		want = "published"
		tsOf = func(r *minimalRow) *time.Time { return r.publishedAt }
	case "dead":
		want = "dead"
		tsOf = func(r *minimalRow) *time.Time { return r.deadAt }
	default:
		return time.Time{}, false, fmt.Errorf("OldestEligibleAt: invalid status %q", status)
	}

	var oldest time.Time
	found := false
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

// Compile-time check.
var _ Store = (*minimalStore)(nil)

// ---------------------------------------------------------------------------
// cleanup() tests
// ---------------------------------------------------------------------------

func TestRelay_Cleanup_DeletesPublishedAndDead(t *testing.T) {
	store := newMinimalStore()
	store.seedPending("c1")
	store.seedPending("c2")
	store.forcePublished("c1")
	store.forceDead("c2")

	require.Equal(t, 2, store.count())

	// Use retention period of 1ms so all 48h-old entries are within cutoff.
	cfg := RelayConfig{
		RetentionPeriod:     relayMinRetentionInternal,
		DeadRetentionPeriod: relayMinRetentionInternal,
	}.WithDefaults()
	cfg.RetentionPeriod = relayMinRetentionInternal
	cfg.DeadRetentionPeriod = relayMinRetentionInternal

	relay := &Relay{
		store:   store,
		cfg:     cfg,
		metrics: &safeRelayCollector{inner: kout.NoopRelayCollector{}},
		clock:   clock.Real(),
	}

	// Use Eventually so the test remains deterministic: the 1ms retention period
	// must have elapsed before cleanup can delete entries. In practice the 48h-old
	// timestamps far predate any reasonable cutoff, so this resolves in one tick.
	require.Eventually(t, func() bool {
		if err := relay.cleanup(context.Background()); err != nil {
			return false
		}
		return store.count() == 0
	}, testtime.D1s, testtime.D2ms, "both published and dead entries must be deleted")
}

func TestRelay_Cleanup_NoEntries_NoError(t *testing.T) {
	relay := &Relay{
		store:   newMinimalStore(),
		cfg:     RelayConfig{}.WithDefaults(),
		metrics: &safeRelayCollector{inner: kout.NoopRelayCollector{}},
		clock:   clock.Real(),
	}
	assert.NoError(t, relay.cleanup(context.Background()))
}

// ---------------------------------------------------------------------------
// handleFailedEntry — stale-lease fail-write paths (B2-A-05)
//
// MarkRetry / MarkDead must observe RowsAffected==0 and skip stats counting
// when the lease was lost (e.g. ReclaimStale + re-Claim raced ahead). Without
// this, retry/dead counters drift while the canonical outcome is owned by a
// new lease holder.
// ---------------------------------------------------------------------------

// staleLeaseStore returns (false, nil) from MarkRetry / MarkDead — the
// fencing CAS missed. Embeds minimalStore for the rest of the Store contract.
type staleLeaseStore struct {
	*minimalStore
	markRetryCalls int
	markDeadCalls  int
}

func (s *staleLeaseStore) MarkRetry(_ context.Context, _, _ string, _ int, _ time.Time, _ string) (bool, error) {
	s.markRetryCalls++
	return false, nil
}

func (s *staleLeaseStore) MarkDead(_ context.Context, _, _ string, _ int, _ string) (bool, error) {
	s.markDeadCalls++
	return false, nil
}

func TestRelay_HandleFailedEntry_StaleLease_RetryNotCounted(t *testing.T) {
	store := &staleLeaseStore{minimalStore: newMinimalStore()}
	relay := &Relay{
		store:   store,
		cfg:     RelayConfig{MaxAttempts: 5, BaseRetryDelay: testtime.D1ms, MaxRetryDelay: testtime.D5s}.WithDefaults(),
		metrics: &safeRelayCollector{inner: kout.NoopRelayCollector{}},
		clock:   clock.Real(),
	}

	res := publishResult{
		entry: ClaimedEntry{
			Entry:    kout.Entry{ID: "e-stale", EventType: "ev", Topic: "t"},
			Attempts: 1,
			LeaseID:  uuid.NewString(),
		},
		err: fmt.Errorf("transient broker error"),
	}
	var stats pollStats

	err := relay.handleFailedEntry(context.Background(), res, &stats)
	require.NoError(t, err, "stale lease must not surface as error")
	assert.Equal(t, 1, store.markRetryCalls, "MarkRetry must be invoked once")
	assert.Zero(t, stats.retried, "stats.retried must NOT be incremented when lease was lost")
	assert.Zero(t, stats.dead, "stats.dead must NOT be incremented")
}

// stalePublishStore returns (false, nil) from MarkPublished — the fencing
// CAS missed even though publish succeeded. Mirrors the failure-path harness
// (staleLeaseStore) so the success-path stale-lease branch in writeBackResults
// has equivalent direct coverage.
type stalePublishStore struct {
	*minimalStore
	markPublishedCalls int
}

func (s *stalePublishStore) MarkPublished(_ context.Context, _, _ string) (bool, error) {
	s.markPublishedCalls++
	return false, nil
}

// TestRelay_WriteBack_PublishSuccess_StaleLease_NotCountedAsPublished verifies
// the success-path stale-lease branch in Relay.writeBackResults: when publish
// succeeded but MarkPublished returns updated=false (lease was reclaimed
// between publish and writeBack), the entry must be classified as skipped, not
// published, and no error must surface (at-least-once delivery guarantee).
//
// PR #373 follow-up #4 — failure paths were already covered by
// TestRelay_HandleFailedEntry_StaleLease_{Retry,Dead}NotCounted; the success
// path closes the matrix.
func TestRelay_WriteBack_PublishSuccess_StaleLease_NotCountedAsPublished(t *testing.T) {
	store := &stalePublishStore{minimalStore: newMinimalStore()}
	relay := &Relay{
		store:   store,
		cfg:     RelayConfig{MaxAttempts: 5, BaseRetryDelay: testtime.D1ms, MaxRetryDelay: testtime.D5s}.WithDefaults(),
		metrics: &safeRelayCollector{inner: kout.NoopRelayCollector{}},
		clock:   clock.Real(),
	}

	results := []publishResult{{
		entry: ClaimedEntry{
			Entry:    kout.Entry{ID: "e-publish-stale", EventType: "ev", Topic: "t"},
			Attempts: 1,
			LeaseID:  uuid.NewString(),
		},
		// nil err = publish succeeded
	}}

	stats, err := relay.writeBackResults(context.Background(), results)
	require.NoError(t, err, "stale lease on success path must not surface as error")
	assert.Equal(t, 1, store.markPublishedCalls, "MarkPublished must be invoked once")
	assert.Zero(t, stats.published, "stats.published must NOT increment when lease was lost")
	assert.Equal(t, 1, stats.skipped, "stats.skipped must increment to record the silent drop")
	assert.Zero(t, stats.retried)
	assert.Zero(t, stats.dead)
}

func TestRelay_HandleFailedEntry_StaleLease_DeadNotCounted(t *testing.T) {
	store := &staleLeaseStore{minimalStore: newMinimalStore()}
	relay := &Relay{
		store:   store,
		cfg:     RelayConfig{MaxAttempts: 3, BaseRetryDelay: testtime.D1ms, MaxRetryDelay: testtime.D5s}.WithDefaults(),
		metrics: &safeRelayCollector{inner: kout.NoopRelayCollector{}},
		clock:   clock.Real(),
	}

	res := publishResult{
		entry: ClaimedEntry{
			Entry:    kout.Entry{ID: "e-stale-dead", EventType: "ev", Topic: "t"},
			Attempts: 2, // newAttempts=3 == MaxAttempts → MarkDead branch
			LeaseID:  uuid.NewString(),
		},
		err: fmt.Errorf("permanent failure"),
	}
	var stats pollStats

	err := relay.handleFailedEntry(context.Background(), res, &stats)
	require.NoError(t, err)
	assert.Equal(t, 1, store.markDeadCalls, "MarkDead must be invoked once")
	assert.Zero(t, stats.dead, "stats.dead must NOT be incremented when lease was lost")
	assert.Zero(t, stats.retried)
}

// ---------------------------------------------------------------------------
// cappedDelay
// ---------------------------------------------------------------------------

func TestCappedDelay_ZeroAndNegative(t *testing.T) {
	r := &Relay{cfg: RelayConfig{MaxRetryDelay: testtime.D5min}.WithDefaults(), clock: clock.Real()}
	assert.Equal(t, time.Duration(0), r.cappedDelay(0))
	assert.Equal(t, time.Duration(0), r.cappedDelay(testtime.DNeg1s))
}

func TestCappedDelay_CapsAtMax(t *testing.T) {
	r := &Relay{cfg: RelayConfig{MaxRetryDelay: testtime.D10s}.WithDefaults(), clock: clock.Real()}
	assert.Equal(t, testtime.D10s, r.cappedDelay(testtime.D20s))
	assert.Equal(t, testtime.D5s, r.cappedDelay(testtime.D5s))
}

// ---------------------------------------------------------------------------
// TruncateError / SanitizeError
// ---------------------------------------------------------------------------

func TestTruncateError_UTF8Safe(t *testing.T) {
	msg := "错误消息测试用例"
	truncated := TruncateError(msg, 4)
	assert.Equal(t, "错误消息", truncated)
}

func TestTruncateError_ShortMessage(t *testing.T) {
	assert.Equal(t, "short", TruncateError("short", 100))
}

func TestSanitizeError_RedactsSensitive(t *testing.T) {
	msg := "dial failed: password=secret123 host=db.internal"
	sanitized := SanitizeError(msg, 1000)
	assert.NotContains(t, sanitized, "secret123")
	assert.Contains(t, sanitized, "password=<REDACTED>")
}
