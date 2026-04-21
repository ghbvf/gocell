package postgres

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time assertion: PGRefreshStore implements refresh.Store.
var _ refresh.Store = (*PGRefreshStore)(nil)

// gcBatchSize is the number of rows deleted per GC batch iteration.
const gcBatchSize = 1000

// SQL constants for PGRefreshStore operations.
const issueSQL = `
INSERT INTO refresh_tokens (token, session_id, subject_id, created_at, last_used, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id`

const rotateActiveSQL = `
UPDATE refresh_tokens
SET token = $1,
    obsolete_token = token,
    last_used = $2
WHERE token = $3
  AND revoked_at IS NULL
  AND expires_at > $4
RETURNING id, token, obsolete_token, session_id, subject_id, created_at, last_used, expires_at`

const checkActiveStateSQL = `
SELECT revoked_at, expires_at
FROM refresh_tokens
WHERE token = $1
LIMIT 1`

const lookupByObsoleteSQL = `
SELECT id, token, obsolete_token, session_id, subject_id, created_at, last_used, expires_at, revoked_at
FROM refresh_tokens
WHERE obsolete_token = $1
  AND revoked_at IS NULL
LIMIT 1`

const checkObsoleteRevokedSQL = `
SELECT 1
FROM refresh_tokens
WHERE obsolete_token = $1
  AND revoked_at IS NOT NULL
LIMIT 1`

const revokeSessionSQL = `
UPDATE refresh_tokens
SET revoked_at = $1
WHERE session_id = $2
  AND revoked_at IS NULL`

const gcBatchSQL = `
DELETE FROM refresh_tokens
WHERE id IN (
    SELECT id FROM refresh_tokens
    WHERE expires_at < $1
    LIMIT $2
    FOR UPDATE SKIP LOCKED
)`

// PGRefreshStore implements refresh.Store over PostgreSQL using pgx.
//
// All time values are sourced from the injected clock (never PG's now()),
// so that the FakeClock in storetest can drive deterministic behaviour.
//
// Consistency: L1 LocalTx — Rotate is atomic within a single transaction;
// Issue and Revoke are single-statement writes.
//
// ref: dexidp/dex storage/sql/sql.go (pgx-based refresh token CAS pattern)
// ref: F2 contract C1-C7 from docs/plans/202604191515-auth-federated-whistle.md
type PGRefreshStore struct {
	pool   *pgxpool.Pool
	policy refresh.Policy
	clock  refresh.Clock
	rand   io.Reader
}

// NewRefreshStore constructs a PGRefreshStore.
//
// clock must not be nil. policy.MaxAge must be positive. If randReader is nil,
// crypto/rand.Reader is used.
func NewRefreshStore(pool *pgxpool.Pool, policy refresh.Policy, clock refresh.Clock, randReader io.Reader) *PGRefreshStore {
	if clock == nil {
		panic("postgres.NewRefreshStore: clock must not be nil")
	}
	if policy.MaxAge <= 0 {
		panic("postgres.NewRefreshStore: policy.MaxAge must be positive")
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

// generateTokenID produces a 43-character base64url token from 32 random bytes.
//
// 32 bytes → 256 bits of entropy; base64.RawURLEncoding gives ceil(32*4/3)=43 chars.
// ref: dexidp/dex server/refreshhandlers.go newRefreshToken()
func (s *PGRefreshStore) generateTokenID() (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(s.rand, buf); err != nil {
		return "", errcode.Wrap(ErrAdapterPGQuery, "refresh store: generate token id", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Issue creates a new refresh chain for (sessionID, subjectID).
//
// Consistency: L1 LocalTx — single INSERT, no outbox event.
func (s *PGRefreshStore) Issue(ctx context.Context, sessionID, subjectID string) (*refresh.Token, error) {
	tokenID, err := s.generateTokenID()
	if err != nil {
		return nil, err
	}

	now := s.clock.Now()
	expiresAt := now.Add(s.policy.MaxAge)

	var id int64
	err = s.pool.QueryRow(ctx, issueSQL, tokenID, sessionID, subjectID, now, now, expiresAt).Scan(&id)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterPGQuery, "refresh store: issue", err)
	}

	return &refresh.Token{
		ID:        tokenID,
		SessionID: sessionID,
		SubjectID: subjectID,
		CreatedAt: now,
		LastUsed:  now,
		ExpiresAt: expiresAt,
	}, nil
}

// Revoke marks all tokens in the session as revoked.
//
// Idempotent: 0 rows affected is not an error.
// Consistency: L1 LocalTx — single UPDATE within one statement.
func (s *PGRefreshStore) Revoke(ctx context.Context, sessionID string) error {
	now := s.clock.Now()
	_, err := s.pool.Exec(ctx, revokeSessionSQL, now, sessionID)
	if err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, "refresh store: revoke", err)
	}
	return nil
}

// GC removes tokens whose expires_at < olderThan in batches of gcBatchSize.
// Returns the total count of rows deleted.
//
// Consistency: L0 LocalOnly — best-effort cleanup, no transactional guarantee.
func (s *PGRefreshStore) GC(ctx context.Context, olderThan time.Time) (int, error) {
	total := 0
	for {
		ct, err := s.pool.Exec(ctx, gcBatchSQL, olderThan, gcBatchSize)
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

// Rotate advances the chain one generation using a single transaction.
//
// State machine branches (see refresh.Store interface for full contract):
//  1. CAS active — UPDATE the current active token; returns new token.
//  2. Active exists but revoked/expired — surface ErrTokenRevoked/ErrTokenExpired.
//  3. Obsolete grace retry — presented token is a previous-generation obsolete;
//     within ReuseInterval → return current token idempotently.
//  4. Obsolete reuse detection — grace window elapsed → cascade Revoke + ErrTokenReused.
//  5. Not found in either index → ErrTokenNotFound.
//
// Consistency: L1 LocalTx — all branches execute within one BEGIN/COMMIT block.
func (s *PGRefreshStore) Rotate(ctx context.Context, presentedToken string) (*refresh.Token, error) {
	now := s.clock.Now()

	newTokenID, err := s.generateTokenID()
	if err != nil {
		return nil, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterPGConnect, "refresh store: rotate begin", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()

	tok, err := s.rotateInTx(ctx, tx, presentedToken, newTokenID, now)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, errcode.Wrap(ErrAdapterPGConnect, "refresh store: rotate commit", err)
	}
	committed = true

	return tok, nil
}

// rotateInTx orchestrates the five Rotate branches within an open transaction.
func (s *PGRefreshStore) rotateInTx(ctx context.Context, tx pgx.Tx, presentedToken, newTokenID string, now time.Time) (*refresh.Token, error) {
	// Branch 1: CAS active token.
	tok, err := s.tryRotateActive(ctx, tx, presentedToken, newTokenID, now)
	if err == nil {
		return tok, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// CAS missed — token either revoked, expired, or is an obsolete token.
	// Branch 2: check if it exists as an active (but invalid) record.
	if stateErr := s.checkActiveState(ctx, tx, presentedToken); stateErr != nil {
		return nil, stateErr
	}

	// Not an active record — check obsolete branches.
	// Branch 3 & 4: check the obsolete token index.
	tok, err = s.tryObsolete(ctx, tx, presentedToken, now)
	if err == nil {
		return tok, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		// Branch 4b: check if it's an obsolete token on a revoked row.
		var dummy int
		scanErr := tx.QueryRow(ctx, checkObsoleteRevokedSQL, presentedToken).Scan(&dummy)
		if scanErr == nil {
			return nil, refresh.ErrTokenRevoked
		}
		// Branch 5: genuinely not found in any index.
		return nil, refresh.ErrTokenNotFound
	}
	return nil, err
}

// tryRotateActive attempts the CAS UPDATE for an active, valid token.
// Returns (token, nil) on success, (nil, pgx.ErrNoRows) when the UPDATE matched
// no row (token absent, revoked, or expired), or (nil, infraErr) on DB error.
func (s *PGRefreshStore) tryRotateActive(ctx context.Context, tx pgx.Tx, presentedToken, newTokenID string, now time.Time) (*refresh.Token, error) {
	row := tx.QueryRow(ctx, rotateActiveSQL, newTokenID, now, presentedToken, now)
	tok, err := scanTokenRow(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, pgx.ErrNoRows
		}
		return nil, errcode.Wrap(ErrAdapterPGQuery, "refresh store: rotate active", err)
	}
	return tok, nil
}

// checkActiveState inspects the record for presentedToken to decide whether
// it's revoked or expired. Returns ErrTokenRevoked, ErrTokenExpired, or nil
// (meaning: no active record found at all, should proceed to obsolete check).
// Returns pgx.ErrNoRows when the token does not exist in the active index.
func (s *PGRefreshStore) checkActiveState(ctx context.Context, tx pgx.Tx, presentedToken string) error {
	var revokedAt *time.Time
	var expiresAt time.Time // scan destination only; not compared (CAS already excluded valid tokens)
	err := tx.QueryRow(ctx, checkActiveStateSQL, presentedToken).Scan(&revokedAt, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// No active record — caller should check obsolete index.
		return nil
	}
	if err != nil {
		return errcode.Wrap(ErrAdapterPGQuery, "refresh store: check active state", err)
	}
	// Record exists but was filtered out by rotateActiveSQL's WHERE clause.
	if revokedAt != nil {
		return refresh.ErrTokenRevoked
	}
	// Not revoked but CAS excluded it — must be expired (expires_at <= now).
	return refresh.ErrTokenExpired
}

// tryObsolete handles Branches 3 and 4: presented token is the obsolete token
// of the current-generation record.
//
// Returns (token, nil) for grace retry, (nil, ErrTokenReused) for reuse attack,
// or (nil, pgx.ErrNoRows) when no active record holds this as obsolete_token.
func (s *PGRefreshStore) tryObsolete(ctx context.Context, tx pgx.Tx, presentedToken string, now time.Time) (*refresh.Token, error) {
	row := tx.QueryRow(ctx, lookupByObsoleteSQL, presentedToken)

	var (
		id            int64
		token         string
		obsoleteToken *string
		sessionID     string
		subjectID     string
		createdAt     time.Time
		lastUsed      time.Time
		expiresAt     time.Time
		revokedAt     *time.Time
	)
	err := row.Scan(&id, &token, &obsoleteToken, &sessionID, &subjectID, &createdAt, &lastUsed, &expiresAt, &revokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, pgx.ErrNoRows
	}
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterPGQuery, "refresh store: lookup by obsolete", err)
	}

	// Branch 4: grace window elapsed → cascade revoke + ErrTokenReused.
	elapsed := now.Sub(lastUsed)
	if elapsed > s.policy.ReuseInterval {
		if _, execErr := tx.Exec(ctx, revokeSessionSQL, now, sessionID); execErr != nil {
			return nil, errcode.Wrap(ErrAdapterPGQuery, "refresh store: cascade revoke on reuse", execErr)
		}
		return nil, refresh.ErrTokenReused
	}

	// Branch 3: grace retry — return the current active token. ObsoleteToken
	// is intentionally blank in the grace-retry response (only the goroutine
	// that performed the actual rotation receives ObsoleteToken). This matches
	// memstore.rotateObsolete behaviour.
	//
	// ref: memstore/store.go rotateObsolete — tok.ObsoleteToken = ""
	tok := &refresh.Token{
		ID:        token,
		SessionID: sessionID,
		SubjectID: subjectID,
		CreatedAt: createdAt,
		LastUsed:  lastUsed,
		ExpiresAt: expiresAt,
	}
	return tok, nil
}

// scanTokenRow reads the columns returned by rotateActiveSQL into a Token.
func scanTokenRow(row RowScanner) (*refresh.Token, error) {
	var (
		id            int64
		token         string
		obsoleteToken *string
		sessionID     string
		subjectID     string
		createdAt     time.Time
		lastUsed      time.Time
		expiresAt     time.Time
	)
	if err := row.Scan(&id, &token, &obsoleteToken, &sessionID, &subjectID, &createdAt, &lastUsed, &expiresAt); err != nil {
		return nil, err
	}
	tok := &refresh.Token{
		ID:        token,
		SessionID: sessionID,
		SubjectID: subjectID,
		CreatedAt: createdAt,
		LastUsed:  lastUsed,
		ExpiresAt: expiresAt,
	}
	if obsoleteToken != nil {
		tok.ObsoleteToken = *obsoleteToken
	}
	return tok, nil
}
