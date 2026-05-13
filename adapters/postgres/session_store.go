package postgres

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

	// selectSessionByIDSQL projects only the columns ValidateView exposes
	// (ID, SubjectID, RevokedAt) — Store.Get is the validate path, and
	// GC-only metadata (jti, authz_epoch_at_issue, created_at, expires_at)
	// must not leak to validate callers. GC sweep / audit / metadata
	// round-trip tests query the full row via store-internal SQL.
	selectSessionByIDSQL = `
SELECT id, subject_id::text, revoked_at
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
// Transaction contract: PGSessionStore uses the typed executor for all SQL.
// The executor joins the ambient tx when present, enabling write atomicity and
// consistent reads within a business transaction.
//
// ref: dexidp/dex storage/sql/sql.go — session row model
// ref: ory/hydra persistence/sql/persister_oauth2.go — ambient-tx pattern
type PGSessionStore struct {
	db       pgExecutor
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
		db:       newPGExecutor(pool),
		txRunner: txRunner,
		protocol: protocol,
		clock:    clk,
	}, nil
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

	_, err := s.db.Exec(
		ctx, insertSessionSQL,
		sess.ID,
		sess.SubjectID,
		sess.JTI,
		sess.AuthzEpochAtIssue,
		sess.CreatedAt.UTC(),
		sess.ExpiresAt.UTC(),
	)
	if err != nil {
		return sessionCreateError(err, sess.ID, sess.SubjectID)
	}
	return nil
}

func sessionCreateError(err error, sessionID, subjectID string) error {
	if IsUniqueViolation(err) {
		return errcode.New(errcode.KindConflict, errcode.ErrSessionConflict,
			"session: duplicate ID",
			errcode.WithDetails(slog.String("sessionID", sessionID)))
	}
	if IsForeignKeyViolation(err) {
		return errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound,
			"session: subject user not found",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithDetails(slog.String("subjectID", subjectID)))
	}
	return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "session store: create", err)
}

// Get fetches the validate projection by ID. Returns ErrSessionNotFound when
// the ID is not present. Revoked sessions are still returned (caller checks
// RevokedAt). GC eligibility (expires_at) is intentionally not exposed —
// validate paths must not gate on it.
func (s *PGSessionStore) Get(ctx context.Context, id string) (*session.ValidateView, error) {
	var v session.ValidateView
	err := s.db.QueryRow(ctx, selectSessionByIDSQL, id).Scan(
		&v.ID,
		&v.SubjectID,
		&v.RevokedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound,
			"session: not found",
			errcode.WithCategory(errcode.CategoryDomain),
			errcode.WithDetails(slog.String("sessionID", id)))
	}
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "session store: get", err)
	}
	if v.RevokedAt != nil {
		t := v.RevokedAt.UTC()
		v.RevokedAt = &t
	}
	return &v, nil
}

// Revoke marks the session dead. Idempotent: already-revoked or missing IDs
// both return nil (防枚举 — must not leak existence). RevokedAt is set exactly
// once via the WHERE revoked_at IS NULL guard; subsequent Revoke calls are
// no-ops at the SQL level.
func (s *PGSessionStore) Revoke(ctx context.Context, id string) error {
	now := s.clock.Now().UTC()
	_, err := s.db.Exec(ctx, revokeSessionByIDSQL, now, id)
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
	if _, err := uuid.Parse(subjectID); err != nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"PG session store requires UUID-formatted subjectID",
			errcode.WithDetails(slog.String("subjectID", subjectID)))
	}
	if !session.ValidateCredentialEvent(event) {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session: RevokeForSubject received unknown CredentialEvent")
	}

	now := s.clock.Now().UTC()
	_, err := s.db.Exec(ctx, revokeSessionsBySubjectSQL, now, subjectID)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "session store: revoke for subject", err)
	}
	return nil
}
