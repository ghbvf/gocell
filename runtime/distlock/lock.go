package distlock

import (
	"context"
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
// renewal failure (ErrLockLost), or manager shutdown will end it.
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
// underlying context.WithoutCancel(callerCtx) preserves trace IDs and auth
// claims while shielding the lock from caller-ctx cancellation/deadline.
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

	// valuesCtx exposes caller-ctx values via Lock.Value while shielding
	// Lock from caller-ctx cancellation/deadline. Built with
	// context.WithoutCancel(callerCtx) in Acquire.
	valuesCtx context.Context

	// release is the closure provided by Acquire. Idempotent via sync.Once
	// embedded in the closure; second call returns the cached error.
	release func() error
}

// newLock constructs a *Lock. Package-internal: only lockerImpl.Acquire and
// tests construct Locks.
func newLock(valuesCtx context.Context, release func() error) *Lock {
	return &Lock{
		done:      make(chan struct{}),
		valuesCtx: valuesCtx,
		release:   release,
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
//   - context.Canceled (or another non-sentinel error): manager forced exit
//     during shutdown.
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
func (l *Lock) Value(key any) any { return l.valuesCtx.Value(key) }

// Release ends the lock. Idempotent: safe to call multiple times; only the
// first call performs Driver.Release I/O and Cause() will be ErrLockReleased
// afterwards. Subsequent calls return the first call's result.
//
// Release blocks until Driver.Release completes (bounded by
// WithReleaseTimeout, default 5s).
func (l *Lock) Release() error { return l.release() }

// markCause sets the cause and closes done exactly once.
// Package-internal: invoked by manager handlers (handleRenew on lost,
// handleRemove on release, shutdown path).
func (l *Lock) markCause(cause error) {
	l.causeOnce.Do(func() {
		l.cause.Store(cause)
		close(l.done)
	})
}
