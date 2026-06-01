package postgres

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Compile-time assertion: *ReconcileElector satisfies reconcile.LeaderElector.
var _ reconcile.LeaderElector = (*ReconcileElector)(nil)

const (
	// pgTryAdvisoryLockSQL takes a SESSION-scoped advisory lock keyed on the
	// reconcilerID hash. It is held for the lease lifetime on a dedicated pooled
	// connection, so a crashed leader's lock auto-releases when its session drops
	// (instant failover — the ADR §4.1 rationale for advisory over pure TTL).
	pgTryAdvisoryLockSQL = `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`
	pgAdvisoryUnlockSQL  = `SELECT pg_advisory_unlock(hashtextextended($1, 0))`

	// pgReconcileUpsertSQL records the holder + lease window and returns the
	// monotonic epoch: bumped on a holder CHANGE (or post-expiry re-acquire), kept
	// on an idempotent same-holder still-live re-acquire. Only the advisory-lock
	// holder reaches this statement, so the row is mutated by one writer at a time.
	pgReconcileUpsertSQL = `
INSERT INTO reconcile_leases (reconciler_id, holder_id, epoch, acquired_at, expires_at)
VALUES ($1, $2, 1, $3, $4)
ON CONFLICT (reconciler_id) DO UPDATE SET
    epoch = CASE
        WHEN reconcile_leases.holder_id = EXCLUDED.holder_id AND reconcile_leases.expires_at > $3
        THEN reconcile_leases.epoch
        ELSE reconcile_leases.epoch + 1
    END,
    holder_id = EXCLUDED.holder_id,
    acquired_at = EXCLUDED.acquired_at,
    expires_at = EXCLUDED.expires_at
RETURNING epoch`

	// pgReconcileRenewSQL extends the lease window, holder-guarded (the row-level
	// correctness CAS atop the advisory lock). 0 rows ⟹ no longer the holder.
	pgReconcileRenewSQL = `UPDATE reconcile_leases SET expires_at = $3 WHERE reconciler_id = $1 AND holder_id = $2`

	// pgReconcileRefreshSQL is the idempotent same-holder re-acquire: refresh the
	// window and return the (unchanged) epoch.
	pgReconcileRefreshSQL = `UPDATE reconcile_leases SET expires_at = $3 WHERE reconciler_id = $1 AND holder_id = $2 RETURNING epoch`
)

// ReconcileElector implements reconcile.LeaderElector using a session-scoped
// pg_try_advisory_lock for the holder gate and a reconcile_leases row for the
// monotonic fencing epoch + lease window. Each held lease keeps a dedicated
// pooled connection for its advisory-lock session lifetime; ReleaseLease unlocks
// and returns it (destroying the physical connection if the unlock fails, so a
// stuck lock never rides a reused pooled connection).
type ReconcileElector struct {
	pool          *Pool
	holderID      string
	leaseDuration time.Duration

	mu   sync.Mutex
	held map[string]*pgxpool.Conn // reconcilerID -> held advisory-lock session conn
}

// NewReconcileElector builds a PG leader elector. holderID identifies this replica
// and MUST be unique per process (production callers pass idutil.NewUUID(); tests
// pass a stable label). leaseDuration is the lease TTL window.
func NewReconcileElector(pool *Pool, holderID string, leaseDuration time.Duration) (*ReconcileElector, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterPGConnect, "postgres reconcile elector: pool is nil")
	}
	if holderID == "" {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterPGConnect, "postgres reconcile elector: holderID is required")
	}
	if leaseDuration <= 0 {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterPGConnect, "postgres reconcile elector: leaseDuration must be positive")
	}
	return &ReconcileElector{pool: pool, holderID: holderID, leaseDuration: leaseDuration, held: make(map[string]*pgxpool.Conn)}, nil
}

// AcquireLease implements reconcile.LeaderElector.
func (e *ReconcileElector) AcquireLease(ctx context.Context, reconcilerID string) (reconcile.LeaseToken, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	expires := now.Add(e.leaseDuration)

	// Idempotent re-acquire: we already hold this lease's session — refresh + keep epoch.
	if conn, ok := e.held[reconcilerID]; ok {
		var epoch int64
		err := conn.QueryRow(ctx, pgReconcileRefreshSQL, reconcilerID, e.holderID, expires).Scan(&epoch)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			e.releaseConn(ctx, reconcilerID, conn) // row vanished — drop stale conn, fall through to fresh acquire
		case err != nil:
			return reconcile.LeaseToken{}, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "postgres reconcile elector: refresh", err)
		default:
			return leaseToken(reconcilerID, e.holderID, uint64(epoch), now, expires), nil
		}
	}

	conn, err := e.pool.DB().Acquire(ctx)
	if err != nil {
		return reconcile.LeaseToken{}, errcode.Wrap(errcode.KindInternal, ErrAdapterPGConnect, "postgres reconcile elector: acquire connection", err)
	}

	var locked bool
	if err := conn.QueryRow(ctx, pgTryAdvisoryLockSQL, reconcilerID).Scan(&locked); err != nil {
		conn.Release()
		return reconcile.LeaseToken{}, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "postgres reconcile elector: advisory lock", err)
	}
	if !locked {
		conn.Release()
		return reconcile.LeaseToken{}, reconcile.ErrLeaseHeld
	}

	var epoch int64
	if err := conn.QueryRow(ctx, pgReconcileUpsertSQL, reconcilerID, e.holderID, now, expires).Scan(&epoch); err != nil {
		e.releaseConn(ctx, reconcilerID, conn) // unlock + return the conn before surfacing the error
		return reconcile.LeaseToken{}, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "postgres reconcile elector: upsert epoch", err)
	}
	e.held[reconcilerID] = conn
	return leaseToken(reconcilerID, e.holderID, uint64(epoch), now, expires), nil
}

// RenewLease implements reconcile.LeaderElector. We hold the advisory lock on a
// pinned connection, so the holder-guarded row UPDATE confirms ownership; a
// missing held connection (we released) or a 0-row UPDATE (holder changed) is a
// lost lease.
func (e *ReconcileElector) RenewLease(ctx context.Context, token reconcile.LeaseToken) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	conn, ok := e.held[token.ReconcilerID]
	if !ok {
		return reconcile.ErrReconcileLeaseLost
	}
	tag, err := conn.Exec(ctx, pgReconcileRenewSQL, token.ReconcilerID, e.holderID, time.Now().Add(e.leaseDuration))
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "postgres reconcile elector: renew", err)
	}
	if tag.RowsAffected() == 0 {
		return reconcile.ErrReconcileLeaseLost
	}
	return nil
}

// ReleaseLease implements reconcile.LeaderElector (unlock + return the pinned
// connection; idempotent). The epoch row is left intact so the next holder
// observes a strictly higher epoch on takeover.
func (e *ReconcileElector) ReleaseLease(_ context.Context, token reconcile.LeaseToken) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	conn, ok := e.held[token.ReconcilerID]
	if !ok {
		return nil
	}
	e.releaseConn(context.Background(), token.ReconcilerID, conn)
	return nil
}

// releaseConn unlocks the session advisory lock and returns the pinned connection
// to the pool. If the unlock fails (or reports the lock was not held), the
// physical connection is destroyed via Hijack+Close so a stuck advisory lock
// never rides a reused pooled connection. Caller MUST hold e.mu. Uses a detached
// ctx so a shutdown release still attempts the unlock.
func (e *ReconcileElector) releaseConn(_ context.Context, reconcilerID string, conn *pgxpool.Conn) {
	delete(e.held, reconcilerID)
	unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var unlocked bool
	err := conn.QueryRow(unlockCtx, pgAdvisoryUnlockSQL, reconcilerID).Scan(&unlocked)
	if err != nil || !unlocked {
		raw := conn.Hijack()
		_ = raw.Close(unlockCtx)
		return
	}
	conn.Release()
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
