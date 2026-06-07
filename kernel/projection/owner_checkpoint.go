package projection

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// OwnerCheckpointStore is the fenced, predicate-based checkpoint contract required
// by the saga-journal Tailer (runtime/saga/tailer) for leader-handoff safety
// (EPIC #1609 ADR D5(b)). It is deliberately NARROW — it exposes only LoadOffset
// (read the committed offset) and AdvanceIfOwner (fenced advance). It does NOT
// embed CheckpointStore and therefore does NOT expose the unconditional
// SaveOffset: a deposed leader holding an OwnerCheckpointStore reference cannot
// bypass fencing by calling SaveOffset, because that method is not in the
// interface (type-system Hard, stronger than an archtest ban).
//
// The outbox-event projection.Coordinator does NOT use this interface — it
// drives push-delivered events under a single owner and reuses the base
// CheckpointStore.SaveOffset. OwnerCheckpointStore exists solely for the
// pull-tailing Tailer, where multiple processes may compete for per-projection
// leadership and a stale leader's checkpoint advance must be rejected.
//
// # Fencing model (mirrors OUTBOX-LEASE-ID-CAS-01, different carrier)
//
// distlock exposes no fence token, so the Tailer mints a fresh ownerToken
// (idutil.NewUUID) on each successful per-projection lock acquisition and passes
// it to every AdvanceIfOwner of that leadership window. AdvanceIfOwner accepts
// when EITHER the caller already owns the checkpoint (ownerToken == recorded
// owner) OR the requested offset is strictly ahead of the committed offset (a
// legitimate new leader advancing past the high-water mark, which atomically
// claims ownership). It rejects with ErrStaleOwner when the token differs AND
// the offset is not ahead — i.e. a deposed leader that lags the new one. The
// distlock mutual-exclusion makes the claiming write race-free under normal
// operation; the CAS is the correctness boundary that fences a stale leader
// during the lock-handoff window (distlock alone is an efficiency lock).
//
// AdvanceIfOwner is ambient-tx, exactly like CheckpointStore.SaveOffset: it
// participates in the caller's transaction via persistence.TxFromContext(ctx)
// (no raw db handle — PROJECTION-CHECKPOINT-TX-BOUND-01), so the offset advance
// commits atomically with the Apply mutation (D5(a)).
//
// Implementations: mem (this PR) verified by
// projectiontest.RunOwnerCheckpointConformance; PG owner-column CAS is deferred
// to PR-PG (gated on a real production consumer; until then the
// projection_checkpoints.owner column stays reserved-but-unwritten). Every
// implementation is forced into conformance by
// SAGA-OWNER-CHECKPOINT-CONFORMANCE-ENROLL-01.
type OwnerCheckpointStore interface {
	// LoadOffset returns the last committed offset for the projection, or 0 if
	// none has been recorded yet (cold start).
	LoadOffset(ctx context.Context, cellID, projectionID string) (int64, error)
	// AdvanceIfOwner advances the committed offset to offset within the ambient
	// transaction carried by ctx, fenced by ownerToken (see the fencing model in
	// the interface doc). It returns ErrStaleOwner when the caller is a deposed
	// leader (different token and not ahead of the committed offset).
	//
	// When the caller's ownerToken matches the recorded owner (same leader),
	// the advance is always accepted regardless of the requested offset — the
	// caller may advance to any value, including a value lower than the last
	// committed offset. Monotonicity across same-owner advances is the caller's
	// responsibility, not the store's.
	//
	// Cold claims (first advance on an empty checkpoint) must use offset >= 1;
	// offset 0 with a new token is not strictly ahead of the committed offset 0
	// and will be rejected with ErrStaleOwner.
	AdvanceIfOwner(ctx context.Context, cellID, projectionID, ownerToken string, offset int64) error
}

// ErrStaleOwner is returned by OwnerCheckpointStore.AdvanceIfOwner when the
// caller's ownerToken does not match the recorded owner and the requested offset
// is not ahead of the committed offset — the caller is a deposed leader and MUST
// stop advancing. Maps to KindConflict / HTTP 409, mirroring the lease-CAS
// rejection semantics of OUTBOX-LEASE-ID-CAS-01 (ErrSagaStaleLease family).
var ErrStaleOwner = errcode.New(errcode.KindConflict, errcode.ErrConflict,
	"projection: checkpoint advance rejected — stale owner token")
