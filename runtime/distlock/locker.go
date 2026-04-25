package distlock

import (
	"context"
	"fmt"
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
//	context.Cause(lockCtx) == ctx.Err()        — parent context was canceled
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
	//   - lockCtx: a derived context canceled with ErrLockReleased or ErrLockLost
	//   - release: must be called to release the lock; idempotent
	//   - nil error
	//
	// On failure it returns (nil, nil, err) where err carries ErrLockTimeout when
	// another holder owns the key, or ctx.Err() if the parent was canceled.
	//
	// The lock is auto-renewed by a shared manager goroutine (not per-lock) until
	// release() is called or renewal fails.
	Acquire(ctx context.Context, key string, ttl time.Duration) (lockCtx context.Context, release func(), err error)
}

// lockerImpl is the concrete Locker returned by New.
type lockerImpl struct {
	mgr *Manager
	cfg config
}

// New creates a Locker backed by the given Driver.
//
// The returned Locker uses a single shared manager goroutine for all locks:
// N active locks = 1 goroutine + O(N) heap, not N goroutines.
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
		return nil, nil, errcode.Wrap(ErrLockTimeout, "distlock: token generation failed", err)
	}

	acquired, err := l.mgr.driver.SetNX(ctx, key, token, ttl)
	if err != nil {
		return nil, nil, fmt.Errorf("distlock: acquire failed: %w", err)
	}
	if !acquired {
		return nil, nil, errcode.New(ErrLockTimeout,
			"distlock: lock already held by another holder (key="+key+")")
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
	go func() {
		select {
		case <-ctx.Done():
			cancelCause(ctx.Err())
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
