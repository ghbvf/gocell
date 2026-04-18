package distlock_test

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/distlock"
)

// lockerShapeCheck is a compile-time assertion type that verifies Locker has
// the correct method set. The variable is never read at runtime.
type lockerShapeCheck struct{}

func (lockerShapeCheck) Acquire(_ context.Context, _ string, _ time.Duration) (distlock.Lock, error) {
	return nil, nil
}

// lockShapeCheck is a compile-time assertion type that verifies Lock has the
// correct method set.
type lockShapeCheck struct{}

func (lockShapeCheck) Release(_ context.Context) error { return nil }
func (lockShapeCheck) Key() string                     { return "" }
func (lockShapeCheck) Lost() <-chan struct{}           { return nil }

// Compile-time interface satisfaction checks. These fail to compile if the
// method sets diverge, which is the primary intent of these tests.
var (
	_ distlock.Locker = lockerShapeCheck{}
	_ distlock.Lock   = lockShapeCheck{}
)

// TestLocker_InterfaceShape asserts Acquire(ctx, key, ttl) (Lock, error) exists.
// The real assertion is the compile-time var block above; the test body
// confirms the types appear in test output.
func TestLocker_InterfaceShape(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "Locker_has_Acquire_method"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Assign via interface variable so the type is exercised at runtime.
			// Avoid nil comparison (SA4023 — concrete type is never nil).
			_ = tc.name
			var locker distlock.Locker = lockerShapeCheck{}
			_ = locker
		})
	}
}

// TestLock_InterfaceShape asserts Release / Key / Lost methods exist on Lock.
func TestLock_InterfaceShape(t *testing.T) {
	tests := []struct {
		name string
	}{
		{name: "Lock_has_Release_method"},
		{name: "Lock_has_Key_method"},
		{name: "Lock_has_Lost_method"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_ = tc.name
			var lock distlock.Lock = lockShapeCheck{}
			_ = lock
		})
	}
}
