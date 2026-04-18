package outbox_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/runtime/outbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// capturingSlogHandler captures log records for assertion in tests.
// ---------------------------------------------------------------------------

type capturingSlogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingSlogHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *capturingSlogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}

func (h *capturingSlogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *capturingSlogHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *capturingSlogHandler) countByLevel(level slog.Level) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, r := range h.records {
		if r.Level == level {
			count++
		}
	}
	return count
}

// ---------------------------------------------------------------------------
// TestFailureBudget_BelowThreshold_NotTripped
// ---------------------------------------------------------------------------

func TestFailureBudget_BelowThreshold_NotTripped(t *testing.T) {
	const threshold = 5
	fb := outbox.NewFailureBudget("test", threshold)

	for i := range threshold - 1 {
		fb.Record(errors.New("err"))
		assert.Falsef(t, fb.Tripped(), "should not trip after %d failures", i+1)
		assert.Nilf(t, fb.Checker()(), "Checker should return nil before threshold, got error after %d failures", i+1)
	}

	assert.Equal(t, int64(threshold-1), fb.ConsecutiveFailures())
}

// ---------------------------------------------------------------------------
// TestFailureBudget_AtThreshold_Trips
// ---------------------------------------------------------------------------

func TestFailureBudget_AtThreshold_Trips(t *testing.T) {
	const threshold = 3
	fb := outbox.NewFailureBudget("relay-test", threshold)

	for range threshold {
		fb.Record(errors.New("err"))
	}

	assert.True(t, fb.Tripped(), "should trip exactly at threshold")
	require.NotNil(t, fb.Checker(), "Checker must not be nil when tripped")
	checkerErr := fb.Checker()()
	require.Error(t, checkerErr)
	assert.Contains(t, checkerErr.Error(), "relay-test")
}

// ---------------------------------------------------------------------------
// TestFailureBudget_SuccessResets
// ---------------------------------------------------------------------------

func TestFailureBudget_SuccessResets(t *testing.T) {
	const threshold = 3
	fb := outbox.NewFailureBudget("reset-test", threshold)

	for range threshold {
		fb.Record(errors.New("err"))
	}
	require.True(t, fb.Tripped())

	fb.Record(nil)

	assert.False(t, fb.Tripped(), "success must reset tripped state")
	assert.Equal(t, int64(0), fb.ConsecutiveFailures(), "success must zero consecutive failures")
	assert.Nil(t, fb.Checker()(), "Checker must return nil after reset")
}

// ---------------------------------------------------------------------------
// TestFailureBudget_ThresholdZero_Disabled
// ---------------------------------------------------------------------------

func TestFailureBudget_ThresholdZero_Disabled(t *testing.T) {
	fb := outbox.NewFailureBudget("disabled", 0)

	for range 100 {
		fb.Record(errors.New("err"))
	}

	assert.False(t, fb.Tripped(), "threshold=0 must never trip")
	assert.Nil(t, fb.Checker(), "threshold=0 Checker must return nil function")
}

// ---------------------------------------------------------------------------
// TestFailureBudget_Concurrent
// ---------------------------------------------------------------------------

func TestFailureBudget_Concurrent(t *testing.T) {
	// 50 goroutines × 100 Record calls each with error — no success resets.
	// After all goroutines finish, the budget must be tripped (5000 errors >> threshold 10).
	// Primary goal: no data race under -race.
	const (
		goroutines = 50
		iterations = 100
	)

	fb := outbox.NewFailureBudget("concurrent", 10)

	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iterations {
				fb.Record(errors.New("err"))
			}
		}()
	}
	wg.Wait()

	// All goroutines sent errors — budget must be tripped.
	assert.True(t, fb.Tripped(), "budget must be tripped after all-error run")
	assert.GreaterOrEqual(t, fb.ConsecutiveFailures(), int64(10),
		"consecutive failures must be >= threshold")
}

// ---------------------------------------------------------------------------
// TestFailureBudget_TripAndRecover_LogsOnce
// ---------------------------------------------------------------------------

func TestFailureBudget_TripAndRecover_LogsOnce(t *testing.T) {
	const threshold = 3
	h := &capturingSlogHandler{}
	logger := slog.New(h)
	fb := outbox.NewFailureBudgetWithLogger("log-test", threshold, logger)

	// Trip: should log exactly one Warn.
	for range threshold {
		fb.Record(errors.New("err"))
	}
	assert.Equal(t, 1, h.countByLevel(slog.LevelWarn),
		"first trip must log exactly one Warn")

	// More failures: no additional Warn logs.
	for range 5 {
		fb.Record(errors.New("err"))
	}
	assert.Equal(t, 1, h.countByLevel(slog.LevelWarn),
		"repeated failures after trip must not re-log Warn")

	// Recover: should log exactly one Info.
	fb.Record(nil)
	assert.Equal(t, 1, h.countByLevel(slog.LevelInfo),
		"first recovery must log exactly one Info")

	// More successes: no additional Info logs.
	for range 5 {
		fb.Record(nil)
	}
	assert.Equal(t, 1, h.countByLevel(slog.LevelInfo),
		"repeated successes after recover must not re-log Info")
}
