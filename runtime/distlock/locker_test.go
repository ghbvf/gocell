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

// TestInterfaces_CompileTimeAssertions ensures the test binary links and the
// package is exercised by "go test ./runtime/distlock/...". The real gate is
// the compile-time var block above — if the method sets diverge the binary
// will not build.
func TestInterfaces_CompileTimeAssertions(t *testing.T) {
	// Compile-time assertions live at package scope above.
	// This runtime stub ensures the test binary links and the package is
	// covered by "go test ./runtime/distlock/...".
	t.Log("interface assertions compiled")
}
