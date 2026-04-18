// Package outbox — white-box tests for unexported relay helpers.
// These tests live in the same package (not outbox_test) so they can reach
// unexported methods (cleanup, cappedDelay, truncateError, sanitizeError).
package outbox

import (
	"context"
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

	// Sleep briefly so time.Now().Add(-1ms) is definitely after the forced past timestamps.
	time.Sleep(5 * time.Millisecond)

	err := relay.cleanup(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, store.count(), "both published and dead entries must be deleted")
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
// truncateError / sanitizeError
// ---------------------------------------------------------------------------

func TestTruncateError_UTF8Safe(t *testing.T) {
	msg := "错误消息测试用例"
	truncated := truncateError(msg, 4)
	assert.Equal(t, "错误消息", truncated)
}

func TestTruncateError_ShortMessage(t *testing.T) {
	assert.Equal(t, "short", truncateError("short", 100))
}

func TestSanitizeError_RedactsSensitive(t *testing.T) {
	msg := "dial failed: password=secret123 host=db.internal"
	sanitized := sanitizeError(msg, 1000)
	assert.NotContains(t, sanitized, "secret123")
	assert.Contains(t, sanitized, "password=<REDACTED>")
}
