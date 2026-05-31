package distlock

import (
	"sync"
	"sync/atomic"
)

// Lock is a held distributed lock returned by Locker.Acquire.
//
// # Lock-as-Resource design
//
// Lock is intentionally NOT a context.Context. The caller-supplied context
// passed to Acquire is consumed only for the acquire RPC (SetNX); once the
// lock is held, its lifecycle is independent of the caller ctx. This matches
// the prevailing industry convention (bsm/redislock, go-redsync, etcd
// client/v3/concurrency, HashiCorp consul, Apache Curator): caller-ctx
// cancellation does NOT release a held lock; only explicit Release(),
// Orphan(), or renewal failure (ErrLockLost) will end it. (Explicit
// Locker.Shutdown is deferred to a future iteration — see ADR
// docs/architecture/202605200000-adr-distlock-lock-as-resource.md
// §"Out of scope".) Per-lock Orphan() now exists for graceful shutdown/handoff
// (bounded-TTL takeover, no release RPC). DISTLOCK-LOCKER-SHUTDOWN-01 tracks
// the deferred locker-level Shutdown/Close path.
//
// Rationale: caller-ctx cancellation expresses "I no longer care about the
// outcome of THIS request" — it does NOT express "release the resource I
// acquired". Conflating the two (the previous Lock-as-Context design)
// caused GH #20: a forgotten caller could leave a held lock being renewed
// indefinitely, while a busy caller whose ctx happened to time out could
// suddenly lose its lock mid-critical-section.
//
// # Caller responsibility
//
// Acquire returns *Lock. Caller must:
//
//	lock, err := locker.Acquire(ctx, key, ttl)
//	if err != nil { return err }
//	defer lock.Release()
//
// Process crash falls back to Redis-side TTL expiry. A living caller that
// forgets Release leaks the lock until process exit.
//
// # Why not context.Context
//
// Acquire could have returned a context.Context whose Done()/Cause() reflect
// lock-end events, but doing so invites the misuse pattern that GH #20
// exposed: caller passes the lock ctx to db.QueryContext / http.NewRequest
// downstream, assuming caller-ctx semantics. By returning *Lock (which does
// not implement context.Context), the compiler rejects such misuse outright.
//
// Values from the caller's ctx are still exposed via Lock.Value(key) — the
// underlying context.WithoutCancel(callerCtx).Value lookup preserves trace
// IDs and auth claims while shielding the lock from caller-ctx
// cancellation/deadline.
//
// Callers should avoid stashing large objects, raw secrets, or
// request-scoped resources whose lifetime should not extend past the
// request as values on callerCtx before calling Acquire: every value
// reachable from callerCtx remains pinned through the captured
// valueLookup closure for the full held-lock duration (including any
// auto-renewal cycles) and is not eligible for GC until lock.Release()
// returns or the lock is lost. Trace IDs / auth claims / span contexts
// (small immutable objects) are the intended use; tokens and PII should
// be parameterized explicitly instead.
//
// ref: GH #20 ; ADR docs/architecture/202605200000-adr-distlock-lock-as-resource.md
type Lock struct {
	// done is closed by markCause exactly once when the lock ends.
	done chan struct{}

	// cause stores the reason the lock ended. Set BEFORE done is closed so
	// readers that observe done closed are guaranteed to see cause.
	cause atomic.Value // error

	// causeOnce ensures markCause is effective exactly once even under
	// concurrent calls (release vs renewal-failure race).
	causeOnce sync.Once

	// valueLookup performs the caller-ctx Value lookup with caller-ctx
	// cancellation/deadline shielded out. Acquire wires this as
	// context.WithoutCancel(callerCtx).Value so only the Value channel of
	// the caller ctx survives — never the full context. Storing the
	// closure (rather than the context) keeps Lock from owning a
	// context.Context field, which would otherwise blur the
	// Lock-as-Resource boundary.
	valueLookup func(key any) any

	// release is the closure provided by Acquire. Idempotent via sync.Once
	// embedded in the closure (shared with orphan); second call returns the
	// cached error.
	release func() error

	// orphan is the closure provided by Acquire. Shares the same sync.Once
	// as release so Orphan() and Release() are mutually exclusive: the first
	// call wins; later calls of either are no-ops.
	orphan func()
}

// newLock constructs a *Lock. Package-internal: only lockerImpl.Acquire and
// tests construct Locks.
func newLock(valueLookup func(key any) any, release func() error, orphan func()) *Lock {
	return &Lock{
		done:        make(chan struct{}),
		valueLookup: valueLookup,
		release:     release,
		orphan:      orphan,
	}
}

// Done returns a channel that closes when the lock ends. Read it to learn
// that the critical section must be aborted; pair with Cause() to learn why.
//
//	select {
//	case <-lock.Done():
//	    return fmt.Errorf("lock ended: %w", lock.Cause())
//	case result := <-doWork(...):
//	    return result
//	}
func (l *Lock) Done() <-chan struct{} { return l.done }

// Cause returns the reason the lock ended, or nil if the lock is still held.
//
// Possible non-nil values:
//   - ErrLockReleased: Release() was called by the application.
//   - ErrLockLost:     renewal failed or backend reports ownership taken by
//     another holder.
//   - ErrLockOrphaned: Orphan() was called by the application (renewal stopped;
//     backend key expires naturally after ≤1×TTL).
//
// Cause never returns context.Cause(callerCtx) — caller-ctx cancellation
// does not end the lock under the Lock-as-Resource contract.
func (l *Lock) Cause() error {
	v := l.cause.Load()
	if v == nil {
		return nil
	}
	return v.(error)
}

// Value returns the value associated with the caller-supplied context's
// key. Caller-ctx cancellation and deadline do NOT propagate to Lock; only
// values do. Use this to retrieve trace IDs / auth claims that were on the
// caller ctx without re-plumbing them through Acquire's signature.
func (l *Lock) Value(key any) any { return l.valueLookup(key) }

// Release ends the lock. Idempotent: safe to call multiple times; only the
// first call performs Driver.Release I/O and Cause() will be ErrLockReleased
// afterwards. Subsequent calls return the first call's result.
//
// Release blocks until Driver.Release completes (bounded by
// WithReleaseTimeout, default 5s).
func (l *Lock) Release() error { return l.release() }

// Orphan stops this lock's lease renewal WITHOUT deleting the backend key.
// The key expires naturally after at most one TTL window, handing the lock to
// a competitor within ≤1×TTL — no Release I/O is performed, so Orphan never
// blocks on or fails due to backend reachability. After Orphan, Done() is
// closed and Cause() reports ErrLockOrphaned.
//
// Orphan and Release are mutually exclusive and idempotent: the first call of
// either wins; later calls of either are no-ops. Use Orphan for graceful
// shutdown/handoff (bounded-TTL takeover, no release RPC); use Release for
// immediate end-of-critical-section.
//
// ref: etcd-io/etcd client/v3/concurrency/session.go Session.Orphan — stop
// keepalive, lease expires after TTL (vs Close which revokes immediately).
func (l *Lock) Orphan() { l.orphan() }

// markCause sets the cause and closes done exactly once.
// Package-internal: invoked by manager handlers (handleRenew on lost,
// handleRemove on release).
func (l *Lock) markCause(cause error) {
	l.causeOnce.Do(func() {
		l.cause.Store(cause)
		close(l.done)
	})
}
