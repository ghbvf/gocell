package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

// Compile-time assertion: PGUserRepo implements ports.UserRepository.
var _ ports.UserRepository = (*PGUserRepo)(nil)

// PGUserRepo is the cell-private PostgreSQL implementation of ports.UserRepository.
// It reads/writes the `users` table (migration 017).
//
// Transaction contract — dual-signal pattern (S3+S5 PR #449 round-3
// clarification). The txRunner field is a *construction-time policy
// declaration*: it fail-fasts at NewPGUserRepo when the L2 caller has not
// wired a real TxRunner (single source of truth for "this repo is intended
// for L2-atomic call sites"). The repo methods themselves do NOT invoke
// txRunner.RunInTx directly because all current write paths are
// single-statement (Create / Update / Delete). Instead, methods extract any
// ambient pgx.Tx from ctx via kernel/persistence.TxCtxKey (the value stored
// by adapters/postgres.TxManager.RunInTx); when no tx is in ctx the methods
// fall through to the pool. The setup service wraps Create + outbox.Write
// in a single TxManager.RunInTx call so both writes share the tx that the
// package-local typed executor picks up here.
//
// Compare runtime/auth/refresh adapters/postgres/refresh_store.go where
// txRunner IS invoked directly because its multi-statement methods (Peek,
// Rotate) need an explicit boundary. PGUserRepo's pattern is the
// "single-statement repo" variant of the same dual-signal contract.
type PGUserRepo struct {
	db pgExecutor
	// txRunner is retained at construction time as policy declaration only —
	// see the type godoc above. Repo methods read tx from ctx, not this field.
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
		db:       newPGExecutor(pool),
		txRunner: txRunner,
		clock:    clk,
	}, nil
}

const (
	insertUserSQL = `
INSERT INTO users (
    id, username, email, password_hash, password_reset_required,
    status, creation_source, authz_epoch, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, 0, $8, $9)`

	selectUserByIDSQL = `
SELECT id, username, email, password_hash, password_version, password_reset_required, status, creation_source, created_at, updated_at
FROM users
WHERE id = $1`

	selectUserByUsernameSQL = `
SELECT id, username, email, password_hash, password_version, password_reset_required, status, creation_source, created_at, updated_at
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

	// updatePasswordSQL is the CAS-guarded password write. WHERE id=$4 AND
	// password_version=$5 ensures that a stale view (from a concurrent change)
	// results in 0 RowsAffected, which CheckVersionMatch translates to
	// ErrVersionConflict (HTTP 409). RETURNING password_version gives the
	// caller the new monotonic version without a second round-trip.
	//nolint:gosec // G101: SQL constant containing "password" column name, not a credential value
	updatePasswordSQL = `
UPDATE users
SET password_hash = $1,
    password_reset_required = $2,
    password_version = password_version + 1,
    updated_at = $3
WHERE id = $4 AND password_version = $5
RETURNING password_version`
)

// Create inserts a new user row. Returns ErrAuthUserDuplicate on unique
// constraint violation (username or email already taken).
func (r *PGUserRepo) Create(ctx context.Context, user *domain.User) error {
	_, err := r.db.Exec(ctx, insertUserSQL,
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
	row := r.db.QueryRow(ctx, selectUserByIDSQL, id)
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
	row := r.db.QueryRow(ctx, selectUserByUsernameSQL, username)
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
// ErrAuthUserNotFound when no row matched. Returns ErrAuthUserDuplicate (409)
// when the updated username or email collides with an existing row.
func (r *PGUserRepo) Update(ctx context.Context, user *domain.User) error {
	tag, err := r.db.Exec(ctx, updateUserSQL,
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
		if isUniqueViolation(err) {
			return errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate,
				"username or email already exists",
				errcode.WithInternal(fmt.Sprintf("id=%s username=%q email=%q", user.ID, user.Username, user.Email)))
		}
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
// Returns ErrAuthLastAdminProtected (403) when the DB trigger rejects the
// delete because the user is the sole admin holder.
func (r *PGUserRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, deleteUserSQL, id)
	if err != nil {
		if isLastAdminProtected(err) {
			return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthLastAdminProtected,
				"delete blocked: last admin",
				errcode.WithCategory(errcode.CategoryAuth),
				errcode.WithInternal(fmt.Sprintf("user_id=%s", id)))
		}
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
// Column order must match selectUserByIDSQL and selectUserByUsernameSQL.
func scanUser(row pgx.Row) (*domain.User, error) {
	var u domain.User
	var status, source string
	err := row.Scan(
		&u.ID,
		&u.Username,
		&u.Email,
		&u.PasswordHash,
		&u.PasswordVersion,
		&u.PasswordResetRequired,
		&status,
		&source,
		&u.CreatedAt,
		&u.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if !domain.ValidUserStatus(domain.UserStatus(status)) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"scanUser: invalid status from DB",
			errcode.WithDetails(slog.String("table", "users"), slog.String("column", "status")),
			errcode.WithInternal(fmt.Sprintf("scanned status=%q", status)))
	}
	if !domain.ValidUserSource(domain.UserSource(source)) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"scanUser: invalid creation_source from DB",
			errcode.WithDetails(slog.String("table", "users"), slog.String("column", "creation_source")),
			errcode.WithInternal(fmt.Sprintf("scanned source=%q", source)))
	}
	u.Status = domain.UserStatus(status)
	u.CreationSource = domain.UserSource(source)
	return &u, nil
}

// UpdatePassword applies a CAS-guarded password write.
//
// It executes updatePasswordSQL (WHERE id=$4 AND password_version=$5). If 0
// rows were affected the method distinguishes "user absent" from "version
// mismatch" via a follow-up GetByID probe — callers receive ErrAuthUserNotFound
// or ErrVersionConflict respectively. On success the new password_version is
// returned.
func (r *PGUserRepo) UpdatePassword(
	ctx context.Context,
	userID string,
	newHash string,
	resetRequired bool,
	expectedPV int64,
) (int64, error) {
	now := r.clock.Now()
	var newPV int64
	err := r.db.QueryRow(ctx, updatePasswordSQL,
		newHash, resetRequired, now, userID, expectedPV,
	).Scan(&newPV)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Distinguish "user does not exist" from "version mismatch".
			if _, gerr := r.GetByID(ctx, userID); gerr != nil {
				return 0, gerr
			}
			// Row exists but version didn't match — CAS conflict.
			return 0, cas.CheckVersionMatch(0, "user", userID)
		}
		return 0, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: update password", err)
	}
	return newPV, nil
}
