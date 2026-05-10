package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Compile-time assertion: PGUserRepo implements ports.UserRepository.
var _ ports.UserRepository = (*PGUserRepo)(nil)

// PGUserRepo is the cell-private PostgreSQL implementation of ports.UserRepository.
// It reads/writes the `users` table (migration 017).
//
// Write paths (Create/Update/Delete) participate in the ambient transaction when
// one is present in ctx (stored under kernel/persistence.TxCtxKey by
// adapters/postgres.TxManager). This is required for outbox atomicity: the setup
// service wraps user.Create + outbox.Write in a single txRunner.RunInTx call.
type PGUserRepo struct {
	pool     *pgxpool.Pool
	txRunner persistence.TxRunner
	clock    clock.Clock
}

// NewPGUserRepo constructs a PGUserRepo. Fails fast on nil dependencies.
func NewPGUserRepo(
	pool *pgxpool.Pool,
	txRunner persistence.TxRunner,
	clk clock.Clock,
) (*PGUserRepo, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGUserRepo: pool must not be nil")
	}
	if validation.IsNilInterface(txRunner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGUserRepo: txRunner must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGUserRepo: clock must not be nil")
	}
	return &PGUserRepo{
		pool:     pool,
		txRunner: txRunner,
		clock:    clk,
	}, nil
}

// execCtx executes a SQL statement using the ambient transaction when one is
// present in ctx (stored by adapters/postgres.TxManager via
// kernel/persistence.TxCtxKey). Falls back to the pool when no tx is in ctx.
func (r *PGUserRepo) execCtx(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return tx.Exec(ctx, sql, args...)
	}
	return r.pool.Exec(ctx, sql, args...)
}

// queryRowCtx queries a single row using the ambient transaction when present.
// Falls back to the pool when no tx is in ctx.
func (r *PGUserRepo) queryRowCtx(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return tx.QueryRow(ctx, sql, args...)
	}
	return r.pool.QueryRow(ctx, sql, args...)
}

const (
	insertUserSQL = `
INSERT INTO users (
    id, username, email, password_hash, password_reset_required,
    status, creation_source, authz_epoch, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, 0, $8, $9)`

	selectUserByIDSQL = `
SELECT id, username, email, password_hash, password_reset_required, status, creation_source, created_at, updated_at
FROM users
WHERE id = $1`

	selectUserByUsernameSQL = `
SELECT id, username, email, password_hash, password_reset_required, status, creation_source, created_at, updated_at
FROM users
WHERE username = $1`

	// updateUserSQL intentionally does NOT include authz_epoch in the SET list.
	// Bumping the epoch is a separate, distinct operation per ADR-credential D2
	// (S4 wires the per-event bump path). Calling Update() after a credential
	// state change (role revoke, password reset, lock, delete) does NOT bump
	// authz_epoch. Use UpdateAuthzEpoch for that purpose.
	updateUserSQL = `
UPDATE users
SET username = $2, email = $3, password_hash = $4, password_reset_required = $5,
    status = $6, creation_source = $7, updated_at = $8
WHERE id = $1`

	deleteUserSQL = `DELETE FROM users WHERE id = $1`
)

// Create inserts a new user row. Returns ErrAuthUserDuplicate on unique
// constraint violation (username or email already taken).
func (r *PGUserRepo) Create(ctx context.Context, user *domain.User) error {
	_, err := r.execCtx(ctx, insertUserSQL,
		user.ID,
		user.Username,
		user.Email,
		user.PasswordHash,
		user.PasswordResetRequired,
		string(user.Status),
		string(user.CreationSource),
		user.CreatedAt,
		user.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate,
				"username or email already exists",
				errcode.WithInternal(fmt.Sprintf("username=%q email=%q", user.Username, user.Email)))
		}
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: create", err)
	}
	return nil
}

// GetByID fetches a user by primary key. Returns ErrAuthUserNotFound when absent.
func (r *PGUserRepo) GetByID(ctx context.Context, id string) (*domain.User, error) {
	row := r.queryRowCtx(ctx, selectUserByIDSQL, id)
	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found",
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal(fmt.Sprintf("id=%s", id)))
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: get-by-id", err)
	}
	return u, nil
}

// GetByUsername fetches a user by username. Returns ErrAuthUserNotFound when absent.
func (r *PGUserRepo) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	row := r.queryRowCtx(ctx, selectUserByUsernameSQL, username)
	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found",
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal(fmt.Sprintf("username=%q", username)))
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: get-by-username", err)
	}
	return u, nil
}

// Update overwrites the mutable fields of an existing user. Returns
// ErrAuthUserNotFound when no row matched.
func (r *PGUserRepo) Update(ctx context.Context, user *domain.User) error {
	tag, err := r.execCtx(ctx, updateUserSQL,
		user.ID,
		user.Username,
		user.Email,
		user.PasswordHash,
		user.PasswordResetRequired,
		string(user.Status),
		string(user.CreationSource),
		user.UpdatedAt,
	)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: update", err)
	}
	if tag.RowsAffected() == 0 {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("id=%s", user.ID)))
	}
	return nil
}

// Delete removes a user row. Returns ErrAuthUserNotFound when no row matched.
func (r *PGUserRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.execCtx(ctx, deleteUserSQL, id)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: delete", err)
	}
	if tag.RowsAffected() == 0 {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal(fmt.Sprintf("id=%s", id)))
	}
	return nil
}

// UpdateAuthzEpoch atomically sets authz_epoch for the given user to newEpoch.
//
// This method is a stub that will be wired in S4 when the credential-event
// bump path is introduced. Its presence here makes the gap visible at compile
// time: any S4 call site that needs to bump the epoch can import this method
// directly rather than discovering the gap at review time.
//
// ADR-credential D2: authz_epoch must be bumped on every credential state
// change (role revoke, password reset, lock, delete). The bump is a distinct
// SQL operation, separate from Update(), and must happen inside the same
// transaction as the credential event.
//
// Returns errcode.ErrInternal until S4 lands the real implementation.
func (r *PGUserRepo) UpdateAuthzEpoch(_ context.Context, _ string, _ int64) error {
	return errcode.New(errcode.KindInternal, errcode.ErrInternal,
		"PGUserRepo.UpdateAuthzEpoch: S4 wiring not yet landed",
		errcode.WithInternal("call site reached the bump stub before S4 cell rewiring"))
}

// scanUser scans a single Row into a domain.User.
func scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	var status, source string
	err := row.Scan(
		&u.ID,
		&u.Username,
		&u.Email,
		&u.PasswordHash,
		&u.PasswordResetRequired,
		&status,
		&source,
		&u.CreatedAt,
		&u.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	u.Status = domain.UserStatus(status)
	u.CreationSource = domain.UserSource(source)
	return &u, nil
}
