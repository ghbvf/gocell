package idempotency

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Compile-time interface check.

type mockChecker struct {
	processed map[string]bool
}

func newMockChecker() *mockChecker {
	return &mockChecker{processed: make(map[string]bool)}
}

func (m *mockChecker) IsProcessed(_ context.Context, key string) (bool, error) {
	return m.processed[key], nil
}

func (m *mockChecker) MarkProcessed(_ context.Context, key string, _ time.Duration) error {
	m.processed[key] = true
	return nil
}

func (m *mockChecker) TryProcess(_ context.Context, key string, _ time.Duration) (bool, error) {
	if m.processed[key] {
		return false, nil
	}
	m.processed[key] = true
	return true, nil
}

var _ Checker = (*mockChecker)(nil)

func TestCheckerInterface(t *testing.T) {
	var c Checker = newMockChecker()
	ok, err := c.IsProcessed(context.Background(), "test-key")
	assert.NoError(t, err)
	assert.False(t, ok)
}

func TestCheckerInterface_TryProcess(t *testing.T) {
	var c Checker = newMockChecker()

	// First call should return true (caller should process).
	shouldProcess, err := c.TryProcess(context.Background(), "test-key", DefaultTTL)
	assert.NoError(t, err)
	assert.True(t, shouldProcess)

	// Second call should return false (already processed).
	shouldProcess, err = c.TryProcess(context.Background(), "test-key", DefaultTTL)
	assert.NoError(t, err)
	assert.False(t, shouldProcess)
}
