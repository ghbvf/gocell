package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Compile-time assertion: PGSetupLock implements ports.SetupLock.
var _ ports.SetupLock = (*PGSetupLock)(nil)

// PGSetupLock serializes the admin provisioning path across concurrent processes
// via PostgreSQL advisory locks. It uses pg_advisory_xact_lock so the lock is
// automatically released at transaction commit or rollback — no explicit Release
// is required or possible.
//
// Closes backlog ADMINPROVISION-DIST-LOCK-01: multi-pod deployments that both
// hit POST /setup/admin before any admin exists now block on this lock inside
// the same transaction that writes the admin user and emits the outbox event.
// Only the first committer persists; the second reads zero-count at fast-path and
// returns OutcomeAlreadyExists / 410 Gone.
//
// The advisory lock key is derived from the well-known sentinel string
// "accesscore.admin.setup" via hashtextextended(..., 0). This is stable across
// all PG versions and does not collide with table OIDs.
type PGSetupLock struct {
	txRunner persistence.TxRunner
}

// NewPGSetupLock constructs a PGSetupLock. Returns an error when txRunner is nil
// so composition roots fail at construction time rather than at the first Acquire.
func NewPGSetupLock(txRunner persistence.TxRunner) (*PGSetupLock, error) {
	if validation.IsNilInterface(txRunner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGSetupLock: txRunner must not be nil")
	}
	return &PGSetupLock{txRunner: txRunner}, nil
}

const pgAdvisoryLockSQL = `SELECT pg_advisory_xact_lock(hashtextextended('accesscore.admin.setup', 0))`

// Acquire blocks until the advisory lock is granted within the ambient
// transaction. ctx must carry a live pgx.Tx injected by txRunner.RunInTx
// (stored under kernel/persistence.TxCtxKey). If no transaction is present,
// Acquire returns ErrInternal because the xact-scoped lock semantics require
// an enclosing transaction.
func (l *PGSetupLock) Acquire(ctx context.Context) error {
	tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx)
	if !ok || tx == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"PGSetupLock.Acquire: must be called inside a transaction (no pgx.Tx in ctx)")
	}
	if _, err := tx.Exec(ctx, pgAdvisoryLockSQL); err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
			"setup lock: pg_advisory_xact_lock", fmt.Errorf("%w", err))
	}
	return nil
}
