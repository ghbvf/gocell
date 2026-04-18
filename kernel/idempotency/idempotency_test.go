package idempotency

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// --- ClaimState Tests ---

func TestClaimState_Values(t *testing.T) {
	assert.Equal(t, ClaimState(0), ClaimAcquired)
	assert.Equal(t, ClaimState(1), ClaimDone)
	assert.Equal(t, ClaimState(2), ClaimBusy)
}

// --- Claimer Interface Test ---

type mockClaimer struct {
	state   ClaimState
	receipt Receipt
	err     error
}

type mockReceipt struct{}

func (mockReceipt) Commit(context.Context) error                    { return nil }
func (mockReceipt) Release(context.Context) error                   { return nil }
func (mockReceipt) Extend(_ context.Context, _ time.Duration) error { return nil }

var _ Receipt = mockReceipt{}

func (m *mockClaimer) Claim(_ context.Context, _ string, _, _ time.Duration) (ClaimState, Receipt, error) {
	return m.state, m.receipt, m.err
}

var _ Claimer = (*mockClaimer)(nil)

func TestClaimerInterface(t *testing.T) {
	var c Claimer = &mockClaimer{state: ClaimAcquired, receipt: mockReceipt{}}
	state, _, err := c.Claim(context.Background(), "test-key", DefaultLeaseTTL, DefaultTTL)
	assert.NoError(t, err)
	assert.Equal(t, ClaimAcquired, state)
}

func TestReceipt_CommitRelease(t *testing.T) {
	var receipt Receipt = mockReceipt{}
	assert.NoError(t, receipt.Commit(context.Background()))
	assert.NoError(t, receipt.Release(context.Background()))
}

// --- DefaultLeaseTTL Test ---

func TestDefaultLeaseTTL(t *testing.T) {
	assert.Equal(t, 5*time.Minute, DefaultLeaseTTL)
}

func TestDefaultTTL(t *testing.T) {
	assert.Equal(t, 24*time.Hour, DefaultTTL)
}
