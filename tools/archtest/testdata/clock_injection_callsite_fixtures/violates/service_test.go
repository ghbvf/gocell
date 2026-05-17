// Package violates — test callsite that omits WithClock (violation).
package violates

import (
	"testing"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func TestServiceWithoutClock(t *testing.T) {
	// VIOLATION: NewService is called with options but WithClock is missing.
	spec.Violation()
	_ = NewService(
		WithName("test"),
	)
}
