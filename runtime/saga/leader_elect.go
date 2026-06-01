package saga

// leader_elect.go — distlock-based leader election for the Coordinator (PR-05,
// #964). The gate is injected into the existing tick loop (tickOnce) rather
// than a wrapper struct: post-#1209 SAGA-JOURNAL-HOLDER-SEAL-01 lets only the
// Coordinator hold the journal.JournalCore field and bans the Heartbeat-bearing
// journal.Journal / journal.Heartbeater as a field anywhere in runtime/saga, so
// journal access must stay inside Coordinator. The only new state is an optional
// distlock.Locker field; all gate logic lives here.

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
	"github.com/ghbvf/gocell/runtime/saga/executor"
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
//   - Single-process mode (c.locker == nil): always leads; returns no-op
//     release and orphan closures so the tickOnce call site is uniform.
//   - Leader-elect mode: acquires the per-instance distlock. On success returns
//     a release closure and an orphan closure. On contention (ErrLockTimeout)
//     or any other acquire error it returns lead=false (skip) — fail-closed: a
//     Coordinator that cannot confirm leadership must not drive. ErrLockTimeout
//     (contention) and ctx cancellation (normal shutdown) are expected (Debug);
//     backend I/O errors are operationally interesting (Warn).
//
// release and orphan are non-nil iff lead is true; callers MUST NOT call either
// when lead is false. release frees the distlock immediately via Driver.Release
// I/O — optimal for normal per-tick completion (work done → immediate handoff).
// orphan stops renewal without a shutdown-time release round-trip; the key
// expires on its lease TTL (~1×TTL from the last successful renewal,
// best-effort — see distlock.Lock.Orphan) so a competitor can take over — used
// by Stop (I/O-free, cannot hang on an unreachable backend during shutdown).
func (c *Coordinator) acquireLead(ctx context.Context, ci journal.ClaimedInstance) (release func(), orphan func(), lead bool) {
	if c.locker == nil {
		noop := func() { /* no-op: no distributed locker configured, single-coordinator mode */ }
		return noop, noop, true
	}
	key := leaderElectLockKey(ci.Instance.DefinitionID, ci.Instance.ID)
	lock, err := c.locker.Acquire(ctx, key, c.cfg.LeaseDuration)
	if err != nil {
		reason := classifyLeaderSkip(err)
		c.logLeaderSkip(ctx, ci, key, err, reason)
		// Best-effort metric: backend_error is the lock-acquire failure rate;
		// sustained contended with no drives means an instance is stuck skipping
		// (#1109). definition_id is the bounded label; instance_id stays in logs.
		c.safeObserve(ctx, "ObserveLeaderSkip", func() {
			c.observer.ObserveLeaderSkip(ctx, string(ci.Instance.DefinitionID), reason)
		})
		return nil, nil, false
	}
	rel := func() {
		if rerr := lock.Release(); rerr != nil {
			// lock_key names the distlock efficiency lock; lease_id is the journal
			// fencing token (ci.LeaseID) of the ClaimPending cycle this drive ran
			// under. They are distinct leases (see package doc) — lease_id is logged
			// purely for claim-cycle correlation, consistent with every other
			// per-instance saga log, not because the distlock is fenced by it.
			c.logger.WarnContext(ctx, "saga: distlock release failed",
				slog.String("instance_id", string(ci.Instance.ID)),
				slog.String("definition_id", string(ci.Instance.DefinitionID)),
				slog.String("lock_key", key),
				slog.String("lease_id", string(ci.LeaseID)),
				slog.Any("error", rerr))
		}
	}
	orp := func() { lock.Orphan() }
	return rel, orp, true
}

// classifyLeaderSkip maps an acquireLead error to its typed skip reason. This
// is the single source feeding both the slog level (via leaderSkipLogLevel) and
// the saga_leader_elect_skip_total{reason} metric, so log and metric never
// diverge. Contention (ErrDistlockTimeout) and ctx cancellation are expected;
// anything else is a distlock backend I/O fault (the lock-acquire failure rate).
//
// Add new skip paths only by extending this function (and its downstream
// consumers leaderSkipLogLevel + acquireLead's safeObserve call) — never by
// classifying inline at a callsite, or log and metric will drift.
func classifyLeaderSkip(err error) executor.LeaderSkipReason {
	var ec *errcode.Error
	switch {
	case errors.As(err, &ec) && ec.Code == errcode.ErrDistlockTimeout:
		return executor.LeaderSkipContended
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return executor.LeaderSkipCtxCanceled
	default:
		return executor.LeaderSkipBackendError
	}
}

// leaderSkipLogLevel derives the slog level from the skip reason: backend I/O
// faults are operationally interesting (Warn); contention and ctx cancellation
// are expected operational signals, not faults (Debug).
func leaderSkipLogLevel(reason executor.LeaderSkipReason) slog.Level {
	if reason == executor.LeaderSkipBackendError {
		return slog.LevelWarn
	}
	return slog.LevelDebug
}

// logLeaderSkip logs an acquireLead miss at the level matching its cause.
func (c *Coordinator) logLeaderSkip(
	ctx context.Context, ci journal.ClaimedInstance, key string, err error, reason executor.LeaderSkipReason,
) {
	// lease_id is the journal fencing token (ci.LeaseID); lock_key is the
	// distlock identity — distinct leases (see package doc / acquireLead). lease_id
	// is logged for claim-cycle correlation, uniform with all per-instance logs.
	c.logger.Log(ctx, leaderSkipLogLevel(reason), "saga: leader-elect skip (lock not acquired)",
		slog.String("instance_id", string(ci.Instance.ID)),
		slog.String("definition_id", string(ci.Instance.DefinitionID)),
		slog.String("lock_key", key),
		slog.String("lease_id", string(ci.LeaseID)),
		slog.String("reason", string(reason)),
		slog.Any("error", err))
}
