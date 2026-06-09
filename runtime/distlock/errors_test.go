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
		assertLockLostNotNil(t)
	})
	t.Run("ErrLockReleased_NotNil", func(t *testing.T) {
		assertLockReleasedNotNil(t)
	})
	t.Run("ErrLockOrphaned_NotNil", func(t *testing.T) {
		assertLockOrphanedNotNil(t)
	})
	t.Run("ErrLockLost_Distinct_FromErrLockReleased", func(t *testing.T) {
		assertLockLostDistinctFromReleased(t)
	})
	t.Run("ErrLockOrphaned_Distinct_FromSiblings", func(t *testing.T) {
		assertLockOrphanedDistinctFromSiblings(t)
	})
	t.Run("ErrLockTimeout_StableValue", func(t *testing.T) {
		assertLockTimeoutStableValue(t)
	})
	// ErrLockOrphaned must unwrap to *errcode.Error with KindInternal and
	// code ERR_DISTLOCK_LOCK_ORPHANED. KindInternal is chosen to match
	// ErrLockReleased's fail-closed semantics: an orphaned lock surfacing to
	// an HTTP handler is a server-side programming bug, not an external
	// conflict — 500 is preferable to a misleading 409.
	t.Run("ErrLockOrphaned_UnwrapsToErrcode", func(t *testing.T) {
		assertLockOrphanedUnwrapsToErrcode(t)
	})
}

func assertLockLostNotNil(t *testing.T) {
	t.Helper()
	if distlock.ErrLockLost == nil {
		t.Fatal("ErrLockLost must not be nil")
	}
}

func assertLockReleasedNotNil(t *testing.T) {
	t.Helper()
	if distlock.ErrLockReleased == nil {
		t.Fatal("ErrLockReleased must not be nil")
	}
}

func assertLockOrphanedNotNil(t *testing.T) {
	t.Helper()
	if distlock.ErrLockOrphaned == nil {
		t.Fatal("ErrLockOrphaned must not be nil")
	}
}

func assertLockLostDistinctFromReleased(t *testing.T) {
	t.Helper()
	if errors.Is(distlock.ErrLockLost, distlock.ErrLockReleased) {
		t.Fatal("ErrLockLost and ErrLockReleased must be distinct sentinels")
	}
}

func assertLockOrphanedDistinctFromSiblings(t *testing.T) {
	t.Helper()
	if errors.Is(distlock.ErrLockOrphaned, distlock.ErrLockLost) {
		t.Fatal("ErrLockOrphaned and ErrLockLost must be distinct sentinels")
	}
	if errors.Is(distlock.ErrLockOrphaned, distlock.ErrLockReleased) {
		t.Fatal("ErrLockOrphaned and ErrLockReleased must be distinct sentinels")
	}
}

func assertLockTimeoutStableValue(t *testing.T) {
	t.Helper()
	if distlock.ErrLockTimeout != "ERR_DISTLOCK_TIMEOUT" {
		t.Errorf("ErrLockTimeout = %q, want %q", distlock.ErrLockTimeout, "ERR_DISTLOCK_TIMEOUT")
	}
}

func assertLockOrphanedUnwrapsToErrcode(t *testing.T) {
	t.Helper()
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
}
