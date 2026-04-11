package idempotency

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
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

func (m *mockChecker) Release(_ context.Context, key string) error {
	delete(m.processed, key)
	return nil
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

// --- ClaimState Tests ---

func TestClaimState_Values(t *testing.T) {
	assert.Equal(t, ClaimState(0), ClaimAcquired)
	assert.Equal(t, ClaimState(1), ClaimDone)
	assert.Equal(t, ClaimState(2), ClaimBusy)
}

// --- Claimer Interface Test ---

type mockClaimer struct {
	state   ClaimState
	receipt outbox.Receipt
	err     error
}

func (m *mockClaimer) Claim(_ context.Context, _ string, _, _ time.Duration) (ClaimState, outbox.Receipt, error) {
	return m.state, m.receipt, m.err
}

var _ Claimer = (*mockClaimer)(nil)

func TestClaimerInterface(t *testing.T) {
	var c Claimer = &mockClaimer{state: ClaimAcquired}
	state, _, err := c.Claim(context.Background(), "test-key", DefaultLeaseTTL, DefaultTTL)
	assert.NoError(t, err)
	assert.Equal(t, ClaimAcquired, state)
}

// --- DefaultLeaseTTL Test ---

func TestDefaultLeaseTTL(t *testing.T) {
	assert.Equal(t, 5*time.Minute, DefaultLeaseTTL)
}

func TestDefaultTTL(t *testing.T) {
	assert.Equal(t, 24*time.Hour, DefaultTTL)
}
