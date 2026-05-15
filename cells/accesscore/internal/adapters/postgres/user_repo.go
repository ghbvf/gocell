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
	// S4d: authz_epoch is sourced from the domain.User (NewUser sets it to 1
	// — the unset sentinel is 0, which session/refresh stores reject).
	// Bumping is still exclusive to UpdateAuthzEpoch / BumpAuthzEpoch; this
	// INSERT only seeds the initial value from the in-memory aggregate.
	insertUserSQL = `
INSERT INTO users (
    id, username, email, password_hash, password_reset_required,
    status, creation_source, authz_epoch, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	selectUserByIDSQL = `
SELECT id, username, email, password_hash, password_version, password_reset_required,
       status, creation_source, authz_epoch, created_at, updated_at
FROM users
WHERE id = $1`

	selectUserByUsernameSQL = `
SELECT id, username, email, password_hash, password_version, password_reset_required,
       status, creation_source, authz_epoch, created_at, updated_at
FROM users
WHERE username = $1`

	// selectUserByIDForUpdateSQL / selectUserByUsernameForUpdateSQL (S4d): row-level
	// write lock (FOR UPDATE) so sessionlogin's read-mint-INSERT cycle is
	// serialized against any concurrent credentialinvalidate.Invalidator.Apply
	// (which holds the same lock during BumpAuthzEpoch). Must run inside an
	// ambient transaction; ambient executor joins it automatically.
	//
	// ref: PostgreSQL 13+ Row-Level Locks — SELECT ... FOR UPDATE blocks UPDATE on
	// the locked row until COMMIT.
	selectUserByIDForUpdateSQL = `
SELECT id, username, email, password_hash, password_version, password_reset_required,
       status, creation_source, authz_epoch, created_at, updated_at
FROM users
WHERE id = $1
FOR UPDATE`

	selectUserByUsernameForUpdateSQL = `
SELECT id, username, email, password_hash, password_version, password_reset_required,
       status, creation_source, authz_epoch, created_at, updated_at
FROM users
WHERE username = $1
FOR UPDATE`

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

	// bumpAuthzEpochSQL atomically increments authz_epoch and returns the new value.
	// Must be called inside an ambient transaction provided by the credential-invalidation funnel.
	bumpAuthzEpochSQL = `UPDATE users SET authz_epoch = authz_epoch + 1 WHERE id = $1 RETURNING authz_epoch`

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
		user.AuthzEpoch,
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
		// scanUser may return errcode.ErrPGSchemaShape for invalid enum drift
		// from the DB; propagate that code instead of collapsing to ErrInternal
		// so operators can distinguish schema-drift faults from generic infra
		// failures (e.g. /readyz?verbose triage).
		var ec *errcode.Error
		if errors.As(err, &ec) && ec.Code == errcode.ErrPGSchemaShape {
			return nil, err
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
		var ec *errcode.Error
		if errors.As(err, &ec) && ec.Code == errcode.ErrPGSchemaShape {
			return nil, err
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: get-by-username", err)
	}
	return u, nil
}

// GetByIDForUpdate (S4d) — see ports.UserRepository godoc. Acquires a row
// lock via SELECT ... FOR UPDATE; caller MUST be inside an ambient tx for
// the lock to persist across the read-modify-write window.
func (r *PGUserRepo) GetByIDForUpdate(ctx context.Context, id string) (*domain.User, error) {
	row := r.db.QueryRow(ctx, selectUserByIDForUpdateSQL, id)
	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found",
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal(fmt.Sprintf("id=%s", id)))
		}
		var ec *errcode.Error
		if errors.As(err, &ec) && ec.Code == errcode.ErrPGSchemaShape {
			return nil, err
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: get-by-id-for-update", err)
	}
	return u, nil
}

// GetByUsernameForUpdate (S4d) — see ports.UserRepository godoc.
func (r *PGUserRepo) GetByUsernameForUpdate(ctx context.Context, username string) (*domain.User, error) {
	row := r.db.QueryRow(ctx, selectUserByUsernameForUpdateSQL, username)
	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found",
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal(fmt.Sprintf("username=%q", username)))
		}
		var ec *errcode.Error
		if errors.As(err, &ec) && ec.Code == errcode.ErrPGSchemaShape {
			return nil, err
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: get-by-username-for-update", err)
	}
	return u, nil
}

// Update overwrites the mutable fields of an existing user. Returns
// ErrAuthUserNotFound when no row matched. Returns ErrAuthUserDuplicate (409)
// when the updated username or email collides with an existing row. Returns
// ErrAuthLastAdminProtected (403) when the migration-024 trigger on `users`
// rejects the UPDATE because the row is the sole effective admin and the
// status would demote (active → suspended/locked) — same errcode as the
// application-layer guard so client handlers match a single business
// invariant regardless of which layer caught the violation.
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
		if isLastAdminProtected(err) {
			return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthLastAdminProtected,
				"cannot remove the last effective admin",
				errcode.WithCategory(errcode.CategoryAuth),
				errcode.WithInternal(fmt.Sprintf("id=%s status=%q", user.ID, string(user.Status))))
		}
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
// Returns ErrAuthLastAdminProtected (403) when the migration-024 trigger on
// `users` rejects the delete because the row is the sole effective admin —
// same errcode + message as PGUserRepo.Update / domain.LastAdminGuard so
// client handlers match a single business invariant regardless of which
// layer caught the violation.
func (r *PGUserRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.db.Exec(ctx, deleteUserSQL, id)
	if err != nil {
		if isLastAdminProtected(err) {
			return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthLastAdminProtected,
				"cannot remove the last effective admin",
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

// BumpAuthzEpoch atomically increments users.authz_epoch by 1 and returns the
// new value. It must be called inside an ambient transaction — the
// credential-invalidation funnel entry point guarantees this. Returns
// ErrAuthUserNotFound (KindNotFound) when no row matches userID.
func (r *PGUserRepo) BumpAuthzEpoch(ctx context.Context, userID string) (int64, error) {
	var newEpoch int64
	err := r.db.QueryRow(ctx, bumpAuthzEpochSQL, userID).Scan(&newEpoch)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "user not found",
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal(fmt.Sprintf("id=%s", userID)))
		}
		return 0, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "user_repo: bump authz epoch", err)
	}
	return newEpoch, nil
}

// scanUser scans a single Row into a domain.User.
// Column order must match selectUserByIDSQL and selectUserByUsernameSQL.
//
// authz_epoch is included so that sessionvalidate's epoch invariant
// (user.AuthzEpoch != claims.AuthzEpoch → 401) sees the post-bump value;
// omitting it silently leaves AuthzEpoch=0 on every read and breaks the
// credential-invalidation chain (Finding #1 / PR #490 review).
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
		&u.AuthzEpoch,
		&u.CreatedAt,
		&u.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	if !domain.ValidUserStatus(domain.UserStatus(status)) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrPGSchemaShape,
			"scanUser: invalid status from DB",
			errcode.WithDetails(slog.String("table", "users"), slog.String("column", "status")),
			errcode.WithInternal(fmt.Sprintf("scanned status=%q", status)))
	}
	if !domain.ValidUserSource(domain.UserSource(source)) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrPGSchemaShape,
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
