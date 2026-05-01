// Package eventually_named_const_passes verifies that a "require.Eventually"-
// shaped call whose timeout/poll-interval args are package-level named consts
// passes the TEST-TIME-LITERAL-01 gate. 0 violations expected.
package eventually_named_const_passes

import (
	"testing"
	"time"
)

const (
	eventuallyTimeout      = 3 * time.Second
	eventuallyPollInterval = 50 * time.Millisecond
)

// fakeEventually mirrors the testify signature without the dependency.
func fakeEventually(t *testing.T, condition func() bool, waitFor, tick time.Duration, msgAndArgs ...any) bool {
	t.Helper()
	_, _, _ = condition, waitFor, tick
	return true
}

func TestEventually(t *testing.T) {
	fakeEventually(t, func() bool { return true }, eventuallyTimeout, eventuallyPollInterval, "ready")
}
