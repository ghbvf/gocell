package distlock

import (
	"context"
	"fmt"
	"log/slog"
	"math"
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
//	context.Cause(lockCtx) == context.Cause(ctx) — parent context was canceled
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
	//   - context.Cause(ctx) — parent context was canceled; values, deadline, and
	//     parent cancellation propagate naturally via Go context machinery.
	//     context.Cause(lockCtx) returns context.Cause(ctx) when the parent cancels,
	//     including custom causes set via context.WithCancelCause.
	//   - context.Canceled — manager forced exit during shutdown drain
	//
	// lockCtx is derived from ctx: caller-side context values (trace IDs, auth
	// claims), deadline, and parent cancellation all propagate automatically.
	//
	// Do not pass lockCtx to a goroutine whose lifetime should outlive the lock.
	// lockCtx is canceled the instant the lock ends.
	//
	// On failure it returns (nil, nil, err) where err carries ErrLockTimeout when
	// another holder owns the key, or ctx.Err() if the parent was canceled.
	//
	// The lock is auto-renewed by a single shared manager goroutine (not per-lock)
	// until release() is called or renewal fails.
	// N active locks = 1 manager goroutine + O(N) heap. Zero per-lock goroutines.
	//
	// release() internally uses context.Background() with WithReleaseTimeout (default
	// 5s) as the Driver.Release deadline. It blocks until the I/O completes and
	// returns nil on success or a wrapped error on I/O failure. release() is
	// idempotent — a second call returns nil without contacting the backend.
	Acquire(ctx context.Context, key string, ttl time.Duration) (lockCtx context.Context, release func() error, err error)

	// Stats reports observable state of the Locker for health checks and metrics.
	Stats() Stats
}

// Stats reports observable state of a Locker instance.
type Stats struct {
	// ActiveLocks is the number of locks currently held and tracked by the manager.
	ActiveLocks int
}

// lockerImpl is the concrete Locker returned by New.
type lockerImpl struct {
	mgr *Manager
	cfg config
}

// New creates a Locker backed by the given Driver.
//
// Panics if driver is nil or if any configuration parameter is out of range:
//   - renewFraction must be in (0, 1)
//   - driftFactor must be in [0, 1)
//   - releaseTimeout must be > 0
//
// The returned Locker uses a single shared manager goroutine for all locks.
// Resource shape:
//   - 1 manager goroutine (owns the renewal heap and all Driver calls)
//   - 0 per-lock goroutines — lockCtx is derived from ctx, so parent
//     cancellation propagates automatically via Go context machinery.
//
// N active locks = 1 manager goroutine + O(N) heap.
//
// ref: plan "共享 manager goroutine" section
func New(driver Driver, opts ...Option) Locker {
	if driver == nil {
		panic("distlock.New: driver must not be nil")
	}
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	validateConfig(cfg)
	return &lockerImpl{
		mgr: newManager(driver, cfg),
		cfg: cfg,
	}
}

// validateConfig panics if any configuration parameter is outside its valid
// range. Called once in New() after all options have been applied so that
// defaults are in effect before validation runs.
func validateConfig(cfg config) {
	if cfg.renewFraction <= 0 || cfg.renewFraction >= 1 || math.IsNaN(cfg.renewFraction) {
		panic("distlock.New: invalid configuration: renewFraction must be in (0, 1), got " +
			fmt.Sprintf("%v", cfg.renewFraction))
	}
	if cfg.driftFactor < 0 || cfg.driftFactor >= 1 || math.IsNaN(cfg.driftFactor) {
		panic("distlock.New: invalid configuration: driftFactor must be in [0, 1), got " +
			fmt.Sprintf("%v", cfg.driftFactor))
	}
	if cfg.releaseTimeout <= 0 {
		panic("distlock.New: invalid configuration: releaseTimeout must be > 0, got " +
			fmt.Sprintf("%v", cfg.releaseTimeout))
	}
	if cfg.maxRenewAttempts < 1 {
		panic("distlock.New: invalid configuration: maxRenewAttempts must be ≥ 1, got " +
			fmt.Sprintf("%v", cfg.maxRenewAttempts))
	}
}

// Acquire implements Locker.
func (l *lockerImpl) Acquire(ctx context.Context, key string, ttl time.Duration) (context.Context, func() error, error) {
	// Fast path: parent already canceled.
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	// Redis SetNX/PEXPIRE take TTL in integer milliseconds; sub-millisecond
	// values truncate to 0, which go-redis v9 documents as "no expiration"
	// (string_commands.go SetNX). Enforce a 1ms minimum so a misconfigured
	// caller cannot create a permanent lock that survives process death.
	if ttl < time.Millisecond {
		return nil, nil, errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("distlock: ttl must be ≥ 1ms (got %s); sub-millisecond TTLs would truncate to 0 in Redis and create a permanent lock", ttl))
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

	// lockCtx is derived from ctx so that parent values (trace IDs, auth claims),
	// deadline, and parent cancellation all propagate automatically via stdlib
	// context machinery. No per-lock watcher goroutine is needed.
	//
	// When the parent ctx is canceled, context.Cause(lockCtx) returns
	// context.Cause(ctx), including custom causes set via context.WithCancelCause.
	//
	// The manager goroutine may also cancel lockCtx with ErrLockLost or
	// ErrLockReleased to signal lock lifecycle events.
	lockCtx, cancelCause := context.WithCancelCause(ctx)

	id := l.mgr.nextID.Add(1)
	state := &lockState{
		id:     id,
		key:    key,
		token:  token,
		ttl:    ttl,
		cancel: cancelCause,
	}

	l.mgr.add(state)

	var once sync.Once
	var releaseErr error
	release := func() error {
		once.Do(func() {
			releaseErr = l.mgr.remove(id)
		})
		return releaseErr
	}

	return lockCtx, release, nil
}

// Stats implements Locker.
func (l *lockerImpl) Stats() Stats {
	return Stats{ActiveLocks: l.mgr.Snapshot().Locks}
}

// Manager returns the internal Manager for test use.
// Only exported so package-level tests (locker_test.go, manager_test.go) can
// assert on lifecycle and heap state without coupling to internal types.
func (l *lockerImpl) Manager() *Manager {
	return l.mgr
}
