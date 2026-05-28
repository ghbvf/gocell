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
	"github.com/ghbvf/gocell/pkg/idutil"
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
// released after the drive. The lock TTL is Config.LeaseDuration; distlock is
// auto-renewed by distlock's shared manager goroutine and the journal lease is
// renewed by the Executor's per-step heartbeat goroutine, both during the hold.
//
// distlock here is an efficiency lock, NOT the correctness boundary: it shrinks
// the window in which two coordinators concurrently run a Step.Run body, but if
// distlock renewal fails mid-drive the drive continues — correctness is
// guaranteed by the journal lease_id CAS fencing (a stale leader's Append /
// MarkTerminal is rejected at commit). The distlock TTL and the journal lease
// share the same LeaseDuration but start at different instants (acquireLead vs
// ClaimPending), so on crash the distlock key may expire slightly before the
// journal lease; this is safe — the journal CAS still fences the old leader.
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

// leaderElectLockKey builds the per-instance distlock key for (definitionID,
// instanceID). idutil.SafeID permits ':' and '/' (idutil.IsSafeID), so a plain
// "saga:{def}:{inst}" join is NOT injective — (def="a:b", inst="c") and
// (def="a", inst="b:c") would collide on the same lock and falsely serialize two
// unrelated instances. Instance IDs are caller-supplied SafeIDs
// (ksaga.NewInstance), so the key must be injective over the full charset, not
// the current UUID generator output. Length-prefixing definitionID restores
// injectivity while staying within the SafeID charset (readable in Redis/logs):
// the decimal length before the first ':'-delimited segment fixes how many bytes
// definitionID occupies, so the (def, inst) split is unambiguous regardless of
// ':' inside either segment.
func leaderElectLockKey(definitionID, instanceID idutil.SafeID) string {
	return fmt.Sprintf("saga:%d:%s:%s", len(definitionID), definitionID, instanceID)
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
//     (contention) and ctx cancellation (normal shutdown) are expected (Debug);
//     backend I/O errors are operationally interesting (Warn).
//
// release is non-nil iff lead is true; callers MUST NOT call release when lead
// is false.
func (c *Coordinator) acquireLead(ctx context.Context, ci journal.ClaimedInstance) (release func(), lead bool) {
	if c.locker == nil {
		return func() { /* no-op release: no distributed locker configured, single-coordinator mode */ }, true
	}
	key := leaderElectLockKey(ci.Instance.DefinitionID, ci.Instance.ID)
	lock, err := c.locker.Acquire(ctx, key, c.cfg.LeaseDuration)
	if err != nil {
		c.logLeaderSkip(ctx, ci, key, err)
		return nil, false
	}
	return func() {
		if rerr := lock.Release(); rerr != nil {
			c.logger.WarnContext(ctx, "saga: distlock release failed",
				slog.String("instance_id", string(ci.Instance.ID)),
				slog.String("definition_id", string(ci.Instance.DefinitionID)),
				slog.String("lock_key", key),
				slog.Any("error", rerr))
		}
	}, true
}

// logLeaderSkip logs an acquireLead miss at the level matching its cause.
// Contention (ErrLockTimeout — another coordinator holds the lock) and ctx
// cancellation (normal Stop()/shutdown of the tick loop) are expected
// operational signals, not faults → Debug. Anything else (backend I/O) → Warn.
func (c *Coordinator) logLeaderSkip(ctx context.Context, ci journal.ClaimedInstance, key string, err error) {
	level := slog.LevelWarn
	var ec *errcode.Error
	switch {
	case errors.As(err, &ec) && ec.Code == errcode.ErrDistlockTimeout:
		level = slog.LevelDebug
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		level = slog.LevelDebug
	}
	c.logger.Log(ctx, level, "saga: leader-elect skip (lock not acquired)",
		slog.String("instance_id", string(ci.Instance.ID)),
		slog.String("definition_id", string(ci.Instance.DefinitionID)),
		slog.String("lock_key", key),
		slog.Any("error", err))
}
