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

	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// minimalStore — tiny in-package Store for white-box tests
// (cannot import outboxtest here due to import cycle)
// ---------------------------------------------------------------------------

type minimalRow struct {
	entry       kout.Entry
	status      string
	attempts    int
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
	past := time.Now().Add(-48 * time.Hour)
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
	past := time.Now().Add(-48 * time.Hour)
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
	past := time.Now().Add(-48 * time.Hour)
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
		r.claimedAt = &now
		out = append(out, ClaimedEntry{Entry: r.entry, Attempts: r.attempts})
	}
	return out, nil
}

func (s *minimalStore) MarkPublished(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.status != "claiming" {
		return false, nil
	}
	now := time.Now()
	r.status = "published"
	r.publishedAt = &now
	r.claimedAt = nil
	return true, nil
}

func (s *minimalStore) MarkRetry(_ context.Context, id string, attempts int, nextRetryAt time.Time, lastError string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.status != "claiming" {
		return false, nil
	}
	r.status = "pending"
	r.attempts = attempts
	r.nextRetryAt = &nextRetryAt
	r.lastError = lastError
	r.claimedAt = nil
	return true, nil
}

func (s *minimalStore) MarkDead(_ context.Context, id string, attempts int, lastError string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok || r.status != "claiming" {
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
		RetentionPeriod:     1 * time.Millisecond,
		DeadRetentionPeriod: 1 * time.Millisecond,
	}.WithDefaults()
	cfg.RetentionPeriod = 1 * time.Millisecond
	cfg.DeadRetentionPeriod = 1 * time.Millisecond

	relay := &Relay{
		store:   store,
		cfg:     cfg,
		metrics: &safeRelayCollector{inner: kout.NoopRelayCollector{}},
	}

	// Use Eventually so the test remains deterministic: the 1ms retention period
	// must have elapsed before cleanup can delete entries. In practice the 48h-old
	// timestamps far predate any reasonable cutoff, so this resolves in one tick.
	require.Eventually(t, func() bool {
		if err := relay.cleanup(context.Background()); err != nil {
			return false
		}
		return store.count() == 0
	}, time.Second, 2*time.Millisecond, "both published and dead entries must be deleted")
}

func TestRelay_Cleanup_NoEntries_NoError(t *testing.T) {
	relay := &Relay{
		store:   newMinimalStore(),
		cfg:     RelayConfig{}.WithDefaults(),
		metrics: &safeRelayCollector{inner: kout.NoopRelayCollector{}},
	}
	assert.NoError(t, relay.cleanup(context.Background()))
}

// ---------------------------------------------------------------------------
// cappedDelay
// ---------------------------------------------------------------------------

func TestCappedDelay_ZeroAndNegative(t *testing.T) {
	r := &Relay{cfg: RelayConfig{MaxRetryDelay: 5 * time.Minute}.WithDefaults()}
	assert.Equal(t, time.Duration(0), r.cappedDelay(0))
	assert.Equal(t, time.Duration(0), r.cappedDelay(-1*time.Second))
}

func TestCappedDelay_CapsAtMax(t *testing.T) {
	r := &Relay{cfg: RelayConfig{MaxRetryDelay: 10 * time.Second}.WithDefaults()}
	assert.Equal(t, 10*time.Second, r.cappedDelay(20*time.Second))
	assert.Equal(t, 5*time.Second, r.cappedDelay(5*time.Second))
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
