package postgres

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// Compile-time assertion: *ReconcileElector satisfies reconcile.LeaderElector.
var _ reconcile.LeaderElector = (*ReconcileElector)(nil)

const (
	// pgReconcileAcquireSQL is the row-TTL lease authority: a single atomic UPSERT
	// whose ON CONFLICT WHERE clause makes the reconcile_leases ROW (its expires_at
	// TTL) — NOT a session advisory lock — the lease authority. A follower can take
	// over once expires_at has passed, so failover triggers on TTL expiry even when
	// the previous leader's process is hung (TCP session alive, no crash) — the case
	// a session-scoped pg_try_advisory_lock could NOT express (ADR §4.1 corrected,
	// PR-A6 review C2). Postgres' row lock during the UPSERT serializes concurrent
	// acquirers (the loser re-evaluates WHERE against the winner's fresh row and
	// gets 0 rows), so no advisory lock is needed.
	//
	// Epoch (the monotonic fencing token) is bumped on a holder CHANGE or a
	// post-expiry takeover and kept on an idempotent same-holder live re-acquire.
	// Timestamps use the DB clock now() — the single time authority across replicas,
	// so no Go clock is injected. $3 is the lease TTL in milliseconds.
	//
	// WHERE (expired OR ours): a live lease held by ANOTHER holder fails the WHERE,
	// so the UPDATE touches 0 rows → RETURNING is empty → contention (ErrLeaseHeld).
	pgReconcileAcquireSQL = `
INSERT INTO reconcile_leases (reconciler_id, holder_id, epoch, acquired_at, expires_at)
VALUES ($1, $2, 1, now(), now() + ($3 * interval '1 millisecond'))
ON CONFLICT (reconciler_id) DO UPDATE SET
    epoch = CASE
        WHEN reconcile_leases.holder_id = EXCLUDED.holder_id AND reconcile_leases.expires_at > now()
        THEN reconcile_leases.epoch
        ELSE reconcile_leases.epoch + 1
    END,
    holder_id = EXCLUDED.holder_id,
    acquired_at = now(),
    expires_at = EXCLUDED.expires_at
WHERE reconcile_leases.expires_at < now() OR reconcile_leases.holder_id = EXCLUDED.holder_id
RETURNING epoch, acquired_at, expires_at`

	// pgReconcileRenewSQL extends the lease window, guarded on still-ours AND
	// still-live (expires_at > now()): a renew of an already-lapsed or taken-over
	// lease affects 0 rows ⟹ ErrReconcileLeaseLost (fail-closed — a follower may
	// have taken over the instant our TTL passed).
	pgReconcileRenewSQL = `
UPDATE reconcile_leases SET expires_at = now() + ($3 * interval '1 millisecond')
WHERE reconciler_id = $1 AND holder_id = $2 AND expires_at > now()`

	// pgReconcileReleaseSQL relinquishes our lease by marking it expired (a graceful
	// handoff: the next acquirer sees expires_at < now() and takes over). The epoch
	// row persists so the next holder's takeover bumps from the current epoch
	// (monotonicity). Holder-guarded + idempotent (0 rows if not ours).
	pgReconcileReleaseSQL = `
UPDATE reconcile_leases SET expires_at = now() - interval '1 second'
WHERE reconciler_id = $1 AND holder_id = $2`
)

// epochToUint64 converts the BIGINT epoch column to uint64. reconcile_leases.epoch
// is monotonic, seeded at 1 and only ever incremented, so it is never negative by
// construction; a negative value would be DB corruption.
func epochToUint64(e int64) uint64 {
	if e < 0 {
		return 0
	}
	return uint64(e)
}

// ReconcileElector implements reconcile.LeaderElector using the reconcile_leases
// ROW's expires_at TTL as the lease authority (row-TTL CAS), NOT a session
// advisory lock. It is STATELESS — every call is one pool query, so it is safe for
// concurrent use across reconcilerIDs and holds no connection between calls.
// Failover triggers on TTL expiry (a hung-but-alive leader's lease lapses and a
// follower takes over), matching the Redis and fake electors.
type ReconcileElector struct {
	pool     *Pool
	holderID string
	lease    reconcile.LeaseTTL
}

// NewReconcileElector builds a PG leader elector. The holderID (this replica's
// identity) is minted INTERNALLY as a fresh UUID — making accidental cross-process
// holderID reuse (which would be treated as the same holder, defeating mutual
// exclusion — PR-A6 review C4) impossible by construction rather than relying on a
// caller contract. lease is the validated lease TTL window (a sealed
// reconcile.LeaseTTL, so a sub-millisecond window — which would truncate to a 0ms
// interval and break mutual exclusion — is unrepresentable). Unlike the Redis
// elector, no clock.Clock is needed — lease timestamps are computed by the DB
// (now()), the single wall-clock authority across replicas.
func NewReconcileElector(pool *Pool, lease reconcile.LeaseTTL) (*ReconcileElector, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterPGConnect, "postgres reconcile elector: pool is nil")
	}
	if lease.IsZero() {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterPGConnect, "postgres reconcile elector: lease must be a constructed LeaseTTL")
	}
	holderID, err := idutil.NewUUID()
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGConnect, "postgres reconcile elector: mint holderID", err)
	}
	slog.Info("postgres reconcile elector: created", slog.String("holder_id", holderID))
	return &ReconcileElector{pool: pool, holderID: holderID, lease: lease}, nil
}

// AcquireLease implements reconcile.LeaderElector via the row-TTL UPSERT CAS.
func (e *ReconcileElector) AcquireLease(ctx context.Context, reconcilerID string) (reconcile.LeaseToken, error) {
	var epoch int64
	var acquiredAt, expiresAt time.Time
	err := e.pool.DB().
		QueryRow(ctx, pgReconcileAcquireSQL, reconcilerID, e.holderID, e.lease.Milliseconds()).
		Scan(&epoch, &acquiredAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT WHERE matched no row: a live lease is held by another holder.
		return reconcile.LeaseToken{}, reconcile.ErrLeaseHeld
	}
	if err != nil {
		return reconcile.LeaseToken{}, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "postgres reconcile elector: acquire", err)
	}
	return leaseToken(reconcilerID, e.holderID, epochToUint64(epoch), acquiredAt, expiresAt), nil
}

// RenewLease implements reconcile.LeaderElector (holder + still-live guarded). 0
// rows ⟹ lease lapsed or taken over ⟹ ErrReconcileLeaseLost.
func (e *ReconcileElector) RenewLease(ctx context.Context, token reconcile.LeaseToken) error {
	tag, err := e.pool.DB().Exec(ctx, pgReconcileRenewSQL, token.ReconcilerID, e.holderID, e.lease.Milliseconds())
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "postgres reconcile elector: renew", err)
	}
	if tag.RowsAffected() == 0 {
		return reconcile.ErrReconcileLeaseLost
	}
	return nil
}

// ReleaseLease implements reconcile.LeaderElector (marks our lease expired for a
// fast handoff; idempotent — 0 rows if no longer ours). The epoch row persists so
// the next holder observes a strictly higher epoch on takeover.
func (e *ReconcileElector) ReleaseLease(ctx context.Context, token reconcile.LeaseToken) error {
	if _, err := e.pool.DB().Exec(ctx, pgReconcileReleaseSQL, token.ReconcilerID, e.holderID); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "postgres reconcile elector: release", err)
	}
	return nil
}

func leaseToken(reconcilerID, holderID string, epoch uint64, acquiredAt, expiresAt time.Time) reconcile.LeaseToken {
	return reconcile.LeaseToken{
		ReconcilerID: reconcilerID,
		HolderID:     holderID,
		Epoch:        epoch,
		AcquiredAt:   acquiredAt,
		ExpiresAt:    expiresAt,
	}
}
