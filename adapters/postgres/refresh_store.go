package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time assertion: PGRefreshStore implements refresh.Store.
var _ refresh.Store = (*PGRefreshStore)(nil)

// gcBatchSize is the number of rows deleted per GC batch iteration.
const gcBatchSize = 1000

// Append-only SQL statements.
//
// Design: Issue and Rotate only INSERT rows; rotated_at and revoked_at are
// one-way flips; verifier_hash is never updated. Reuse detection cascades
// revoke_session for the entire session_id lineage.
const (
	insertRowSQL = `
INSERT INTO refresh_tokens (id, parent_id, session_id, subject_id, selector, verifier_hash, created_at, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`

	selectBySelectorSQL = `
SELECT id, session_id, subject_id, verifier_hash, created_at, expires_at, rotated_at, revoked_at
FROM refresh_tokens
WHERE selector = $1
ORDER BY created_at DESC
LIMIT 1`

	lockSessionSQL = `
SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`

	markRotatedSQL = `
UPDATE refresh_tokens
SET rotated_at = $1
WHERE id = $2
  AND rotated_at IS NULL`

	revokeSessionSQL = `
UPDATE refresh_tokens
SET revoked_at = $1
WHERE session_id = $2
  AND revoked_at IS NULL`

	revokeUserSQL = `
UPDATE refresh_tokens
SET revoked_at = $1
WHERE subject_id = $2
  AND revoked_at IS NULL`

	gcBatchSQL = `
DELETE FROM refresh_tokens
WHERE id IN (
    SELECT id FROM refresh_tokens
    WHERE expires_at < $1
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)`
)

// PGRefreshStore implements refresh.Store over PostgreSQL using pgx.
//
// All time values come from the injected clock (never PG's now()), so the
// FakeClock in storetest drives deterministic behaviour.
//
// Consistency: L1 LocalTx — Rotate is atomic within a single transaction;
// Issue and revoke paths are single-statement writes.
//
// ref: ory/fosite token/hmac/hmacsha.go (base64url nopad + constant-time compare)
// ref: ory/hydra persistence/sql/persister_oauth2.go (CAS chain + reuse cascade)
type PGRefreshStore struct {
	pool   *pgxpool.Pool
	policy refresh.Policy
	clock  refresh.Clock
	rand   io.Reader
}

type refreshRow struct {
	id           uuid.UUID
	sessionID    string
	subjectID    string
	verifierHash []byte
	createdAt    time.Time
	expiresAt    time.Time
	rotatedAt    *time.Time
	revokedAt    *time.Time
}

func (r refreshRow) toToken() *refresh.Token {
	return &refresh.Token{
		ID:        r.id,
		SessionID: r.sessionID,
		SubjectID: r.subjectID,
		CreatedAt: r.createdAt,
		ExpiresAt: r.expiresAt,
	}
}

// NewRefreshStore constructs a PGRefreshStore.
func NewRefreshStore(pool *pgxpool.Pool, policy refresh.Policy, clock refresh.Clock, randReader io.Reader) *PGRefreshStore {
	if pool == nil {
		panic("postgres.NewRefreshStore: pool must not be nil")
	}
	if clock == nil {
		panic("postgres.NewRefreshStore: clock must not be nil")
	}
	if policy.MaxAge <= 0 {
		panic("postgres.NewRefreshStore: policy.MaxAge must be positive")
	}
	if policy.ReuseInterval < 0 {
		panic("postgres.NewRefreshStore: policy.ReuseInterval must not be negative")
	}
	if randReader == nil {
		randReader = rand.Reader
	}
	return &PGRefreshStore{
		pool:   pool,
		policy: policy,
		clock:  clock,
		rand:   randReader,
	}
}

// execCtx executes a SQL statement against the ambient transaction in ctx when
// one is present (F1: join caller's tx so refresh revokes are atomic with the
// session revoke). Falls back to the pool when no tx is in context.
func (s *PGRefreshStore) execCtx(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if tx, ok := TxFromContext(ctx); ok {
		return tx.Exec(ctx, sql, args...)
	}
	return s.pool.Exec(ctx, sql, args...)
}

// generatePair delegates to the shared refresh.GeneratePair helper (F10).
func (s *PGRefreshStore) generatePair() (selector []byte, verifier []byte, err error) {
	sel, ver, err := refresh.GeneratePair(s.rand)
	if err != nil {
		return nil, nil, errcode.Wrap(ErrAdapterPGQuery, "refresh store: rng", err)
	}
	return sel, ver, nil
}

// Issue creates a new refresh chain root. L1 LocalTx.
func (s *PGRefreshStore) Issue(ctx context.Context, sessionID, subjectID string) (string, *refresh.Token, error) {
	sel, ver, err := s.generatePair()
	if err != nil {
		return "", nil, err
	}
	now := s.clock.Now()
	expiresAt := now.Add(s.policy.MaxAge)
	id := uuid.New()
	verHash := sha256.Sum256(ver)

	if _, err := s.execCtx(ctx, insertRowSQL,
		id, uuid.NullUUID{}, sessionID, subjectID, sel, verHash[:], now, expiresAt,
	); err != nil {
		return "", nil, errcode.Wrap(ErrAdapterPGQuery, "refresh store: issue", err)
	}

	return refresh.EncodeOpaque(sel, ver), &refresh.Token{
		ID:        id,
		SessionID: sessionID,
		SubjectID: subjectID,
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}, nil
}

// Peek validates the presented wire token without advancing the lineage.
func (s *PGRefreshStore) Peek(ctx context.Context, presented string) (*refresh.Token, error) {
	sel, ver, ok := refresh.ParseOpaque(presented)
	if !ok {
		return nil, rejectWithReason("malformed", "")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterPGConnect, "refresh store: peek begin", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	row, err := s.validatePresentedInTx(ctx, tx, sel, ver)
	if err != nil && !errors.Is(err, refresh.ErrRejected) {
		return nil, err
	}
	if cErr := tx.Commit(ctx); cErr != nil {
		return nil, errcode.Wrap(ErrAdapterPGConnect, "refresh store: peek commit", cErr)
	}
	committed = true
	if err != nil {
		return nil, err
	}
	return row.toToken(), nil
}

// Rotate advances the chain. See Store.Rotate contract for branch behaviour.
//
// Non-happy paths funnel through rejectWithReason and return refresh.ErrRejected
// so callers cannot enumerate cause via error shape or timing. The transaction
// is committed uniformly on ErrRejected so that commit-vs-rollback latency is
// not an oracle on whether a cascade-revoke happened.
func (s *PGRefreshStore) Rotate(ctx context.Context, presented string) (string, *refresh.Token, error) {
	sel, ver, ok := refresh.ParseOpaque(presented)
	if !ok {
		return "", nil, rejectWithReason("malformed", "")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", nil, errcode.Wrap(ErrAdapterPGConnect, "refresh store: rotate begin", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	wire, tok, err := s.rotateInTx(ctx, tx, sel, ver)
	if err != nil && !errors.Is(err, refresh.ErrRejected) {
		return "", nil, err
	}

	// Commit unconditionally on success and on ErrRejected so commit latency
	// does not distinguish happy paths from rejections. For read-only reject
	// branches the commit is a no-op; for reuse_detected it persists the
	// cascade revoke.
	if cErr := tx.Commit(ctx); cErr != nil {
		return "", nil, errcode.Wrap(ErrAdapterPGConnect, "refresh store: rotate commit", cErr)
	}
	committed = true
	return wire, tok, err
}

// rotateInTx orchestrates the Rotate branches within an open transaction.
func (s *PGRefreshStore) rotateInTx(ctx context.Context, tx pgx.Tx, sel, ver []byte) (string, *refresh.Token, error) {
	row, err := s.validatePresentedInTx(ctx, tx, sel, ver)
	if err != nil {
		return "", nil, err
	}

	// Happy path or grace retry — INSERT a child whose parent_id points to
	// row.id (the current generation), then flip row.id.rotated_at iff this is
	// the first rotation.
	newSel, newVer, err := s.generatePair()
	if err != nil {
		return "", nil, err
	}
	now := s.clock.Now()
	newID := uuid.New()
	newHash := sha256.Sum256(newVer)
	newExpires := now.Add(s.policy.MaxAge)

	if _, err := tx.Exec(ctx, insertRowSQL,
		newID, uuid.NullUUID{UUID: row.id, Valid: true},
		row.sessionID, row.subjectID, newSel, newHash[:], now, newExpires,
	); err != nil {
		return "", nil, errcode.Wrap(ErrAdapterPGQuery, "refresh store: rotate insert child", err)
	}

	if row.rotatedAt == nil {
		if _, err := tx.Exec(ctx, markRotatedSQL, now, row.id); err != nil {
			return "", nil, errcode.Wrap(ErrAdapterPGQuery, "refresh store: mark parent rotated", err)
		}
	}

	return refresh.EncodeOpaque(newSel, newVer), &refresh.Token{
		ID:        newID,
		SessionID: row.sessionID,
		SubjectID: row.subjectID,
		CreatedAt: now,
		ExpiresAt: newExpires,
	}, nil
}

func (s *PGRefreshStore) validatePresentedInTx(ctx context.Context, tx pgx.Tx, sel, ver []byte) (refreshRow, error) {
	row, err := s.selectBySelectorInTx(ctx, tx, sel)
	if err != nil {
		return refreshRow{}, err
	}
	if err := s.lockSessionInTx(ctx, tx, row.sessionID); err != nil {
		return refreshRow{}, err
	}

	// Re-read after acquiring the per-session advisory lock. This closes the
	// READ COMMITTED race where a child rotation validates before a concurrent
	// reuse-detection transaction revokes the session, then inserts a new child
	// after the cascade has already run.
	row, err = s.selectBySelectorInTx(ctx, tx, sel)
	if err != nil {
		return refreshRow{}, err
	}
	return s.validateRow(ctx, tx, row, ver)
}

func (s *PGRefreshStore) selectBySelectorInTx(ctx context.Context, tx pgx.Tx, sel []byte) (refreshRow, error) {
	var row refreshRow
	err := tx.QueryRow(ctx, selectBySelectorSQL, sel).Scan(
		&row.id, &row.sessionID, &row.subjectID,
		&row.verifierHash, &row.createdAt, &row.expiresAt, &row.rotatedAt, &row.revokedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return refreshRow{}, rejectWithReason("selector_miss", "")
	}
	if err != nil {
		return refreshRow{}, errcode.Wrap(ErrAdapterPGQuery, "refresh store: token select", err)
	}
	return row, nil
}

func (s *PGRefreshStore) lockSessionInTx(ctx context.Context, tx pgx.Tx, sessionID string) error {
	if _, err := tx.Exec(ctx, lockSessionSQL, sessionID); err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, "refresh store: session lock", err)
	}
	return nil
}

func (s *PGRefreshStore) validateRow(ctx context.Context, tx pgx.Tx, row refreshRow, ver []byte) (refreshRow, error) {
	presentedHash := sha256.Sum256(ver)
	if subtle.ConstantTimeCompare(presentedHash[:], row.verifierHash) != 1 {
		return refreshRow{}, rejectWithReason("verifier_miss", row.sessionID)
	}

	now := s.clock.Now()
	if row.revokedAt != nil {
		return refreshRow{}, rejectWithReason("revoked", row.sessionID)
	}
	if !row.expiresAt.After(now) {
		return refreshRow{}, rejectWithReason("expired", row.sessionID)
	}

	if row.rotatedAt != nil && now.Sub(*row.rotatedAt) > s.policy.ReuseInterval {
		if _, execErr := tx.Exec(ctx, revokeSessionSQL, now, row.sessionID); execErr != nil {
			return refreshRow{}, errcode.Wrap(ErrAdapterPGQuery, "refresh store: reuse cascade", execErr)
		}
		slog.Error("refresh token reuse detected",
			slog.String("session_id", row.sessionID),
			slog.String("reason", "reuse_detected"),
		)
		return refreshRow{}, refresh.ErrRejected
	}

	return row, nil
}

// RevokeSession marks every row in the session_id lineage as revoked.
// Uses the ambient transaction from ctx when present (F1).
func (s *PGRefreshStore) RevokeSession(ctx context.Context, sessionID string) error {
	now := s.clock.Now()
	if _, err := s.execCtx(ctx, revokeSessionSQL, now, sessionID); err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, "refresh store: revoke session", err)
	}
	return nil
}

// RevokeUser marks every row owned by subjectID as revoked.
// Uses the ambient transaction from ctx when present (F1).
func (s *PGRefreshStore) RevokeUser(ctx context.Context, subjectID string) error {
	now := s.clock.Now()
	if _, err := s.execCtx(ctx, revokeUserSQL, now, subjectID); err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, "refresh store: revoke user", err)
	}
	return nil
}

// GC removes rows whose expires_at < olderThan in batches of gcBatchSize.
// Uses the ambient transaction from ctx when present (F1).
func (s *PGRefreshStore) GC(ctx context.Context, olderThan time.Time) (int, error) {
	total := 0
	for {
		ct, err := s.execCtx(ctx, gcBatchSQL, olderThan, gcBatchSize)
		if err != nil {
			return total, errcode.Wrap(ErrAdapterPGQuery, "refresh store: gc batch", err)
		}
		deleted := int(ct.RowsAffected())
		total += deleted
		if deleted < gcBatchSize {
			break
		}
	}
	return total, nil
}

// rejectWithReason emits a Warn slog line and returns refresh.ErrRejected.
// Every non-happy Rotate branch funnels through this helper so error shape
// and log cadence stay uniform. session_id is empty for reasons observed
// before the DB is consulted (malformed, selector_miss).
func rejectWithReason(reason, sessionID string) error {
	if sessionID == "" {
		slog.Warn("refresh token rejected", slog.String("reason", reason))
	} else {
		slog.Warn("refresh token rejected",
			slog.String("reason", reason),
			slog.String("session_id", sessionID),
		)
	}
	return refresh.ErrRejected
}
