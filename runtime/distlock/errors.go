package distlock

import (
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Sentinel errors returned by Lock.Cause() when the lock ends.
// Callers distinguish them via errors.Is(lock.Cause(), ErrLockLost) or direct
// == comparison (markCause stores the exact package-level pointer).
// *errcode.Error has no custom Is(target error) bool method; errors.Is matches
// by package-level pointer identity. Callers that wrap with fmt.Errorf("%w",
// ErrLockLost) still work via Unwrap chain traversal. To match by Code
// regardless of pointer identity, use:
//
//	var ec *errcode.Error
//	if errors.As(err, &ec) && ec.Code == errcode.ErrDistlockLockLost { ... }
var (
	// ErrLockLost is returned by Lock.Cause() when the manager fails to renew
	// the lock or the backend reports ownership has been taken by another holder.
	ErrLockLost = errcode.New(errcode.KindConflict, errcode.ErrDistlockLockLost, "distlock: lock lost")

	// ErrLockReleased is returned by Lock.Cause() when Lock.Release() is called
	// by the application (normal end-of-critical-section).
	ErrLockReleased = errcode.New(errcode.KindConflict, errcode.ErrDistlockLockReleased, "distlock: lock released")
)

// ErrLockTimeout is a package-level alias for errcode.ErrDistlockTimeout.
// It is returned by Acquire when the key is already held by another holder.
// The canonical definition lives in pkg/errcode for cross-package matching at
// HTTP handler boundaries; this alias keeps call sites within distlock concise.
const ErrLockTimeout = errcode.ErrDistlockTimeout
