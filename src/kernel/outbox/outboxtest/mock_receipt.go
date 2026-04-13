package outboxtest

import (
	"context"
	"sync/atomic"

	"github.com/ghbvf/gocell/kernel/idempotency"
)

// Compile-time check.
var _ idempotency.Receipt = (*MockReceipt)(nil)

// MockReceipt records Commit/Release calls for assertion in tests.
// Thread-safe via atomic.Bool.
type MockReceipt struct {
	committed  atomic.Bool
	released   atomic.Bool
	commitErr  error
	releaseErr error
}

// NewMockReceipt creates a MockReceipt that succeeds on Commit/Release.
func NewMockReceipt() *MockReceipt {
	return &MockReceipt{}
}

// NewMockReceiptWithErrors creates a MockReceipt that returns the given errors.
func NewMockReceiptWithErrors(commitErr, releaseErr error) *MockReceipt {
	return &MockReceipt{commitErr: commitErr, releaseErr: releaseErr}
}

// Commit marks the receipt as committed.
func (r *MockReceipt) Commit(_ context.Context) error {
	r.committed.Store(true)
	return r.commitErr
}

// Release marks the receipt as released.
func (r *MockReceipt) Release(_ context.Context) error {
	r.released.Store(true)
	return r.releaseErr
}

// Committed reports whether Commit was called.
func (r *MockReceipt) Committed() bool {
	return r.committed.Load()
}

// Released reports whether Release was called.
func (r *MockReceipt) Released() bool {
	return r.released.Load()
}
