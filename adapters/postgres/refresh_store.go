package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

// Compile-time assertion: PGRefreshStore implements refresh.Store.
var _ refresh.Store = (*PGRefreshStore)(nil)

// gcBatchSize is the number of rows deleted per GC batch iteration.
const gcBatchSize = 1000

// idleFarFuture is used as idle_expires_at when Policy.MaxIdle is zero (pre-016
// migration stores that have not been configured with an idle window). Setting
// far-future prevents pre-016 rows from being swept by idle-expiry GC while still
// allowing the column to carry a valid TIMESTAMPTZ NOT NULL value.
const idleFarFuture = 10 * 365 * 24 * time.Hour

// Append-only SQL statements.
//
// Design: Issue and Rotate only INSERT rows; rotated_at and revoked_at are
// one-way flips; verifier_hash is never updated. Reuse detection cascades
// revoke_session for the entire session_id lineage.
//
// Columns idle_expires_at, first_used_at, used_times are added by migration 016
// (X12 + X14). Pre-016 rows have idle_expires_at defaulted to created_at + 30d.
const (
	insertRowSQL = `
INSERT INTO refresh_tokens (id, parent_id, session_id, subject_id, selector, verifier_hash, created_at, expires_at, idle_expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`

	selectBySelectorSQL = `
SELECT id, session_id, subject_id, verifier_hash, created_at, expires_at, rotated_at, revoked_at, idle_expires_at, first_used_at, used_times
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

	// markGraceUsedSQL sets first_used_at on first grace re-use and increments
	// used_times on every subsequent re-presentation within the grace window.
	markGraceUsedSQL = `
UPDATE refresh_tokens
SET first_used_at = COALESCE(first_used_at, $1),
    used_times    = used_times + 1
WHERE id = $2`

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

	// gcBatchSQL deletes expired rows: a row is eligible when either its
	// absolute expires_at or its idle_expires_at has passed the olderThan
	// threshold. idle_expires_at defaults to now()+30d for pre-016 rows so
	// they are only swept by expires_at.
	gcBatchSQL = `
DELETE FROM refresh_tokens
WHERE id IN (
    SELECT id FROM refresh_tokens
    WHERE LEAST(expires_at, idle_expires_at) < $1
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)`
)

// PGRefreshStore implements refresh.Store over PostgreSQL using pgx.
//
// All time values come from the injected clock (never PG's now()), so the
// FakeClock in storetest drives deterministic behavior.
//
// Consistency: L1 LocalTx — Rotate is atomic within a single transaction;
// Issue and revoke paths are single-statement writes.
//
// Transaction contract (B2-A-08): PGRefreshStore never acquires its own
// transactions. All multi-statement operations (Peek, Rotate) delegate to the
// injected TxRunner. When the caller already holds an ambient transaction,
// TxManager creates a savepoint instead of a new top-level transaction, so
// refresh operations are fully nesting-safe.
//
// ref: ory/fosite token/hmac/hmacsha.go (base64url nopad + constant-time compare)
// ref: ory/hydra persistence/sql/persister_oauth2.go (CAS chain + reuse cascade)
type PGRefreshStore struct {
	pool     *pgxpool.Pool
	txRunner persistence.TxRunner
	policy   refresh.Policy
	clock    clock.Clock
	rand     io.Reader
}

type refreshRow struct {
	id            uuid.UUID
	sessionID     string
	subjectID     string
	verifierHash  []byte
	createdAt     time.Time
	expiresAt     time.Time
	rotatedAt     *time.Time
	revokedAt     *time.Time
	idleExpiresAt time.Time
	firstUsedAt   *time.Time
	usedTimes     int
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
//
// Returns a non-nil error if pool, txRunner, or clock are nil, or if policy
// values are out of range.
//
// pool is retained for Health probes (ping path); all SQL operations go
// through execCtx/queryRowCtx which join the ambient transaction from context.
//
// txRunner is required: Peek and Rotate need a transaction boundary. Pass
// NewTxManager(pool) for standalone callers; ambient-tx callers (e.g. session
// login) provide a shared TxManager whose RunInTx will create a savepoint when
// a top-level transaction is already in context.
func NewRefreshStore(
	pool *pgxpool.Pool,
	txRunner persistence.TxRunner,
	policy refresh.Policy,
	clk clock.Clock,
	randReader io.Reader,
) (*PGRefreshStore, error) {
	if pool == nil {
		return nil, fmt.Errorf("postgres.NewRefreshStore: pool must not be nil")
	}
	if validation.IsNilInterface(txRunner) {
		return nil, fmt.Errorf("postgres.NewRefreshStore: txRunner must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return nil, fmt.Errorf("postgres.NewRefreshStore: clock must not be nil")
	}
	if policy.MaxAge <= 0 {
		return nil, fmt.Errorf("postgres.NewRefreshStore: policy.MaxAge must be positive")
	}
	if policy.ReuseInterval < 0 {
		return nil, fmt.Errorf("postgres.NewRefreshStore: policy.ReuseInterval must not be negative")
	}
	if validation.IsNilInterface(randReader) {
		randReader = rand.Reader
	}
	return &PGRefreshStore{
		pool:     pool,
		txRunner: txRunner,
		policy:   policy,
		clock:    clk,
		rand:     randReader,
	}, nil
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

// queryRowCtx queries a single row against the ambient transaction in ctx when
// one is present. Falls back to the pool when no tx is in context.
func (s *PGRefreshStore) queryRowCtx(ctx context.Context, sql string, args ...any) pgx.Row {
	if tx, ok := TxFromContext(ctx); ok {
		return tx.QueryRow(ctx, sql, args...)
	}
	return s.pool.QueryRow(ctx, sql, args...)
}

// generatePair delegates to the shared refresh.GeneratePair helper (F10).
func (s *PGRefreshStore) generatePair() (selector []byte, verifier []byte, err error) {
	sel, ver, err := refresh.GeneratePair(s.rand)
	if err != nil {
		return nil, nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: rng", err)
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
	idleExpiresAt := s.idleDeadline(now)
	id := uuid.New()
	verHash := sha256.Sum256(ver)

	if _, err := s.execCtx(ctx, insertRowSQL,
		id, uuid.NullUUID{}, sessionID, subjectID, sel, verHash[:], now, expiresAt, idleExpiresAt,
	); err != nil {
		return "", nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: issue", err)
	}

	return refresh.EncodeOpaque(sel, ver), &refresh.Token{
		ID:        id,
		SessionID: sessionID,
		SubjectID: subjectID,
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}, nil
}

// idleDeadline returns now + MaxIdle when MaxIdle is configured, or a far-future
// sentinel when MaxIdle is zero (pre-016 migration stores that have not yet
// been configured with an idle expiry window).
func (s *PGRefreshStore) idleDeadline(now time.Time) time.Time {
	if s.policy.MaxIdle > 0 {
		return now.Add(s.policy.MaxIdle)
	}
	return now.Add(idleFarFuture)
}

// Peek validates the presented wire token without advancing the lineage.
//
// Callers MUST call Peek (and Rotate) within an ambient transaction created by
// the injected TxRunner. PGRefreshStore no longer acquires its own transactions
// (B2-A-08 ambient-only model).
func (s *PGRefreshStore) Peek(ctx context.Context, presented string) (*refresh.Token, error) {
	sel, ver, ok := refresh.ParseOpaque(presented)
	if !ok {
		return nil, rejectWithReason("malformed", "")
	}

	var row refreshRow
	err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		var innerErr error
		row, innerErr = s.validatePresentedInTx(txCtx, sel, ver)
		if innerErr != nil && !errors.Is(innerErr, refresh.ErrRejected) {
			return innerErr
		}
		// ErrRejected is returned from RunInTx, not wrapped as inner error,
		// so the commit happens unconditionally (timing oracle defense).
		return innerErr
	})
	if err != nil && !errors.Is(err, refresh.ErrRejected) {
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return row.toToken(), nil
}

// Rotate advances the chain. See Store.Rotate contract for branch behavior.
//
// Non-happy paths funnel through rejectWithReason and return refresh.ErrRejected
// so callers cannot enumerate cause via error shape or timing. The transaction
// is committed uniformly on ErrRejected so that commit-vs-rollback latency is
// not an oracle on whether a cascade-revoke happened.
//
// Callers MUST call Rotate within an ambient transaction or with a standalone
// context. PGRefreshStore delegates transaction management to the injected
// TxRunner (B2-A-08 ambient-only model).
func (s *PGRefreshStore) Rotate(ctx context.Context, presented string) (string, *refresh.Token, error) {
	sel, ver, ok := refresh.ParseOpaque(presented)
	if !ok {
		return "", nil, rejectWithReason("malformed", "")
	}

	var wire string
	var tok *refresh.Token
	err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		var innerErr error
		wire, tok, innerErr = s.rotateInTx(txCtx, sel, ver)
		if innerErr != nil && !errors.Is(innerErr, refresh.ErrRejected) {
			return innerErr
		}
		// Commit unconditionally on success and on ErrRejected so commit latency
		// does not distinguish happy paths from rejections. For read-only reject
		// branches the commit is a no-op; for reuse_detected it persists the
		// cascade revoke.
		return innerErr
	})
	if err != nil && !errors.Is(err, refresh.ErrRejected) {
		return "", nil, err
	}
	return wire, tok, err
}

// rotateInTx orchestrates the Rotate branches within a transaction context.
func (s *PGRefreshStore) rotateInTx(ctx context.Context, sel, ver []byte) (string, *refresh.Token, error) {
	row, err := s.validatePresentedInTx(ctx, sel, ver)
	if err != nil {
		return "", nil, err
	}

	// Happy path or grace retry — INSERT a child whose parent_id points to
	// row.id (the current generation), then flip row.id.rotated_at iff this is
	// the first rotation. The child's idle_expires_at is reset to now+MaxIdle
	// (sliding window: each rotation extends the idle deadline).
	newSel, newVer, err := s.generatePair()
	if err != nil {
		return "", nil, err
	}
	now := s.clock.Now()
	newID := uuid.New()
	newHash := sha256.Sum256(newVer)
	newExpires := now.Add(s.policy.MaxAge)
	newIdleExpires := s.idleDeadline(now)

	if _, err := s.execCtx(ctx, insertRowSQL,
		newID, uuid.NullUUID{UUID: row.id, Valid: true},
		row.sessionID, row.subjectID, newSel, newHash[:], now, newExpires, newIdleExpires,
	); err != nil {
		return "", nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: rotate insert child", err)
	}

	if row.rotatedAt == nil {
		if _, err := s.execCtx(ctx, markRotatedSQL, now, row.id); err != nil {
			return "", nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: mark parent rotated", err)
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

func (s *PGRefreshStore) validatePresentedInTx(ctx context.Context, sel, ver []byte) (refreshRow, error) {
	row, err := s.selectBySelectorInTx(ctx, sel)
	if err != nil {
		return refreshRow{}, err
	}
	if err := s.lockSessionInTx(ctx, row.sessionID); err != nil {
		return refreshRow{}, err
	}

	// Re-read after acquiring the per-session advisory lock. This closes the
	// READ COMMITTED race where a child rotation validates before a concurrent
	// reuse-detection transaction revokes the session, then inserts a new child
	// after the cascade has already run.
	row, err = s.selectBySelectorInTx(ctx, sel)
	if err != nil {
		return refreshRow{}, err
	}
	return s.validateRow(ctx, row, ver)
}

func (s *PGRefreshStore) selectBySelectorInTx(ctx context.Context, sel []byte) (refreshRow, error) {
	var row refreshRow
	err := s.queryRowCtx(ctx, selectBySelectorSQL, sel).Scan(
		&row.id, &row.sessionID, &row.subjectID,
		&row.verifierHash, &row.createdAt, &row.expiresAt, &row.rotatedAt, &row.revokedAt,
		&row.idleExpiresAt, &row.firstUsedAt, &row.usedTimes,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return refreshRow{}, rejectWithReason("selector_miss", "")
	}
	if err != nil {
		return refreshRow{}, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: token select", err)
	}
	return row, nil
}

func (s *PGRefreshStore) lockSessionInTx(ctx context.Context, sessionID string) error {
	if _, err := s.execCtx(ctx, lockSessionSQL, sessionID); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: session lock", err)
	}
	return nil
}

func (s *PGRefreshStore) validateRow(ctx context.Context, row refreshRow, ver []byte) (refreshRow, error) {
	if err := s.checkBasicValidity(row, ver); err != nil {
		return refreshRow{}, err
	}

	if row.rotatedAt == nil {
		return row, nil
	}
	if err := s.handleRotatedRow(ctx, row); err != nil {
		return refreshRow{}, err
	}
	return row, nil
}

// checkBasicValidity verifies hash, revoke flag, hard expiry, and idle expiry.
// Returns refresh.ErrRejected (via rejectWithReason) on any failure.
func (s *PGRefreshStore) checkBasicValidity(row refreshRow, ver []byte) error {
	presentedHash := sha256.Sum256(ver)
	if subtle.ConstantTimeCompare(presentedHash[:], row.verifierHash) != 1 {
		return rejectWithReason("verifier_miss", row.sessionID)
	}
	now := s.clock.Now()
	if row.revokedAt != nil {
		return rejectWithReason("revoked", row.sessionID)
	}
	if !row.expiresAt.After(now) {
		return rejectWithReason("expired", row.sessionID)
	}
	// X12: idle-expiry check. idleExpiresAt is zero for pre-016 rows in DBs
	// without the migration; zero time is before any real time so we guard
	// against that by only checking when idleExpiresAt is non-zero.
	if !row.idleExpiresAt.IsZero() && s.policy.MaxIdle > 0 && !row.idleExpiresAt.After(now) {
		return rejectWithReason("idle_expired", row.sessionID)
	}
	return nil
}

// handleRotatedRow runs grace-cap, reuse-window, and used_times-increment
// branches for rows whose parent has already been rotated once. Caller has
// already verified the row is otherwise valid.
func (s *PGRefreshStore) handleRotatedRow(ctx context.Context, row refreshRow) error {
	now := s.clock.Now()

	// X14: grace counter cap. If the grace cap is configured and this parent
	// has already been re-presented GraceMaxReuses times, treat this as a
	// reuse attack regardless of ReuseInterval.
	if s.policy.GraceMaxReuses > 0 && row.usedTimes >= s.policy.GraceMaxReuses {
		slog.Error("refresh token grace counter exhausted",
			slog.String("session_id", row.sessionID),
			slog.String("reason", "reuse_detected"),
			slog.Int("used_times", row.usedTimes),
		)
		if _, execErr := s.execCtx(ctx, revokeSessionSQL, now, row.sessionID); execErr != nil {
			return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: grace exhausted cascade", execErr)
		}
		return rejectWithReason("reuse_detected", row.sessionID)
	}

	if now.Sub(*row.rotatedAt) > s.policy.ReuseInterval {
		// Reuse detected: log as security event BEFORE the cascade SQL so that
		// the security-observable log entry is not delayed by DB latency.
		// The cascade revoke path is slower than other reject branches due to
		// the UPDATE SQL; we accept this timing difference (B2-A-09: string
		// processing and log formatting are uniform across all branches; the
		// DB write is the only unavoidable timing oracle).
		slog.Error("refresh token reuse detected",
			slog.String("session_id", row.sessionID),
			slog.String("reason", "reuse_detected"),
		)
		if _, execErr := s.execCtx(ctx, revokeSessionSQL, now, row.sessionID); execErr != nil {
			return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: reuse cascade", execErr)
		}
		return rejectWithReason("reuse_detected", row.sessionID)
	}

	// Within grace window: increment used_times so the counter approaches GraceMaxReuses.
	if _, execErr := s.execCtx(ctx, markGraceUsedSQL, now, row.id); execErr != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: mark grace used", execErr)
	}
	return nil
}

// RevokeSession marks every row in the session_id lineage as revoked.
// Uses the ambient transaction from ctx when present (F1).
func (s *PGRefreshStore) RevokeSession(ctx context.Context, sessionID string) error {
	now := s.clock.Now()
	if _, err := s.execCtx(ctx, revokeSessionSQL, now, sessionID); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: revoke session", err)
	}
	return nil
}

// RevokeUser marks every row owned by subjectID as revoked.
// Uses the ambient transaction from ctx when present (F1).
func (s *PGRefreshStore) RevokeUser(ctx context.Context, subjectID string) error {
	now := s.clock.Now()
	if _, err := s.execCtx(ctx, revokeUserSQL, now, subjectID); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: revoke user", err)
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
			return total, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: gc batch", err)
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
// Every non-happy Rotate/Peek branch funnels through this helper so error
// shape and log cadence stay uniform (B2-A-09 timing/log uniformity).
//
// reuse_detected callers additionally emit a slog.Error BEFORE calling this
// helper (since the security event requires Error level), but the function
// execution path is uniform: every branch calls rejectWithReason once.
// session_id is empty for reasons observed before the DB is consulted
// (malformed, selector_miss).
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
