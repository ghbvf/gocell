package distlock

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// MinTTL is the smallest lock TTL Acquire accepts. Redis SetNX/PEXPIRE take TTL
// in integer milliseconds; sub-millisecond values truncate to 0, which go-redis
// v9 documents as "no expiration" — a permanent lock that survives process
// death. Callers that derive a distlock TTL from their own config (e.g.
// runtime/saga.Coordinator's LeaseDuration) reference this const to fail fast at
// construction instead of at the first Acquire.
const MinTTL = time.Millisecond

// Locker acquires named distributed locks.
//
// # Lock-as-Resource design
//
// Acquire returns a *Lock — intentionally NOT a context.Context. Caller-ctx
// cancellation does NOT release a held lock; only explicit Release or
// renewal failure will end it. This matches the prevailing industry
// convention (bsm/redislock, go-redsync, etcd client/v3/concurrency,
// HashiCorp consul, Apache Curator) and prevents the misuse pattern that GH
// #20 exposed under the previous Lock-as-Context design. (Explicit
// Locker.Shutdown is deferred — see ADR
// docs/architecture/202605200000-adr-distlock-lock-as-resource.md
// §"Out of scope".)
//
// Caller responsibility:
//
//	lock, err := locker.Acquire(ctx, key, ttl)
//	if err != nil { return err }
//	defer lock.Release()
//
// ctx is consumed only for the acquire RPC (SetNX). Once the lock is held,
// caller-ctx cancellation is decoupled. Values from ctx (trace IDs, auth
// claims) are still exposed via Lock.Value via context.WithoutCancel.
//
// ref: GH #20 ; ADR docs/architecture/202605200000-adr-distlock-lock-as-resource.md
// ref: go-redsync/redsync — caller-ctx scoped to acquire only
// ref: etcd client/v3/concurrency — session-scoped keepalive, decoupled from per-op ctx
type Locker interface {
	// Acquire blocks until the lock is granted or ctx is canceled.
	//
	// On success it returns a *Lock; caller MUST eventually call lock.Release.
	//
	// Lock-end signals (lock.Done() closed; lock.Cause() reports):
	//   - ErrLockReleased — Release() was called (normal end-of-critical-section)
	//   - ErrLockLost     — renewal failed or backend reports ownership taken
	//   - ErrLockOrphaned — Orphan() was called (renewal stopped; key expires after ≤1×TTL)
	//
	// Notably absent: caller-ctx cancellation does NOT end the lock. If the
	// caller wants the lock to end when its ctx is canceled, the caller must
	// explicitly arrange a goroutine that does so.
	//
	// Per-lock Orphan() now exists for graceful shutdown/handoff (bounded-TTL
	// takeover, no release RPC). Locker.Shutdown/Close remains deferred
	// (DISTLOCK-LOCKER-SHUTDOWN-01).
	//
	// Idiomatic patterns for combining lock-end with caller-ctx:
	//
	//  // Pattern A — caller wants ctx cancel to also release the lock:
	//  lock, err := locker.Acquire(ctx, key, ttl)
	//  if err != nil { return err }
	//  defer lock.Release()
	//  go func() {
	//      // Wait for whichever ends first; the second case prevents the
	//      // goroutine from blocking on ctx forever after a normal Release
	//      // or a renewal-failure ends the lock.
	//      select {
	//      case <-ctx.Done():
	//          _ = lock.Release() // best-effort; idempotent.
	//      case <-lock.Done():
	//          // lock ended (Release / lost) — nothing more to do.
	//      }
	//  }()
	//
	//  // Pattern B — caller wants the *first* of (ctx-cancel | lock-lost) to abort:
	//  select {
	//  case <-ctx.Done():
	//      _ = lock.Release()
	//      return ctx.Err()
	//  case <-lock.Done():
	//      return fmt.Errorf("lock ended: %w", lock.Cause())
	//  }
	//
	// TTL is the only ceiling on a held-but-forgotten lock. Choose ttl
	// commensurate with the critical-section worst case (typically seconds
	// to minutes); avoid hour-scale TTLs unless the workload genuinely
	// runs that long, since a caller that aborts without Release leaves
	// peers blocked for the full ttl window. The fallback after process
	// crash is Redis-side TTL expiry.
	//
	// On failure it returns (nil, err) where err carries ErrLockTimeout when
	// another holder owns the key, or ctx.Err() (wrapped) if the parent was canceled.
	//
	// The lock is auto-renewed by a single shared manager goroutine (not per-lock)
	// until lock.Release() is called or renewal fails.
	// N active locks = 1 manager goroutine + O(N) heap. Zero per-lock goroutines.
	//
	// Driver.Release uses context.Background() with WithReleaseTimeout (default
	// 5s). lock.Release() blocks until the I/O completes and returns nil on
	// success or a wrapped error on I/O failure. lock.Release() is idempotent —
	// a second call returns the first call's result without contacting the backend.
	Acquire(ctx context.Context, key string, ttl time.Duration) (*Lock, error)

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
// Returns an error if driver is nil or if any configuration parameter is out of range:
//   - renewFraction must be in (0, 1)
//   - driftFactor must be in [0, 1)
//   - releaseTimeout must be > 0
//
// The returned Locker uses a single shared manager goroutine for all locks.
// Resource shape:
//   - 1 manager goroutine (owns the renewal heap and all Driver calls)
//   - 0 per-lock goroutines — *Lock is a signal/value handle; lock-end is
//     delivered by the manager goroutine via Lock.markCause (closes Done()
//     channel, sets Cause()) without spawning watchers.
//
// N active locks = 1 manager goroutine + O(N) heap.
//
// ref: plan "共享 manager goroutine" section
func New(driver Driver, clk clock.Clock, opts ...Option) (Locker, error) {
	if validation.IsNilInterface(driver) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "distlock.New: driver must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "distlock.New: clock must not be nil")
	}
	cfg := defaultConfig()
	cfg.clock = clk
	for _, o := range opts {
		o(&cfg)
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return &lockerImpl{
		mgr: newManager(driver, cfg),
		cfg: cfg,
	}, nil
}

// validateConfig returns an error if any configuration parameter is outside its
// valid range. Called once in New() after all options have been applied so that
// defaults are in effect before validation runs.
func validateConfig(cfg config) error {
	if cfg.renewFraction <= 0 || cfg.renewFraction >= 1 || math.IsNaN(cfg.renewFraction) {
		return invalidDistlockConfig("renewFraction must be in (0, 1)", cfg.renewFraction)
	}
	if cfg.driftFactor < 0 || cfg.driftFactor >= 1 || math.IsNaN(cfg.driftFactor) {
		return invalidDistlockConfig("driftFactor must be in [0, 1)", cfg.driftFactor)
	}
	if cfg.releaseTimeout <= 0 {
		return invalidDistlockConfig("releaseTimeout must be > 0", cfg.releaseTimeout)
	}
	if cfg.maxRenewAttempts < 1 {
		return invalidDistlockConfig("maxRenewAttempts must be >= 1", cfg.maxRenewAttempts)
	}
	return nil
}

func invalidDistlockConfig(reason string, got any) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"distlock.New: invalid configuration",
		errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("reason=%s got=%v", reason, got))))
}

// Acquire implements Locker.
func (l *lockerImpl) Acquire(ctx context.Context, key string, ttl time.Duration) (*Lock, error) {
	// Fast path: parent already canceled. Driver.SetNX would also catch this,
	// but short-circuit gives a clearer error origin.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("distlock: acquire: %w", err)
	}
	if key == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"distlock: key must not be empty")
	}
	// Enforce MinTTL so a misconfigured caller cannot create a permanent lock
	// that survives process death (see MinTTL godoc).
	if ttl < MinTTL {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"distlock: ttl must be ≥ 1ms; sub-millisecond TTLs would truncate to 0 in Redis and create a permanent lock",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("ttl=%s", ttl))))
	}

	token, err := randomToken()
	if err != nil {
		return nil, fmt.Errorf("distlock: token generation failed: %w", err)
	}

	acquired, err := l.mgr.driver.SetNX(ctx, key, token, ttl)
	if err != nil {
		slog.Warn("distlock: acquire I/O error", "key", key, "op", "SetNX", "error", err)
		return nil, fmt.Errorf("distlock: acquire failed: %w", err)
	}
	if !acquired {
		return nil, errcode.New(errcode.KindConflict, ErrLockTimeout,
			"distlock: lock already held by another holder")
	}

	id := l.mgr.nextID.Add(1)

	// release and orphan share ONE sync.Once so Orphan() and Release() are
	// mutually exclusive: whichever is called first wins; all later calls of
	// either are no-ops. This eliminates the Release-after-Orphan and
	// Orphan-after-Release races without additional locking.
	//
	// The Once MUST remain shared across both closures — splitting it into two
	// Onces would let orphan-then-release (or the renewal-lost-then-orphan path)
	// double-send manager events and corrupt pendingReleases drain accounting.
	var once sync.Once
	var releaseErr error
	release := func() error {
		once.Do(func() {
			releaseErr = l.mgr.remove(id)
		})
		return releaseErr
	}
	orphan := func() {
		once.Do(func() {
			l.mgr.orphan(id)
		})
	}

	// valueLookup exposes caller-ctx values (trace IDs, auth claims) via
	// Lock.Value while shielding the lock from caller-ctx cancellation and
	// deadline. context.WithoutCancel preserves values only; the closure
	// captures the derived ctx's Value method without storing the ctx
	// itself on Lock (keeps Lock-as-Resource boundary explicit and
	// satisfies "no context.Context field on long-lived struct").
	// ref: stdlib context.WithoutCancel — Go 1.21+
	valuesCtx := context.WithoutCancel(ctx)
	lock := newLock(valuesCtx.Value, release, orphan)

	state := &lockState{
		id:    id,
		key:   key,
		token: token,
		ttl:   ttl,
		lock:  lock,
	}

	l.mgr.add(state)

	return lock, nil
}

// Stats implements Locker.
func (l *lockerImpl) Stats() Stats {
	return Stats{ActiveLocks: l.mgr.Snapshot().Locks}
}

// Manager returns the internal Manager. TEST USE ONLY — production callers
// MUST NOT reach into the Manager; the Locker interface is the supported
// public surface. The method is exported only because *_test.go in package
// distlock_test (external) cannot access unexported methods via the
// mgrGetter interface used by package-level tests to read Started() /
// Drained() / Snapshot() / RenewNotify().
//
// The unexported receiver lockerImpl is the primary barrier: external
// packages can only reach this via an explicit
// l.(interface{ Manager() *Manager }) type-assertion, which provides
// deliberate friction and a clear grep target. No production path
// performs that assertion.
func (l *lockerImpl) Manager() *Manager {
	return l.mgr
}
