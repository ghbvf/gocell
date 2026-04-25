package distlock_test

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/runtime/distlock"
)

// TestErrors_Sentinels verifies the sentinel errors are distinct, non-nil,
// and matchable via errors.Is.
func TestErrors_Sentinels(t *testing.T) {
	t.Run("ErrLockLost_NotNil", func(t *testing.T) {
		if distlock.ErrLockLost == nil {
			t.Fatal("ErrLockLost must not be nil")
		}
	})
	t.Run("ErrLockReleased_NotNil", func(t *testing.T) {
		if distlock.ErrLockReleased == nil {
			t.Fatal("ErrLockReleased must not be nil")
		}
	})
	t.Run("ErrLockLost_Distinct_FromErrLockReleased", func(t *testing.T) {
		if errors.Is(distlock.ErrLockLost, distlock.ErrLockReleased) {
			t.Fatal("ErrLockLost and ErrLockReleased must be distinct sentinels")
		}
	})
	t.Run("ErrLockTimeout_StableValue", func(t *testing.T) {
		if distlock.ErrLockTimeout != "ERR_DISTLOCK_TIMEOUT" {
			t.Errorf("ErrLockTimeout = %q, want %q", distlock.ErrLockTimeout, "ERR_DISTLOCK_TIMEOUT")
		}
	})
}
