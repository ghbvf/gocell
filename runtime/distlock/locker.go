package distlock

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Locker acquires named distributed locks.
//
// # Lock-as-Context design
//
// Acquire returns a derived context that is automatically canceled when the
// lock ends. The cause distinguishes how it ended:
//
//	context.Cause(lockCtx) == ErrLockReleased  — release() called
//	context.Cause(lockCtx) == ErrLockLost      — renewal failed / ownership taken
//	context.Cause(lockCtx) == context.Cause(parent ctx)  — parent context was canceled
//	context.Cause(lockCtx) == context.Canceled (or another error) — manager forced exit
//
// Use context.Cause(lockCtx) (not lockCtx.Err()) to distinguish causes.
//
// This mirrors context.WithDeadline(parent) (Context, CancelFunc) so callers
// pass lockCtx directly to database calls, HTTP requests, or outbox.Emit —
// all downstream operations are canceled automatically on lock loss.
//
// ref: golang stdlib context.WithDeadline — API shape adopted directly
// ref: go-redsync/redsync — per-lock goroutine model replaced by shared manager
type Locker interface {
	// Acquire blocks until the lock is granted or ctx is canceled.
	//
	// On success it returns:
	//   - lockCtx: a derived context canceled when the lock ends
	//   - release: must be called to release the lock; idempotent
	//   - nil error
	//
	// lockCtx cancel causes:
	//   - ErrLockReleased — release() was called (normal end-of-critical-section)
	//   - ErrLockLost     — renewal failed or backend reports ownership taken
	//   - context.Cause(ctx) — parent context was canceled (propagated faithfully,
	//     including custom causes set via context.WithCancelCause)
	//   - context.Canceled — manager forced exit during shutdown drain
	//
	// NOTE: lockCtx is derived from context.Background() (not from ctx) — caller-side
	// context values (trace IDs, auth claims) do NOT propagate to lockCtx. If your
	// downstream calls need request-scoped values, attach them explicitly to lockCtx.
	//
	// Do not pass lockCtx to a goroutine whose lifetime should outlive the lock.
	// lockCtx is canceled the instant the lock ends.
	//
	// On failure it returns (nil, nil, err) where err carries ErrLockTimeout when
	// another holder owns the key, or ctx.Err() if the parent was canceled.
	//
	// The lock is auto-renewed by a shared manager goroutine (not per-lock) until
	// release() is called or renewal fails.
	//
	// release() takes no context — internally it uses context.Background() with
	// WithReleaseTimeout. The Driver.Release I/O is fire-and-forget; release()
	// returns immediately. Use Locker.Manager().Drained() if you need to wait for
	// all in-flight releases to complete.
	Acquire(ctx context.Context, key string, ttl time.Duration) (lockCtx context.Context, release func(), err error)
}

// lockerImpl is the concrete Locker returned by New.
type lockerImpl struct {
	mgr *Manager
	cfg config
}

// New creates a Locker backed by the given Driver.
//
// The returned Locker uses a single shared manager goroutine for all locks.
// The actual resource shape per Acquire call is:
//   - 1 shared manager goroutine (owns the renewal heap and all Driver calls)
//   - 1 small watcher goroutine per held lock (forwards parent-ctx cancellation)
//
// N active locks = 1 manager goroutine + N watcher goroutines + O(N) heap.
// The watcher goroutines are minimal (no allocations after start; exit on
// either ctx.Done or lockCtx.Done). A single shared "all parents" goroutine
// would require reflect.Select which is slower at scale, so per-lock watchers
// are preferred — this is an intentional design trade-off.
//
// ref: plan "共享 manager goroutine" section
func New(driver Driver, opts ...Option) Locker {
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	return &lockerImpl{
		mgr: newManager(driver, cfg),
		cfg: cfg,
	}
}

// Acquire implements Locker.
func (l *lockerImpl) Acquire(ctx context.Context, key string, ttl time.Duration) (context.Context, func(), error) {
	// Fast path: parent already canceled.
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	token, err := randomToken()
	if err != nil {
		return nil, nil, fmt.Errorf("distlock: token generation failed: %w", err)
	}

	acquired, err := l.mgr.driver.SetNX(ctx, key, token, ttl)
	if err != nil {
		slog.Warn("distlock: acquire I/O error", "key", key, "op", "SetNX", "error", err)
		return nil, nil, fmt.Errorf("distlock: acquire failed: %w", err)
	}
	if !acquired {
		return nil, nil, errcode.New(ErrLockTimeout,
			"distlock: lock already held by another holder")
	}

	lockCtx, cancelCause := context.WithCancelCause(context.Background())

	id := l.mgr.nextID.Add(1)
	state := &lockState{
		id:     id,
		key:    key,
		token:  token,
		ttl:    ttl,
		cancel: cancelCause,
	}

	// Watch parent ctx: if it is canceled, propagate the cause to lockCtx.
	// context.Cause propagates custom causes set via context.WithCancelCause.
	go func() {
		select {
		case <-ctx.Done():
			cancelCause(context.Cause(ctx))
		case <-lockCtx.Done():
		}
	}()

	l.mgr.add(state)

	var once sync.Once
	release := func() {
		once.Do(func() {
			l.mgr.remove(id)
		})
	}

	return lockCtx, release, nil
}

// Manager returns the internal Manager for test use.
// Only exported so package-level tests (locker_test.go, manager_test.go) can
// assert on lifecycle and heap state without coupling to internal types.
func (l *lockerImpl) Manager() *Manager {
	return l.mgr
}
