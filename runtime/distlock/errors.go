package distlock

import (
	"errors"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Sentinel errors used as context.Cause values on the lock-derived context.
// Callers distinguish them via errors.Is(context.Cause(lockCtx), ErrLockLost).
//
// These are plain sentinel errors (not errcode.Error) because they are used
// as context cancellation causes, not as API boundary error codes. At the API
// boundary (Acquire returning an error) errcode is used instead.
var (
	// ErrLockLost is set as the context cause when the manager fails to renew
	// the lock or the backend reports ownership has been taken by another holder.
	ErrLockLost = errors.New("distlock: lock lost")

	// ErrLockReleased is set as the context cause when release() is called
	// by the application (normal end-of-critical-section).
	ErrLockReleased = errors.New("distlock: lock released")
)

// ErrLockTimeout is returned by Acquire when the key is already held by
// another holder and the lock cannot be granted.
// Uses errcode so it can be matched at HTTP handler boundaries.
const ErrLockTimeout errcode.Code = "ERR_DISTLOCK_TIMEOUT"
