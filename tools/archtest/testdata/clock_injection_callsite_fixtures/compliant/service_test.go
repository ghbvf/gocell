// Package compliant — test callsite that correctly includes WithClock (no violation).
package compliant

import "testing"

// stubClock is a minimal Clock stub for tests.
type stubClock struct{}

func (stubClock) Now() interface{} { return nil }

func TestServiceWithClock(t *testing.T) {
	// COMPLIANT: NewService is called with WithClock present.
	_ = NewService(
		WithName("test"),
		WithClock(stubClock{}),
	)
}
