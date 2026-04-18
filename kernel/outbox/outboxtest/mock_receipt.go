package outboxtest

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// Compile-time check.
var _ outbox.Receipt = (*MockReceipt)(nil)

// MockReceipt records Commit/Release calls for assertion in tests.
// Thread-safe via atomic counters.
type MockReceipt struct {
	committed   atomic.Bool
	released    atomic.Bool
	commitCalls atomic.Int32
	commitErr   error
	releaseErr  error
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
	r.commitCalls.Add(1)
	return r.commitErr
}

// CommitCount returns the number of times Commit was called. Used by
// regression tests that need to verify retries on Commit failure.
func (r *MockReceipt) CommitCount() int32 {
	return r.commitCalls.Load()
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

// Extend is a no-op stub that always succeeds. Override in tests that need
// to exercise lease-renewal behavior.
func (r *MockReceipt) Extend(_ context.Context, _ time.Duration) error {
	return nil
}
