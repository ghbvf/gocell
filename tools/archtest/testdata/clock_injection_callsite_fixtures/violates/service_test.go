// Package violates — test callsite that omits WithClock (violation).
package violates

import "testing"

func TestServiceWithoutClock(t *testing.T) {
	// VIOLATION: NewService is called with options but WithClock is missing.
	_ = NewService(
		WithName("test"),
	)
}
