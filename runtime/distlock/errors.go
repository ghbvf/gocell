package distlock

import (
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Sentinel errors used as context.Cause values on the lock-derived context.
// Callers distinguish them via errors.Is(context.Cause(lockCtx), ErrLockLost)
// or direct == comparison (context.Cause returns the exact pointer stored by
// state.cancel). *errcode.Error satisfies both: interface equality is pointer-
// based, so the package-level var pointer serves as a stable identity just like
// errors.New did.
//
// Note on errors.Is: *errcode.Error has no custom Is(target error) bool method;
// errors.Is matches by package-level pointer identity. Callers that wrap with
// fmt.Errorf("%w", ErrLockLost) still work via Unwrap chain traversal.
// To match by Code regardless of pointer identity, use:
//
//	var ec *errcode.Error
//	if errors.As(err, &ec) && ec.Code == errcode.ErrDistlockLockLost { ... }
var (
	// ErrLockLost is set as the context cause when the manager fails to renew
	// the lock or the backend reports ownership has been taken by another holder.
	ErrLockLost = errcode.New(errcode.KindConflict, errcode.ErrDistlockLockLost, "distlock: lock lost")

	// ErrLockReleased is set as the context cause when release() is called
	// by the application (normal end-of-critical-section).
	ErrLockReleased = errcode.New(errcode.KindConflict, errcode.ErrDistlockLockReleased, "distlock: lock released")
)

// ErrLockTimeout is a package-level alias for errcode.ErrDistlockTimeout.
// It is returned by Acquire when the key is already held by another holder.
// The canonical definition lives in pkg/errcode for cross-package matching at
// HTTP handler boundaries; this alias keeps call sites within distlock concise.
const ErrLockTimeout = errcode.ErrDistlockTimeout
