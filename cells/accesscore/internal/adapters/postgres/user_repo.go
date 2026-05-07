// Package postgres provides a PostgreSQL implementation of accesscore internal ports.
//
// It does NOT import adapters/postgres — it resolves the ambient pgx.Tx from
// ctx via persistence.TxCtxKey (kernel-owned) and falls back to the pool for
// non-transactional reads. This keeps the cell decoupled from the adapter
// layer (CLAUDE.md: cells/ must not import adapters/).
//
// ref: cells/configcore/internal/adapters/postgres/session.go (TxCtxKey pattern)
// ref: jackc/pgx v5 pgconn PgError 23505 unique_violation detection
// ref: ory/kratos persistence/sql persister_identity.go
package postgres

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Compile-time assertion: PGUserRepository implements ports.UserRepository.
var _ ports.UserRepository = (*PGUserRepository)(nil)

// pgUniqueViolation is the PostgreSQL error code for UNIQUE constraint violations.
const pgUniqueViolation = "23505"

const (
	insertUserSQL = `
INSERT INTO users (id, username, email, password_hash, password_reset_required, status, creation_source, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	selectUserByIDSQL = `
SELECT id, username, email, password_hash, password_reset_required, status, creation_source, created_at, updated_at
FROM users
WHERE id = $1`

	selectUserByUsernameSQL = `
SELECT id, username, email, password_hash, password_reset_required, status, creation_source, created_at, updated_at
FROM users
WHERE username = $1`

	updateUserSQL = `
UPDATE users
SET username = $2, email = $3, password_hash = $4, password_reset_required = $5,
    status = $6, creation_source = $7, updated_at = $8
WHERE id = $1`

	deleteUserSQL = `
DELETE FROM users
WHERE id = $1`
)

// PGUserRepository implements ports.UserRepository on PostgreSQL.
//
// Construction: error-first 1-param signature; nil check fails-fast
// (PG-CONSTRUCTOR-MUST-FREE-01: no MustNew* variant is provided).
//
// TX semantics: ambient — if a pgx.Tx is stored in ctx under
// persistence.TxCtxKey (placed there by adapters/postgres.TxManager), each
// method joins it. Otherwise the call falls back to the pool. This is identical
// to the pattern in cells/configcore/internal/adapters/postgres/session.go.
//
// All CRUD methods are single-statement — no pool.Begin / BeginTx call is made
// (PG-REPO-AMBIENT-TX-01: that archtest scans adapters/postgres/ only, but the
// same design principle applies here). 23505 UNIQUE violations are mapped to
// ErrAuthUserDuplicate; ConstraintName distinguishes idx_users_username vs
// idx_users_email.
type PGUserRepository struct {
	pool *pgxpool.Pool
}

// NewPGUserRepository constructs a PGUserRepository backed by the provided pool.
//
// Returns a non-nil error if pool is nil.
func NewPGUserRepository(pool *pgxpool.Pool) (*PGUserRepository, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"pg.NewPGUserRepository: pool must not be nil")
	}
	return &PGUserRepository{pool: pool}, nil
}

// execCtx executes a SQL statement against the ambient pgx.Tx when one is
// present in ctx (persistence.TxCtxKey), falling back to the pool.
func (r *PGUserRepository) execCtx(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return tx.Exec(ctx, sql, args...)
	}
	return r.pool.Exec(ctx, sql, args...)
}

// queryRowCtx queries a single row against the ambient pgx.Tx when one is
// present in ctx (persistence.TxCtxKey), falling back to the pool.
func (r *PGUserRepository) queryRowCtx(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return tx.QueryRow(ctx, sql, args...)
	}
	return r.pool.QueryRow(ctx, sql, args...)
}

// Create inserts a new user row.
//
// Returns ErrAuthUserDuplicate (KindConflict) on UNIQUE 23505 violation.
// ConstraintName distinguishes idx_users_username from idx_users_email.
func (r *PGUserRepository) Create(ctx context.Context, user *domain.User) error {
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
		return r.mapCreateError(err, user.Username, user.Email)
	}
	return nil
}

// mapCreateError converts a raw pgx error from Create into an errcode.Error.
func (r *PGUserRepository) mapCreateError(err error, username, email string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		// A9: ConstraintName is internal diagnostic — not exposed in 4xx body.
		// A8: username and email are PII — stay in WithInternal, not wire-visible.
		slog.Warn("user create: unique constraint violation",
			slog.String("constraint", pgErr.ConstraintName),
		)
		switch pgErr.ConstraintName {
		case "idx_users_email":
			return errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate,
				"email already exists",
				errcode.WithInternal("constraint="+pgErr.ConstraintName+" email="+email),
			)
		default:
			// idx_users_username or any other unique constraint
			return errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate,
				"username already exists",
				errcode.WithInternal("constraint="+pgErr.ConstraintName+" username="+username),
			)
		}
	}
	return errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "user repo: create", err)
}

// GetByID retrieves a user by primary key.
// Returns ErrAuthUserNotFound (KindNotFound) when the row does not exist.
func (r *PGUserRepository) GetByID(ctx context.Context, id string) (*domain.User, error) {
	row := r.queryRowCtx(ctx, selectUserByIDSQL, id)
	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound,
				"user not found",
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal("id="+id),
			)
		}
		return nil, errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "user repo: get by id", err)
	}
	return u, nil
}

// GetByUsername retrieves a user by username.
// Returns ErrAuthUserNotFound (KindNotFound) when the row does not exist.
func (r *PGUserRepository) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	row := r.queryRowCtx(ctx, selectUserByUsernameSQL, username)
	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound,
				"user not found",
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal("username="+username),
			)
		}
		return nil, errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "user repo: get by username", err)
	}
	return u, nil
}

// Update persists a modified user aggregate.
// Returns ErrAuthUserNotFound (KindNotFound) when no row was updated (id not found).
func (r *PGUserRepository) Update(ctx context.Context, user *domain.User) error {
	ct, err := r.execCtx(ctx, updateUserSQL,
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
		return errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "user repo: update", err)
	}
	if ct.RowsAffected() == 0 {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound,
			"user not found",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal("id="+user.ID),
		)
	}
	return nil
}

// Delete removes a user row by primary key.
// Returns ErrAuthUserNotFound (KindNotFound) when the row does not exist.
func (r *PGUserRepository) Delete(ctx context.Context, id string) error {
	ct, err := r.execCtx(ctx, deleteUserSQL, id)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "user repo: delete", err)
	}
	if ct.RowsAffected() == 0 {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound,
			"user not found",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal("id="+id),
		)
	}
	return nil
}

// scanUser scans a pgx.Row into a domain.User.
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
