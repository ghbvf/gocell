package distlock

import (
	"github.com/ghbvf/gocell/framework/pkg/errcode"
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
//
// HTTP Kind tagging is fail-closed: these sentinels are internal
// Lock.Cause() signals and are NOT intended to reach an HTTP handler via
// httputil.WriteError. If one accidentally does, the Kind below produces
// the least-misleading response — never a confidently-wrong status.
var (
	// ErrLockLost is returned by Lock.Cause() when the manager fails to renew
	// the lock or the backend reports ownership has been taken by another holder.
	// KindConflict (HTTP 409) is the natural mapping if it ever surfaces to a
	// caller: another holder owns the resource.
	ErrLockLost = errcode.New(errcode.KindConflict, errcode.ErrDistlockLockLost, "distlock: lock lost")

	// ErrLockReleased is returned by Lock.Cause() when Lock.Release() is called
	// by the application (normal end-of-critical-section). KindInternal (HTTP
	// 500) is deliberate: a normal release is NOT a conflict, and if this
	// sentinel ever reaches an HTTP handler that is a server-side programming
	// bug — 500 surfaces it as such rather than misleading the client with 409.
	ErrLockReleased = errcode.New(errcode.KindInternal, errcode.ErrDistlockLockReleased, "distlock: lock released")

	// ErrLockOrphaned is returned by Lock.Cause() when Lock.Orphan() is called
	// by the application. Renewal is stopped and the backend key is NOT deleted
	// — it expires on its lease TTL (~1×TTL from the last successful renewal;
	// best-effort, see Lock.Orphan godoc), handing the lock to a competitor
	// WITHOUT a Release round-trip that could hang or fail at shutdown.
	// KindInternal (HTTP 500) matches ErrLockReleased: an orphaned
	// lock surfacing to an HTTP handler is a server-side programming bug — 500
	// is preferable to a misleading 409 (external conflict). Orphan is a
	// deliberate local action; if it reaches an HTTP boundary that is a
	// caller error, not a backend conflict.
	//
	// ref: etcd-io/etcd client/v3/concurrency/session.go Session.Orphan
	ErrLockOrphaned = errcode.New(errcode.KindInternal, errcode.ErrDistlockLockOrphaned,
		"distlock: lock orphaned by caller (renewal stopped; key expires after TTL)")
)

// ErrLockTimeout is a package-level alias for errcode.ErrDistlockTimeout.
// It is returned by Acquire when the key is already held by another holder.
// The canonical definition lives in pkg/errcode for cross-package matching at
// HTTP handler boundaries; this alias keeps call sites within distlock concise.
const ErrLockTimeout = errcode.ErrDistlockTimeout
