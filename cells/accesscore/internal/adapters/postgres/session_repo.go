// Package postgres provides a PostgreSQL implementation of accesscore internal ports.
//
// It does NOT import adapters/postgres — it resolves the ambient pgx.Tx from
// ctx via persistence.TxCtxKey (kernel-owned) and falls back to the pool for
// non-transactional reads. This keeps the cell decoupled from the adapter
// layer (CLAUDE.md: cells/ must not import adapters/).
//
// ref: cells/accesscore/internal/adapters/postgres/user_repo.go (Dev A pattern)
// ref: cells/configcore/internal/adapters/postgres/session.go (TxCtxKey pattern)
// ref: jackc/pgx v5 pgconn PgError 23505 unique_violation detection
// ref: ory/kratos persistence/sql persister_session.go (RevokeByIDAndOwner)
// ref: K8s apimachinery ResourceVersion (session.version optimistic lock)
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
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Compile-time assertion: PGSessionRepository implements ports.SessionRepository.
var _ ports.SessionRepository = (*PGSessionRepository)(nil)

// ErrSessionRepoQuery is the error code for unexpected PostgreSQL query failures in
// the accesscore session repository. Mirrors the adapter-level sentinel
// (ErrAdapterPGQuery = "ERR_ADAPTER_PG_QUERY") so monitoring can group all PG
// query failures under a single code. The adapter package cannot be re-imported
// because cells/ must not depend on adapters/ (depguard cells-isolation rule).
const ErrSessionRepoQuery errcode.Code = "ERR_ADAPTER_PG_QUERY"

const (
	insertSessionSQL = `
INSERT INTO sessions (id, user_id, access_token, expires_at, revoked_at, created_at, version)
VALUES ($1, $2, $3, $4, $5, $6, $7)`

	selectSessionByIDSQL = `
SELECT id, user_id, access_token, expires_at, revoked_at, created_at, version
FROM sessions
WHERE id = $1`

	// updateSessionSQL performs an optimistic-lock UPDATE: WHERE id=$1 AND version=$2.
	// On success, version advances by 1.
	// RowsAffected == 0 means either the row does not exist or the version was stale.
	//
	// ref: K8s apimachinery ResourceVersion CAS pattern
	updateSessionSQL = `
UPDATE sessions
SET access_token = $3, expires_at = $4, revoked_at = $5, version = version + 1
WHERE id = $1 AND version = $2`

	revokeByIDAndOwnerSQL = `
DELETE FROM sessions
WHERE id = $1 AND user_id = $2`

	revokeByUserIDSQL = `
DELETE FROM sessions
WHERE user_id = $1`

	deleteSessionSQL = `
DELETE FROM sessions
WHERE id = $1`
)

// PGSessionRepository implements ports.SessionRepository on PostgreSQL.
//
// Construction: error-first 2-param signature; nil checks fail-fast
// (PG-CONSTRUCTOR-MUST-FREE-01: no MustNew* variant is provided).
//
// TX semantics: ambient — if a pgx.Tx is stored in ctx under
// persistence.TxCtxKey (placed there by adapters/postgres.TxManager), each
// method joins it. Otherwise the call falls back to the pool. This is
// identical to the pattern in cells/accesscore/internal/adapters/postgres/user_repo.go.
//
// Optimistic concurrency: Update uses WHERE id=$1 AND version=$2. RowsAffected==0
// is disambiguated: a follow-up GetByID distinguishes "not found" from
// "version conflict".
//
// All CRUD methods are single-statement — no pool.Begin / BeginTx call is made
// (PG-REPO-AMBIENT-TX-01).
type PGSessionRepository struct {
	pool *pgxpool.Pool
	clk  clock.Clock
}

// NewPGSessionRepository constructs a PGSessionRepository backed by the provided pool.
//
// Returns a non-nil error if pool or clk are nil (including typed-nil).
func NewPGSessionRepository(pool *pgxpool.Pool, clk clock.Clock) (*PGSessionRepository, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"pg.NewPGSessionRepository: pool must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"pg.NewPGSessionRepository: clock must not be nil")
	}
	return &PGSessionRepository{pool: pool, clk: clk}, nil
}

// execCtx executes a SQL statement against the ambient pgx.Tx when one is
// present in ctx (persistence.TxCtxKey), falling back to the pool.
func (r *PGSessionRepository) execCtx(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return tx.Exec(ctx, sql, args...)
	}
	return r.pool.Exec(ctx, sql, args...)
}

// queryRowCtx queries a single row against the ambient pgx.Tx when one is
// present in ctx (persistence.TxCtxKey), falling back to the pool.
func (r *PGSessionRepository) queryRowCtx(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx, ok := ctx.Value(persistence.TxCtxKey).(pgx.Tx); ok {
		return tx.QueryRow(ctx, sql, args...)
	}
	return r.pool.QueryRow(ctx, sql, args...)
}

// Create inserts a new session row.
//
// Returns ErrSessionConflict (KindConflict) on UNIQUE 23505 violation
// (access_token uniqueness).
func (r *PGSessionRepository) Create(ctx context.Context, session *domain.Session) error {
	_, err := r.execCtx(ctx, insertSessionSQL,
		session.ID,
		session.UserID,
		session.AccessToken,
		session.ExpiresAt,
		session.RevokedAt,
		session.CreatedAt,
		session.Version,
	)
	if err != nil {
		return r.mapCreateError(err, session.ID)
	}
	return nil
}

// mapCreateError converts a raw pgx error from Create into an errcode.Error.
func (r *PGSessionRepository) mapCreateError(err error, sessionID string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		slog.Warn("session create: unique constraint violation",
			slog.String("constraint", pgErr.ConstraintName),
			slog.String("session_id", sessionID),
		)
		return errcode.New(errcode.KindConflict, errcode.ErrSessionConflict,
			"session access token already exists",
			errcode.WithDetails(slog.String("constraint", pgErr.ConstraintName)),
			errcode.WithInternal("session_id="+sessionID),
		)
	}
	return errcode.Wrap(errcode.KindInternal, ErrSessionRepoQuery, "session repo: create", err)
}

// GetByID retrieves a session by primary key.
// Returns ErrSessionNotFound (KindNotFound) when the row does not exist.
func (r *PGSessionRepository) GetByID(ctx context.Context, id string) (*domain.Session, error) {
	row := r.queryRowCtx(ctx, selectSessionByIDSQL, id)
	s, err := scanSession(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound,
				"session not found",
				errcode.WithCategory(errcode.CategoryDomain),
				errcode.WithInternal("id="+id),
			)
		}
		return nil, errcode.Wrap(errcode.KindInternal, ErrSessionRepoQuery, "session repo: get by id", err)
	}
	return s, nil
}

// Update persists a modified session aggregate using optimistic concurrency.
//
// The WHERE clause includes version=$2 so concurrent updates on the same
// version are detected. RowsAffected==0 is disambiguated via a GetByID probe:
// not found → ErrSessionNotFound; version mismatch → ErrSessionConflict.
//
// On success, session.Version is incremented to match the new DB value.
func (r *PGSessionRepository) Update(ctx context.Context, session *domain.Session) error {
	ct, err := r.execCtx(ctx, updateSessionSQL,
		session.ID,
		session.Version,
		session.AccessToken,
		session.ExpiresAt,
		session.RevokedAt,
	)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrSessionRepoQuery, "session repo: update", err)
	}
	if ct.RowsAffected() == 0 {
		// Distinguish not-found from version-conflict via a follow-up probe.
		_, lookupErr := r.GetByID(ctx, session.ID)
		if lookupErr != nil {
			// Row does not exist — propagate the not-found error.
			return lookupErr
		}
		// Row exists but version did not match.
		return errcode.New(errcode.KindConflict, errcode.ErrSessionConflict,
			"session was modified by another request, please retry",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal("id="+session.ID),
		)
	}
	// Advance the caller's version to match what was written.
	session.Version++
	return nil
}

// RevokeByIDAndOwner atomically deletes a session only if both id and
// ownerUserID match. Returns ErrSessionNotFound when the session does not
// exist OR does not belong to the caller — the two cases are intentionally
// conflated to hide enumeration of other users' session ids.
//
// ref: Ory Kratos session/handler.go deleteMySession
func (r *PGSessionRepository) RevokeByIDAndOwner(ctx context.Context, id, ownerUserID string) error {
	ct, err := r.execCtx(ctx, revokeByIDAndOwnerSQL, id, ownerUserID)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrSessionRepoQuery, "session repo: revoke by id and owner", err)
	}
	if ct.RowsAffected() == 0 {
		return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound,
			"session not found",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal("id="+id),
		)
	}
	return nil
}

// RevokeByUserID deletes all sessions for a given user.
// Returns the number of deleted rows and any query error.
func (r *PGSessionRepository) RevokeByUserID(ctx context.Context, userID string) error {
	_, err := r.execCtx(ctx, revokeByUserIDSQL, userID)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrSessionRepoQuery, "session repo: revoke by user id", err)
	}
	return nil
}

// Delete removes a session row by primary key.
// Returns ErrSessionNotFound (KindNotFound) when the row does not exist.
func (r *PGSessionRepository) Delete(ctx context.Context, id string) error {
	ct, err := r.execCtx(ctx, deleteSessionSQL, id)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrSessionRepoQuery, "session repo: delete", err)
	}
	if ct.RowsAffected() == 0 {
		return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound,
			"session not found",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithInternal("id="+id),
		)
	}
	return nil
}

// scanSession scans a pgx.Row into a domain.Session.
func scanSession(row pgx.Row) (*domain.Session, error) {
	var s domain.Session
	err := row.Scan(
		&s.ID,
		&s.UserID,
		&s.AccessToken,
		&s.ExpiresAt,
		&s.RevokedAt,
		&s.CreatedAt,
		&s.Version,
	)
	if err != nil {
		return nil, err
	}
	return &s, nil
}
