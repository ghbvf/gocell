package postgres

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// Compile-time assertions.
var (
	_ session.Store             = (*PGSessionStore)(nil)
	_ lifecycle.ManagedResource = (*PGSessionStore)(nil)
)

// Session SQL statements.
//
// Design: sessions rows are append-only for the revoke semantics (ADR-Session D3);
// revoked_at is a one-way flip — set exactly once, never cleared.
const (
	insertSessionSQL = `
INSERT INTO sessions (id, subject_id, jti, authz_epoch_at_issue, created_at, expires_at)
VALUES ($1, $2::uuid, $3, $4, $5, $6)`

	selectSessionByIDSQL = `
SELECT id, subject_id::text, jti, authz_epoch_at_issue, created_at, expires_at, revoked_at
FROM sessions
WHERE id = $1`

	revokeSessionByIDSQL = `
UPDATE sessions
SET revoked_at = $1
WHERE id = $2
  AND revoked_at IS NULL`

	revokeSessionsBySubjectSQL = `
UPDATE sessions
SET revoked_at = $1
WHERE subject_id = $2::uuid
  AND revoked_at IS NULL`
)

// PGSessionStore implements session.Store over PostgreSQL using pgx.
//
// All time values come from the injected clock (never PG's now()), so the
// FakeClock in storetest drives deterministic behavior.
//
// Consistency: L1 LocalTx — Create/Revoke/RevokeForSubject are single-statement
// writes that participate in the ambient transaction (ADR-credential D5
// same-tx revoke).
//
// Transaction contract: PGSessionStore uses the ambient transaction for all
// write paths (execCtx joins caller's tx). Get uses queryRowCtx which also
// joins the ambient tx when present, enabling consistent reads within a
// business transaction.
//
// ref: dexidp/dex storage/sql/sql.go — session row model
// ref: ory/hydra persistence/sql/persister_oauth2.go — ambient-tx pattern
type PGSessionStore struct {
	pool     *pgxpool.Pool
	txRunner persistence.TxRunner
	protocol *session.Protocol
	clock    clock.Clock
}

// NewSessionStore constructs a PGSessionStore.
//
// Returns a non-nil error if pool, txRunner, protocol, or clock are nil.
//
// pool is retained for direct exec when no ambient transaction is in context;
// txRunner is required for callers that need transaction scoping (e.g. session
// login service). protocol drives fingerprint-shape validation on Create.
// clock is used exclusively for revoked_at timestamps — never PG's NOW().
func NewSessionStore(
	pool *pgxpool.Pool,
	txRunner persistence.TxRunner,
	protocol *session.Protocol,
	clk clock.Clock,
) (*PGSessionStore, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewSessionStore: pool must not be nil")
	}
	if validation.IsNilInterface(txRunner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewSessionStore: txRunner must not be nil")
	}
	if protocol == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewSessionStore: protocol must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"postgres.NewSessionStore: clock must not be nil")
	}
	return &PGSessionStore{
		pool:     pool,
		txRunner: txRunner,
		protocol: protocol,
		clock:    clk,
	}, nil
}

// execCtx executes a SQL statement against the ambient transaction in ctx when
// one is present (join caller's tx so session operations are atomic with the
// surrounding business transaction). Falls back to the pool when no tx is in
// context.
func (s *PGSessionStore) execCtx(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if tx, ok := TxFromContext(ctx); ok {
		return tx.Exec(ctx, sql, args...)
	}
	return s.pool.Exec(ctx, sql, args...)
}

// queryRowCtx queries a single row against the ambient transaction in ctx when
// one is present. Falls back to the pool when no tx is in context.
func (s *PGSessionStore) queryRowCtx(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx, ok := TxFromContext(ctx); ok {
		return tx.QueryRow(ctx, sql, args...)
	}
	return s.pool.QueryRow(ctx, sql, args...)
}

// validateFingerprintShape enforces per-FingerprintMode invariants on the Session
// record. Mirrors mem_store.go:163-171 so both backends enforce identical rules.
func (s *PGSessionStore) validateFingerprintShape(sess *session.Session) error {
	if _, ok := s.protocol.Fingerprint().(session.FingerprintJTIRef); ok {
		if sess.JTI == "" {
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"session: FingerprintJTIRef requires non-empty JTI")
		}
	}
	return nil
}

// Create persists a new session row. Nil session, empty Session.ID, or empty
// Session.SubjectID return ErrValidationFailed. FingerprintMode shape violations
// (e.g. empty JTI under FingerprintJTIRef) return ErrValidationFailed.
// Duplicate Session.ID returns ErrSessionConflict (SQLSTATE 23505).
func (s *PGSessionStore) Create(ctx context.Context, sess *session.Session) error {
	if sess == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session: Create requires non-nil Session")
	}
	if sess.ID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session: Session.ID required")
	}
	if sess.SubjectID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session: Session.SubjectID required")
	}
	// PG store requires SubjectID to be a valid UUID string because sessions.subject_id
	// is a UUID FK to users.id. Backends may enforce additional shape constraints
	// beyond what the generic session.Store contract mandates.
	if _, err := uuid.Parse(sess.SubjectID); err != nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"PG session store requires UUID-formatted SubjectID",
			errcode.WithDetails(slog.String("subjectID", sess.SubjectID)))
	}
	if err := s.validateFingerprintShape(sess); err != nil {
		return err
	}

	_, err := s.execCtx(
		ctx, insertSessionSQL,
		sess.ID,
		sess.SubjectID,
		sess.JTI,
		sess.AuthzEpochAtIssue,
		sess.CreatedAt.UTC(),
		sess.ExpiresAt.UTC(),
	)
	if err != nil {
		if IsUniqueViolation(err) {
			return errcode.New(errcode.KindConflict, errcode.ErrSessionConflict,
				"session: duplicate ID",
				errcode.WithDetails(slog.String("sessionID", sess.ID)))
		}
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "session store: create", err)
	}
	return nil
}

// Get fetches the session by ID. Returns ErrSessionNotFound if the ID is not
// present. Revoked and expired sessions are still returned — callers inspect
// Session.RevokedAt and Session.ExpiresAt to make policy decisions.
func (s *PGSessionStore) Get(ctx context.Context, id string) (*session.Session, error) {
	var sess session.Session
	err := s.queryRowCtx(ctx, selectSessionByIDSQL, id).Scan(
		&sess.ID,
		&sess.SubjectID,
		&sess.JTI,
		&sess.AuthzEpochAtIssue,
		&sess.CreatedAt,
		&sess.ExpiresAt,
		&sess.RevokedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound,
			"session: not found",
			errcode.WithDetails(slog.String("sessionID", id)))
	}
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "session store: get", err)
	}
	// Normalize timestamps to UTC for deterministic equality comparisons in
	// storetest cases that use FakeClock.Now() (always UTC).
	sess.CreatedAt = sess.CreatedAt.UTC()
	sess.ExpiresAt = sess.ExpiresAt.UTC()
	if sess.RevokedAt != nil {
		t := sess.RevokedAt.UTC()
		sess.RevokedAt = &t
	}
	return &sess, nil
}

// Revoke marks the session dead. Idempotent: already-revoked or missing IDs
// both return nil (防枚举 — must not leak existence). RevokedAt is set exactly
// once via the WHERE revoked_at IS NULL guard; subsequent Revoke calls are
// no-ops at the SQL level.
func (s *PGSessionStore) Revoke(ctx context.Context, id string) error {
	now := s.clock.Now().UTC()
	_, err := s.execCtx(ctx, revokeSessionByIDSQL, now, id)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "session store: revoke", err)
	}
	// RowsAffected 0 means either not found or already revoked — both are
	// silent no-ops per Store contract (防枚举).
	return nil
}

// ─── lifecycle.ManagedResource ───────────────────────────────────────────────
//
// PGSessionStore is stateless with respect to lifecycle: it does not own its
// connection pool (that is *postgres.Pool's responsibility), has no background
// worker, and has no independent readiness probe. The three methods below are
// no-ops satisfying the A54 archtest contract
// (TestAdaptersExportedTypesManagedResourceOrOptOut). Pool health is surfaced
// by the *Pool ManagedResource that the composition root registers separately.

// Checkers returns an empty map: PGSessionStore has no independent readiness
// surface — the underlying pool's _ready probe (registered by *Pool) covers it.
func (s *PGSessionStore) Checkers() map[string]func(context.Context) error {
	return nil
}

// Worker returns nil: PGSessionStore has no background goroutine.
func (s *PGSessionStore) Worker() worker.Worker { return nil }

// Close is a no-op: PGSessionStore does not own its pool.
// Pool teardown is handled by the *Pool ManagedResource registered at the
// composition root; closing the store a second time would double-close.
func (s *PGSessionStore) Close(_ context.Context) error { return nil }

// ─── session.Store: RevokeForSubject ─────────────────────────────────────────

// RevokeForSubject marks every active session for subjectID dead. Empty
// subjectID returns ErrValidationFailed. Unknown CredentialEvent values return
// ErrValidationFailed. Returns nil even when the subject has no sessions.
// Pre-revoked sessions preserve their original RevokedAt timestamp (append-only
// revoke semantics — ADR-Session D3). The event argument is informational under
// the current protocol; the UPDATE covers all active sessions regardless of event.
func (s *PGSessionStore) RevokeForSubject(ctx context.Context, subjectID string, event session.CredentialEvent) error {
	if subjectID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session: RevokeForSubject requires non-empty subjectID")
	}
	if !session.ValidateCredentialEvent(event) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session: RevokeForSubject received unknown CredentialEvent")
	}

	now := s.clock.Now().UTC()
	_, err := s.execCtx(ctx, revokeSessionsBySubjectSQL, now, subjectID)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "session store: revoke for subject", err)
	}
	return nil
}
