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
// ref: K8s apimachinery resourceVersion CAS pattern (ApplyPatch version gating)
package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

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

// Constraint name constants — used as the uniqueness white-list so no dynamic
// column names are ever interpolated into SQL (SQL-injection prevention).
const (
	constraintUsersUsername = "idx_users_username"
	constraintUsersEmail    = "idx_users_email"
)

const (
	insertUserSQL = `
INSERT INTO users (id, username, email, password_hash, password_reset_required, status, creation_source, created_at, updated_at, version)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`

	selectUserByIDSQL = `
SELECT id, username, email, password_hash, password_reset_required, status, creation_source, created_at, updated_at, version
FROM users
WHERE id = $1`

	selectUserByUsernameSQL = `
SELECT id, username, email, password_hash, password_reset_required, status, creation_source, created_at, updated_at, version
FROM users
WHERE username = $1`

	// selectUserByUsernameForUpdateSQL uses SELECT … FOR UPDATE to acquire a
	// row-level write lock within the caller's ambient transaction. Used by
	// login flows to prevent concurrent modification between the credential
	// check and the session creation.
	//
	// ref: ory/kratos persister_session.go GetSessionByToken FOR UPDATE pattern
	selectUserByUsernameForUpdateSQL = `
SELECT id, username, email, password_hash, password_reset_required, status, creation_source, created_at, updated_at, version
FROM users
WHERE username = $1
FOR UPDATE`

	deleteUserSQL = `
DELETE FROM users
WHERE id = $1`

	// selectUserExistsSQL checks existence without returning all columns.
	// Used by ApplyPatch to distinguish NotFound from ConcurrentUpdate.
	selectUserExistsSQL = `SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)`
)

// PGUserRepository implements ports.UserRepository on PostgreSQL.
//
// Construction: error-first 1-param signature; nil check fails-fast
// (PG-CONSTRUCTOR-MUST-FREE-01: no MustNew* variant is provided).
//
// TX semantics: ambient — if a pgx.Tx is stored in ctx under
// persistence.TxCtxKey (placed there by adapters/postgres.TxManager), each
// method joins it. Otherwise the call falls back to the pool.
//
// Optimistic concurrency: ApplyPatch uses WHERE id=$1 AND version=$N with
// RETURNING so a single round-trip detects both "not found" (no row) and
// "version mismatch" (row exists, version changed). A follow-up EXISTS query
// disambiguates RowsAffected==0 into the two error kinds.
//
// All CRUD methods are single-statement — no pool.Begin / BeginTx call is made
// (PG-REPO-AMBIENT-TX-01).
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
// Returns ErrAuthUserDuplicate (KindConflict) on UNIQUE 23505 violation for
// username; ErrAuthEmailDuplicate for email constraint.
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
		user.Version,
	)
	if err != nil {
		return r.mapUniqueViolation(err, "user repo: create", user.Username, user.Email)
	}
	return nil
}

// mapUniqueViolation converts a 23505 pgx error into an errcode.Error.
// Non-23505 errors are wrapped as KindInternal.
//
// PII safety: email and username are redacted before being written to
// WithInternal (first 3 chars + ***) to limit log-backend PII exposure.
func (r *PGUserRepository) mapUniqueViolation(err error, op, username, email string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		slog.Warn("user repo: unique constraint violation",
			slog.String("op", op),
			slog.String("constraint", pgErr.ConstraintName),
		)
		if pgErr.ConstraintName == constraintUsersEmail {
			return errcode.New(errcode.KindConflict, errcode.ErrAuthEmailDuplicate,
				"email already exists",
				errcode.WithInternal(fmt.Sprintf("constraint=%s email=%s", pgErr.ConstraintName, redactPII(email))),
			)
		}
		// idx_users_username or any other unique constraint.
		return errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate,
			"username already exists",
			errcode.WithInternal(fmt.Sprintf("constraint=%s username=%s", pgErr.ConstraintName, redactPII(username))),
		)
	}
	return errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "user repo: query failed", err,
		errcode.WithInternal("op="+op))
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

// GetByUsernameForUpdate retrieves a user by username with a row-level write
// lock (SELECT … FOR UPDATE). Must be called inside an active TxRunner.RunInTx.
// Returns ErrAuthUserNotFound (KindNotFound) when the row does not exist.
//
// ref: ory/kratos persister_session.go GetSessionByToken FOR UPDATE pattern
func (r *PGUserRepository) GetByUsernameForUpdate(ctx context.Context, username string) (*domain.User, error) {
	row := r.queryRowCtx(ctx, selectUserByUsernameForUpdateSQL, username)
	u, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound,
				"user not found",
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal("username="+username),
			)
		}
		return nil, errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "user repo: get by username for update", err)
	}
	return u, nil
}

// ApplyPatch updates the user atomically, gated on p.CurrentVersion.
//
// The UPDATE only fires when the row's version matches p.CurrentVersion.
// RowsAffected==0 is disambiguated: a follow-up EXISTS query determines
// whether the row is missing (ErrAuthUserNotFound) or was concurrently
// modified (ErrAuthConcurrentUpdate).
//
// Uniqueness constraint violations (23505) are mapped to ErrAuthUserDuplicate
// (username) or ErrAuthEmailDuplicate (email).
//
// ref: K8s apimachinery resourceVersion CAS pattern
// ref: ory/kratos persister_identity.go UpdateIdentity optimistic lock
func (r *PGUserRepository) ApplyPatch(ctx context.Context, p ports.UserPatch) (*domain.User, error) {
	setClauses, args, nextN := buildSetClauses(p)
	if len(setClauses) == 0 {
		// Nothing to update — just return the current state.
		return r.GetByID(ctx, p.ID)
	}

	// Always advance version and set updated_at.
	setClauses = append(setClauses,
		"version = version + 1",
		fmt.Sprintf("updated_at = $%d", nextN),
	)
	args = append(args, p.UpdatedAt)
	nextN++

	// WHERE id=$N AND version=$M
	args = append(args, p.ID, p.CurrentVersion)
	whereIDN := nextN
	whereVerN := nextN + 1

	sql := fmt.Sprintf(`
UPDATE users
SET %s
WHERE id = $%d AND version = $%d
RETURNING id, username, email, password_hash, password_reset_required, status, creation_source, created_at, updated_at, version`,
		strings.Join(setClauses, ", "),
		whereIDN, whereVerN,
	)

	row := r.queryRowCtx(ctx, sql, args...)
	u, err := scanUser(row)
	if err == nil {
		return u, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		// Pass actual patch values so unique-constraint diagnostics include them.
		var email, username string
		if p.Email != nil {
			email = *p.Email
		}
		if p.Username != nil {
			username = *p.Username
		}
		return nil, r.mapUniqueViolation(err, "user repo: apply patch", username, email)
	}

	// RowsAffected==0: distinguish not-found from version mismatch.
	var exists bool
	existsErr := r.queryRowCtx(ctx, selectUserExistsSQL, p.ID).Scan(&exists)
	if existsErr != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errAdapterPGQuery, "user repo: apply patch exists check", existsErr)
	}
	if !exists {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound,
			"user not found",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal("id="+p.ID),
		)
	}
	return nil, errcode.New(errcode.KindConflict, errcode.ErrAuthConcurrentUpdate,
		"user was modified by another request, please retry",
		errcode.WithCategory(errcode.CategoryDomain),
		errcode.WithInternal(fmt.Sprintf("id=%s version_expected=%d", p.ID, p.CurrentVersion)),
	)
}

// buildSetClauses constructs the SET clause fragments for ApplyPatch.
// Only non-nil patch fields are included; column names are hard-coded
// constants (never user-supplied strings) to prevent SQL injection.
//
// Returns the clause list, the corresponding args slice, and nextN — the
// next available positional parameter index ($nextN) for the caller to use
// in WHERE / additional SET clauses. Using the returned nextN removes the
// implicit coupling where the caller had to derive the next index from
// len(args).
func buildSetClauses(p ports.UserPatch) (clauses []string, args []any, nextN int) {
	n := 1

	if p.Username != nil {
		clauses = append(clauses, fmt.Sprintf("username = $%d", n))
		args = append(args, *p.Username)
		n++
	}
	if p.Email != nil {
		clauses = append(clauses, fmt.Sprintf("email = $%d", n))
		args = append(args, *p.Email)
		n++
	}
	if p.PasswordHash != nil {
		clauses = append(clauses, fmt.Sprintf("password_hash = $%d", n))
		args = append(args, *p.PasswordHash)
		n++
	}
	if p.PasswordResetRequired != nil {
		clauses = append(clauses, fmt.Sprintf("password_reset_required = $%d", n))
		args = append(args, *p.PasswordResetRequired)
		n++
	}
	if p.Status != nil {
		clauses = append(clauses, fmt.Sprintf("status = $%d", n))
		args = append(args, string(*p.Status))
		n++
	}
	return clauses, args, n
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
		&u.Version,
	)
	if err != nil {
		return nil, err
	}
	u.Status = domain.UserStatus(status)
	u.CreationSource = domain.UserSource(source)
	return &u, nil
}

// redactPII returns only the first 3 characters of s followed by "***".
// Used to limit PII exposure in WithInternal log fields for email/username.
//
//   - empty string → ""
//   - len ≤ 3 → "***"
//   - otherwise → s[:3] + "***"
func redactPII(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 3 {
		return "***"
	}
	return s[:3] + "***"
}
