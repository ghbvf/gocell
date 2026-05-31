package distlock_test

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
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
	t.Run("ErrLockOrphaned_NotNil", func(t *testing.T) {
		if distlock.ErrLockOrphaned == nil {
			t.Fatal("ErrLockOrphaned must not be nil")
		}
	})
	t.Run("ErrLockLost_Distinct_FromErrLockReleased", func(t *testing.T) {
		if errors.Is(distlock.ErrLockLost, distlock.ErrLockReleased) {
			t.Fatal("ErrLockLost and ErrLockReleased must be distinct sentinels")
		}
	})
	t.Run("ErrLockOrphaned_Distinct_FromSiblings", func(t *testing.T) {
		if errors.Is(distlock.ErrLockOrphaned, distlock.ErrLockLost) {
			t.Fatal("ErrLockOrphaned and ErrLockLost must be distinct sentinels")
		}
		if errors.Is(distlock.ErrLockOrphaned, distlock.ErrLockReleased) {
			t.Fatal("ErrLockOrphaned and ErrLockReleased must be distinct sentinels")
		}
	})
	t.Run("ErrLockTimeout_StableValue", func(t *testing.T) {
		if distlock.ErrLockTimeout != "ERR_DISTLOCK_TIMEOUT" {
			t.Errorf("ErrLockTimeout = %q, want %q", distlock.ErrLockTimeout, "ERR_DISTLOCK_TIMEOUT")
		}
	})
	// ErrLockOrphaned must unwrap to *errcode.Error with KindInternal and
	// code ERR_LOCK_ORPHANED. KindInternal is chosen to match
	// ErrLockReleased's fail-closed semantics: an orphaned lock surfacing to
	// an HTTP handler is a server-side programming bug, not an external
	// conflict — 500 is preferable to a misleading 409.
	t.Run("ErrLockOrphaned_UnwrapsToErrcode", func(t *testing.T) {
		var ec *errcode.Error
		if !errors.As(distlock.ErrLockOrphaned, &ec) {
			t.Fatalf("ErrLockOrphaned must unwrap to *errcode.Error; got %T", distlock.ErrLockOrphaned)
		}
		if ec.Code != errcode.ErrDistlockLockOrphaned {
			t.Errorf("ErrLockOrphaned code = %q, want %q", ec.Code, errcode.ErrDistlockLockOrphaned)
		}
		if ec.Kind != errcode.KindInternal {
			t.Errorf("ErrLockOrphaned kind = %v, want KindInternal", ec.Kind)
		}
	})
}
