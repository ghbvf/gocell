package saga

// leader_elect.go — distlock-based leader election for the Coordinator (PR-05,
// #964). The gate is injected into the existing tick loop (tickOnce) rather
// than a wrapper struct: SAGA-JOURNAL-HOLDER-SEAL-01 forbids any struct other
// than Coordinator from holding a journal.Journal, and journal access must stay
// inside Coordinator. The only new state is an optional distlock.Locker field;
// all gate logic lives here.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/distlock"
)

// LeaderElectModeLabel is the slog "mode" field value emitted at Start() when
// the Coordinator runs with distlock leader election — the multi-process-safe
// counterpart to UnsafeModeLabel.
const LeaderElectModeLabel = "leader_elect"

// WithLeaderElect enables distlock-based leader election. Before driving each
// ClaimPending-d instance, the Coordinator acquires a per-instance distributed
// lock keyed "saga:{definitionID}:{instanceID}"; instances whose lock is held
// by another process are skipped this tick (no-lock → skip), and the lock is
// released after the drive. The lock TTL equals Config.LeaseDuration so a
// crashed leader's distlock key and journal lease expire together (TTL
// backstop). The distlock is auto-renewed by distlock's shared manager
// goroutine; the journal lease is renewed independently by heartbeatLoop —
// both during the hold.
//
// This is a strong-dependency wiring option (see runtime-api.md Option 范式分层):
// a nil locker — both bare-nil and typed-nil — sets the leaderElectNil sentinel
// and is rejected at NewCoordinator with KindInvalid/ErrValidationFailed.
// Without this option the Coordinator runs in single-process unsafe mode and
// Start() logs mode=unsafe_no_leader.
//
// ref: runtime/distlock/locker.go (Lock-as-Resource; Acquire is non-blocking,
// returns ErrLockTimeout on contention) + temporal service/history shard
// ownership.
func WithLeaderElect(locker distlock.Locker) Option {
	return func(c *Coordinator) {
		if validation.IsNilInterface(locker) {
			c.leaderElectNil = true
			return
		}
		c.locker = locker
	}
}

// acquireLead decides whether this Coordinator may drive ci this tick and is
// the sole leader-elect gate guarding driveOne (locked by
// SAGA-DRIVE-BEHIND-LEADER-GATE-01).
//
//   - Single-process mode (c.locker == nil): always leads; returns a no-op
//     release so the tickOnce call site is uniform.
//   - Leader-elect mode: acquires the per-instance distlock. On success returns
//     a release closure invoked after driveOne. On contention (ErrLockTimeout)
//     or any other acquire error it returns lead=false (skip) — fail-closed: a
//     Coordinator that cannot confirm leadership must not drive. ErrLockTimeout
//     is the normal multi-coordinator handoff signal (Debug); other errors
//     (ctx cancel / backend I/O) are operationally interesting (Warn).
func (c *Coordinator) acquireLead(ctx context.Context, ci journal.ClaimedInstance) (release func(), lead bool) {
	if c.locker == nil {
		return func() {}, true
	}
	key := fmt.Sprintf("saga:%s:%s", ci.Instance.DefinitionID, ci.Instance.ID)
	lock, err := c.locker.Acquire(ctx, key, c.cfg.LeaseDuration)
	if err != nil {
		c.logLeaderSkip(ctx, ci, key, err)
		return nil, false
	}
	return func() {
		if rerr := lock.Release(); rerr != nil {
			c.logger.WarnContext(ctx, "saga: distlock release failed",
				slog.String("instance_id", string(ci.Instance.ID)),
				slog.String("lock_key", key),
				slog.Any("error", rerr))
		}
	}, true
}

// logLeaderSkip logs an acquireLead miss at the level matching its cause:
// ErrLockTimeout (another coordinator holds the lock — expected during handoff)
// → Debug; any other error (backend I/O, ctx cancel) → Warn.
func (c *Coordinator) logLeaderSkip(ctx context.Context, ci journal.ClaimedInstance, key string, err error) {
	level := slog.LevelWarn
	var ec *errcode.Error
	if errors.As(err, &ec) && ec.Code == errcode.ErrDistlockTimeout {
		level = slog.LevelDebug
	}
	c.logger.Log(ctx, level, "saga: leader-elect skip (lock not acquired)",
		slog.String("instance_id", string(ci.Instance.ID)),
		slog.String("definition_id", string(ci.Instance.DefinitionID)),
		slog.String("lock_key", key),
		slog.Any("error", err))
}
