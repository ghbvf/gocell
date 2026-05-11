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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/ctxutil"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
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
//
// Columns idle_expires_at, first_used_at, used_times are added by migration 016
// (X12 + X14). idle_expires_at is written explicitly on every Issue and Rotate;
// Policy.MaxIdle is required (must be positive).
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
	// threshold. LEAST(expires_at, idle_expires_at) determines the effective
	// expiry deadline for each row.
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
// Transaction contract (B2-A-08): PGRefreshStore uses the ambient transaction
// for business operations. Multi-statement operations (Peek, Rotate) delegate
// to the injected TxRunner. When the caller already holds an ambient
// transaction, TxManager creates a savepoint instead of a new top-level
// transaction, so refresh operations are fully nesting-safe. The only explicit
// bypass is RevokeSessionDetached and reuse-detection cascade revoke, which
// commit independently as security/compensation responses.
//
// ref: ory/fosite token/hmac/hmacsha.go (base64url nopad + constant-time compare)
// ref: ory/hydra persistence/sql/persister_oauth2.go (CAS chain + reuse cascade)
type PGRefreshStore struct {
	db       pgExecutor
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
// SQL operations go through the typed executor, which joins the ambient
// transaction from context unless a method explicitly asks for a direct bypass.
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
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "postgres.NewRefreshStore: pool must not be nil")
	}
	if validation.IsNilInterface(txRunner) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "postgres.NewRefreshStore: txRunner must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "postgres.NewRefreshStore: clock must not be nil")
	}
	if err := policy.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "postgres.NewRefreshStore", err)
	}
	if validation.IsNilInterface(randReader) {
		randReader = rand.Reader
	}
	return &PGRefreshStore{
		db:       newPGExecutor(pool),
		txRunner: txRunner,
		policy:   policy,
		clock:    clk,
		rand:     randReader,
	}, nil
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

	if _, err := s.db.Exec(ctx, insertRowSQL,
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

// idleDeadline returns now + MaxIdle. Policy.MaxIdle is guaranteed positive by
// Validate(), so no zero-check is needed.
func (s *PGRefreshStore) idleDeadline(now time.Time) time.Time {
	return now.Add(s.policy.MaxIdle)
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
	var rejectErr error
	err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		var innerErr error
		// Peek is read-only by contract: pass mutate=false so the grace
		// counter (used_times) is not incremented. Cascade revoke on
		// reuse / grace_exhausted still fires (it bypasses the ambient tx
		// directly via the typed executor's explicit bypass — security
		// response must persist).
		row, innerErr = s.validatePresentedInTx(txCtx, sel, ver, false)
		if innerErr == nil {
			return nil
		}
		if errors.Is(innerErr, refresh.ErrRejected) {
			// Capture reject through an outer variable so RunInTx commits the
			// transaction. This persists the cascade-revoke SQL (reuse_detected /
			// grace_exhausted) and keeps commit/rollback latency uniform across
			// branches (B2-A-09 timing oracle defense).
			rejectErr = innerErr
			return nil
		}
		return innerErr
	})
	if err != nil {
		return nil, err
	}
	if rejectErr != nil {
		return nil, rejectErr
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
	var rejectErr error
	err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		var innerErr error
		wire, tok, innerErr = s.rotateInTx(txCtx, sel, ver)
		if innerErr == nil {
			return nil
		}
		if errors.Is(innerErr, refresh.ErrRejected) {
			// Capture reject through an outer variable so RunInTx commits the
			// transaction. This persists the cascade-revoke SQL on reuse_detected /
			// grace_exhausted, and keeps commit latency uniform across branches
			// (B2-A-09 timing oracle defense).
			rejectErr = innerErr
			return nil
		}
		return innerErr
	})
	if err != nil {
		return "", nil, err
	}
	if rejectErr != nil {
		return "", nil, rejectErr
	}
	return wire, tok, nil
}

// rotateInTx orchestrates the Rotate branches within a transaction context.
func (s *PGRefreshStore) rotateInTx(ctx context.Context, sel, ver []byte) (string, *refresh.Token, error) {
	// Rotate is the mutating path: pass mutate=true so the grace counter
	// is incremented when the parent has already been rotated and is being
	// re-presented within the grace window.
	row, err := s.validatePresentedInTx(ctx, sel, ver, true)
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

	if _, err := s.db.Exec(ctx, insertRowSQL,
		newID, uuid.NullUUID{UUID: row.id, Valid: true},
		row.sessionID, row.subjectID, newSel, newHash[:], now, newExpires, newIdleExpires,
	); err != nil {
		return "", nil, errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: rotate insert child", err)
	}

	if row.rotatedAt == nil {
		if _, err := s.db.Exec(ctx, markRotatedSQL, now, row.id); err != nil {
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

// validatePresentedInTx validates the presented (selector, verifier) within
// the ambient transaction. mutate=true is the Rotate path (increments
// used_times in the grace branch); mutate=false is the Peek path (read-only
// by contract — Peek MUST NOT consume the grace budget). Cascade revoke on
// reuse / grace-exhausted bypasses the ambient tx in both paths because it
// is a security response that must persist regardless of caller rollback.
func (s *PGRefreshStore) validatePresentedInTx(ctx context.Context, sel, ver []byte, mutate bool) (refreshRow, error) {
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
	return s.validateRow(ctx, row, ver, mutate)
}

func (s *PGRefreshStore) selectBySelectorInTx(ctx context.Context, sel []byte) (refreshRow, error) {
	var row refreshRow
	err := s.db.QueryRow(ctx, selectBySelectorSQL, sel).Scan(
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
	if _, err := s.db.Exec(ctx, lockSessionSQL, sessionID); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: session lock", err)
	}
	return nil
}

func (s *PGRefreshStore) validateRow(ctx context.Context, row refreshRow, ver []byte, mutate bool) (refreshRow, error) {
	if err := s.checkBasicValidity(row, ver); err != nil {
		return refreshRow{}, err
	}

	if row.rotatedAt == nil {
		return row, nil
	}
	if err := s.handleRotatedRow(ctx, row, mutate); err != nil {
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
	// X12: idle-expiry check. Policy.MaxIdle is required (must be positive),
	// and idle_expires_at is written explicitly on every Issue/Rotate.
	if !row.idleExpiresAt.After(now) {
		return rejectWithReason("idle_expired", row.sessionID)
	}
	return nil
}

// handleRotatedRow runs grace-cap, reuse-window, and used_times-increment
// branches for rows whose parent has already been rotated once. Caller has
// already verified the row is otherwise valid.
//
// mutate=false (Peek path): grace cap and reuse-window cascade revoke still
// fire (security response must persist on any presentation, even read-only
// preflight), but the in-grace used_times increment is skipped — Peek is
// strictly read-only by Store contract and must not consume grace budget.
//
// mutate=true (Rotate path): in-grace re-presentation increments used_times
// via markGraceUsedSQL so that the counter approaches GraceMaxReuses.
func (s *PGRefreshStore) handleRotatedRow(ctx context.Context, row refreshRow, mutate bool) error {
	now := s.clock.Now()

	// X14: grace counter cap. If this parent has already been re-presented
	// GraceMaxReuses times, treat this as a reuse attack regardless of ReuseInterval.
	// Policy.GraceMaxReuses is required (must be positive) — no zero-check needed.
	if row.usedTimes >= s.policy.GraceMaxReuses {
		slog.Error("refresh token grace counter exhausted",
			slog.String("session_id", row.sessionID),
			slog.String("subject_id", row.subjectID),
			slog.String("reason", "reuse_detected"),
			slog.Int("used_times", row.usedTimes),
		)
		if err := s.revokeSessionDetachedAt(ctx, row.sessionID, now); err != nil {
			return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: grace exhausted cascade", err)
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
			slog.String("subject_id", row.subjectID),
			slog.String("reason", "reuse_detected"),
		)
		if err := s.revokeSessionDetachedAt(ctx, row.sessionID, now); err != nil {
			return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: reuse cascade", err)
		}
		return rejectWithReason("reuse_detected", row.sessionID)
	}

	// Within grace window. Only Rotate consumes the grace budget; Peek is
	// read-only by Store contract and must not advance used_times even when
	// the parent is in the grace window (otherwise a single sessionrefresh
	// request — Peek + Rotate — would consume two slots and exhaust grace
	// twice as fast as the policy intends).
	if !mutate {
		return nil
	}
	if _, execErr := s.db.Exec(ctx, markGraceUsedSQL, now, row.id); execErr != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: mark grace used", execErr)
	}
	return nil
}

// RevokeSession marks every row in the session_id lineage as revoked.
// Uses the ambient transaction from ctx when present (F1).
func (s *PGRefreshStore) RevokeSession(ctx context.Context, sessionID string) error {
	now := s.clock.Now()
	if _, err := s.db.Exec(ctx, revokeSessionSQL, now, sessionID); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: revoke session", err)
	}
	return nil
}

// RevokeSessionDetached marks every row in the session_id lineage as revoked,
// bypassing any ambient transaction and caller cancellation. It is used only
// for security cascade/compensation paths that must commit independently of
// the surrounding business transaction.
func (s *PGRefreshStore) RevokeSessionDetached(ctx context.Context, sessionID string) error {
	if err := s.revokeSessionDetachedAt(ctx, sessionID, s.clock.Now()); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: revoke session detached", err)
	}
	return nil
}

func (s *PGRefreshStore) revokeSessionDetachedAt(ctx context.Context, sessionID string, revokedAt time.Time) error {
	// Detach from the caller's cancellation context: a security/compensation
	// revoke MUST persist even when the HTTP request is canceled or times out.
	// The detached context gets a bounded 5-second deadline so the write does
	// not run indefinitely. The ambient tx is bypassed via the executor's direct
	// path so the revoke commits on its own connection regardless of the outer
	// RunInTx outcome.
	// ref: golang/go context.WithoutCancel; hashicorp/vault token_store.go quitContext
	// ref: ADR docs/architecture/202605051800-adr-refresh-store-ambient-tx-and-idle-grace.md
	cascadeCtx, cancelCascade := ctxutil.WithDetachedTimeout(ctx, refresh.CascadeRevokeTimeout)
	defer cancelCascade()
	_, err := s.db.ExecDirect(cascadeCtx, revokeSessionSQL, revokedAt, sessionID)
	return err
}

// RevokeUser marks every row owned by subjectID as revoked.
// Uses the ambient transaction from ctx when present (F1).
func (s *PGRefreshStore) RevokeUser(ctx context.Context, subjectID string) error {
	now := s.clock.Now()
	if _, err := s.db.Exec(ctx, revokeUserSQL, now, subjectID); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterPGQuery, "refresh store: revoke user", err)
	}
	return nil
}

// GC removes rows whose expires_at < olderThan in batches of gcBatchSize.
// Uses the ambient transaction from ctx when present (F1).
func (s *PGRefreshStore) GC(ctx context.Context, olderThan time.Time) (int, error) {
	total := 0
	for {
		ct, err := s.db.Exec(ctx, gcBatchSQL, olderThan, gcBatchSize)
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
