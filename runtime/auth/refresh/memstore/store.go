// Package memstore provides an in-memory implementation of refresh.Store.
//
// This implementation is the oracle for the storetest contract suite and serves
// as a unit-test double for services that depend on refresh.Store. It is NOT
// suitable for production use — state is not persisted across process restarts.
//
// ref: dexidp/dex storage/memory/memory.go (in-memory refresh store layout)
// ref: dexidp/dex server/refreshhandlers.go (AllowedToReuse + UpdateRefreshToken CAS)
package memstore

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"io"
	"sync"
	"time"

	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

// tokenRecord is the internal persisted row for a single refresh token chain.
// Each Issue creates one record; Rotate updates it in-place (CAS under mu).
//
// ref: dexidp/dex storage/storage.go RefreshToken (Token + ObsoleteToken + LastUsed)
type tokenRecord struct {
	id            string // current active token value (base64url, 43 chars)
	obsoleteToken string // previous generation's token value (empty on first issue)
	sessionID     string
	subjectID     string
	createdAt     time.Time
	lastUsed      time.Time
	expiresAt     time.Time
	revokedAt     time.Time // zero value means not revoked
}

// isRevoked returns true if the record has been revoked.
func (r *tokenRecord) isRevoked() bool {
	return !r.revokedAt.IsZero()
}

// toToken converts an internal record to the public Token type.
func (r *tokenRecord) toToken() *refresh.Token {
	return &refresh.Token{
		ID:            r.id,
		ObsoleteToken: r.obsoleteToken,
		SessionID:     r.sessionID,
		SubjectID:     r.subjectID,
		CreatedAt:     r.createdAt,
		LastUsed:      r.lastUsed,
		ExpiresAt:     r.expiresAt,
	}
}

// store is the in-memory implementation of refresh.Store.
//
// Concurrency: a single sync.Mutex guards all mutable state. This is intentional
// — memstore targets test doubles and the storetest oracle, not production
// throughput. A single lock gives CAS-correct semantics for T10 with zero
// complexity overhead.
//
// ref: dexidp/dex storage/memory/memory.go — uses a single sync.RWMutex for the
// entire in-memory store; we use Mutex (write-dominated workload in tests).
type store struct {
	mu     sync.Mutex
	policy refresh.Policy
	clock  refresh.Clock
	rand   io.Reader

	// byID maps the current active token value → record.
	// After Rotate, the old token value is removed and replaced by the new one.
	byID map[string]*tokenRecord

	// byObsolete maps an obsolete token value → the same record (so we can
	// look up the record when a client retries with the previous generation).
	// Entries are added on Rotate and removed on GC / Revoke.
	byObsolete map[string]*tokenRecord

	// bySession maps sessionID → all records belonging to that session.
	// A session can have multiple concurrent chains (multiple Issue calls).
	bySession map[string][]*tokenRecord
}

// New constructs an in-memory refresh.Store.
//
// If randReader is nil, crypto/rand.Reader is used. clock must not be nil; use
// storetest.NewFakeClock for deterministic tests or a realClock wrapper for
// production-equivalent tests.
//
// Intended for:
//   - unit tests of services that depend on refresh.Store
//   - the storetest contract suite oracle
func New(policy refresh.Policy, clock refresh.Clock, randReader io.Reader) refresh.Store {
	if clock == nil {
		panic("memstore.New: clock must not be nil; use storetest.NewFakeClock or a real clock wrapper")
	}
	if policy.MaxAge <= 0 {
		panic("memstore.New: policy.MaxAge must be positive (zero value of Policy is invalid)")
	}
	if randReader == nil {
		randReader = rand.Reader
	}
	return &store{
		policy:     policy,
		clock:      clock,
		rand:       randReader,
		byID:       make(map[string]*tokenRecord),
		byObsolete: make(map[string]*tokenRecord),
		bySession:  make(map[string][]*tokenRecord),
	}
}

// generateTokenID produces a 43-character base64url-encoded token from 32 random bytes.
//
// 32 bytes → 256 bits of entropy; base64.RawURLEncoding produces ceil(32*4/3) = 43 chars.
// The RawURL variant omits padding ('='), matching the dexidp/dex token format.
func (s *store) generateTokenID() (string, error) {
	buf := make([]byte, 32)
	if _, err := io.ReadFull(s.rand, buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Issue creates a new refresh chain for (sessionID, subjectID).
//
// Consistency: L1 LocalTx — atomic write under mu, no outbox event.
func (s *store) Issue(_ context.Context, sessionID, subjectID string) (*refresh.Token, error) {
	tokenID, err := s.generateTokenID()
	if err != nil {
		return nil, err
	}

	now := s.clock.Now()
	rec := &tokenRecord{
		id:        tokenID,
		sessionID: sessionID,
		subjectID: subjectID,
		createdAt: now,
		lastUsed:  now,
		expiresAt: now.Add(s.policy.MaxAge),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.byID[tokenID] = rec
	s.bySession[sessionID] = append(s.bySession[sessionID], rec)

	return rec.toToken(), nil
}

// Rotate advances the chain one generation.
//
// State machine (resolved under mu for CAS correctness):
//  1. Happy path: presentedToken is the current active token, not expired, not revoked.
//     → rotate: generate new ID, update record in-place, return updated token.
//  2. Expired: current active token, ExpiresAt in the past.
//     → ErrTokenExpired.
//  3. Revoked: current active token, revokedAt non-zero.
//     → ErrTokenRevoked.
//  4. Grace retry: presentedToken found in byObsolete index, not revoked,
//     AND (now - current.LastUsed) <= Policy.ReuseInterval.
//     → idempotent: return current token copy without a new rotate.
//  5. Reuse detection: presentedToken found in byObsolete index but grace window elapsed.
//     → cascade Revoke(sessionID) inline, return ErrTokenReused.
//  6. Not found: presentedToken in neither index.
//     → ErrTokenNotFound.
//
// ref: dexidp/dex server/refreshhandlers.go UpdateRefreshToken callback CAS pattern
// ref: F2 contract C1+C5
func (s *store) Rotate(_ context.Context, presentedToken string) (*refresh.Token, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clock.Now()

	// Case 1-3: presentedToken is the current active token.
	if rec, ok := s.byID[presentedToken]; ok {
		return s.rotateActive(rec, now)
	}

	// Case 4-5: presentedToken is an obsolete token from a previous generation.
	if rec, ok := s.byObsolete[presentedToken]; ok {
		return s.rotateObsolete(rec, presentedToken, now)
	}

	// Case 6: not found in either index.
	return nil, refresh.ErrTokenNotFound
}

// rotateActive handles Rotate when presentedToken matches the current active record.
// Called with mu held.
func (s *store) rotateActive(rec *tokenRecord, now time.Time) (*refresh.Token, error) {
	if rec.isRevoked() {
		return nil, refresh.ErrTokenRevoked
	}
	if now.After(rec.expiresAt) {
		return nil, refresh.ErrTokenExpired
	}

	// Happy path: generate new token ID and update the record in-place.
	newID, err := s.generateTokenID()
	if err != nil {
		return nil, err
	}

	oldID := rec.id

	// Update indexes: remove old active entry, add new active entry.
	delete(s.byID, oldID)
	s.byID[newID] = rec

	// Single-generation obsolete invariant: PG's refresh_tokens table holds one
	// obsolete_token value per row (see migration 007). When a record advances
	// another generation (oldID becomes the new obsolete and the previous
	// obsoleteToken is dropped), drop the byObsolete entry that pointed to the
	// previous generation so memstore stays aligned with the PG model.
	//
	// ref: plan §F2 C1 — record holds at most one obsolete_token at any time.
	if rec.obsoleteToken != "" {
		delete(s.byObsolete, rec.obsoleteToken)
	}

	// Update record in-place (CAS under mu — only one goroutine reaches here).
	rec.obsoleteToken = oldID
	rec.id = newID
	rec.lastUsed = now

	// Register old token as obsolete so grace-retry and reuse-detection work.
	s.byObsolete[oldID] = rec

	return rec.toToken(), nil
}

// rotateObsolete handles Rotate when presentedToken matches an obsolete (previous-
// generation) token. Implements grace-period idempotency and reuse detection.
// Called with mu held.
//
// ref: dexidp/dex server/refreshhandlers.go AllowedToReuse(reuseInterval, lastUsed, now)
func (s *store) rotateObsolete(rec *tokenRecord, presentedObsolete string, now time.Time) (*refresh.Token, error) {
	_ = presentedObsolete // only used for the index lookup (already done by caller)

	if rec.isRevoked() {
		// Already cascade-revoked from a prior reuse detection — surface as revoked.
		return nil, refresh.ErrTokenRevoked
	}

	// Grace window: if the current token was used (i.e. rotated) recently, the
	// client's retry is likely a network-retransmit or a concurrent request
	// racing with another goroutine on the same stale token — not an attack.
	//
	// AllowedToReuse: (now - lastUsed) <= reuseInterval
	//
	// Zero-delta (now == lastUsed) is explicitly treated as a grace retry, not
	// a race-to-attack: under a deterministic FakeClock two goroutines may read
	// the same Now(); under PG, NOW() has second-level resolution and concurrent
	// requests can observe identical last_used timestamps. Both are legitimate
	// concurrent scenarios per OAuth2 RFC 6749 §6 guidance on refresh token
	// rotation and match Dex/Fosite behaviour.
	//
	// ref: dexidp/dex server/refreshhandlers.go AllowedToReuse uses strict `<`
	//      with no lower bound on elapsed.
	// ref: ory/fosite refresh_token_strategy treats same-window duplicates as
	//      concurrent retries, not reuse attacks.
	elapsed := now.Sub(rec.lastUsed)
	if elapsed <= s.policy.ReuseInterval {
		// Idempotent grace retry: return the current active token. ObsoleteToken
		// is not set in the response — only the goroutine that performed the
		// actual rotation receives ObsoleteToken in its return value. Grace-retry
		// callers need only the current token ID to continue their session.
		tok := rec.toToken()
		tok.ObsoleteToken = ""
		return tok, nil
	}

	// Reuse detection: obsolete token presented outside the grace window — attack signal.
	// Cascade-revoke the entire session chain.
	s.revokeSessionLocked(rec.sessionID, now)
	return nil, refresh.ErrTokenReused
}

// revokeSessionLocked marks all records in the session as revoked.
// Must be called with mu held.
func (s *store) revokeSessionLocked(sessionID string, now time.Time) {
	for _, rec := range s.bySession[sessionID] {
		if rec.revokedAt.IsZero() {
			rec.revokedAt = now
		}
	}
}

// Revoke marks all tokens in the session as revoked.
//
// Idempotent: calling Revoke on an already-revoked session is a no-op.
// Consistency: L1 LocalTx — bulk update under mu.
func (s *store) Revoke(_ context.Context, sessionID string) error {
	now := s.clock.Now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.revokeSessionLocked(sessionID, now)
	return nil
}

// GC removes tokens whose ExpiresAt < olderThan. Revoked tokens past their
// ExpiresAt are also removed. Returns the count of records removed.
//
// Consistency: L0 LocalOnly — best-effort cleanup, no transactional guarantee.
func (s *store) GC(_ context.Context, olderThan time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	removed := 0
	for _, recs := range s.bySession {
		for _, rec := range recs {
			if rec.expiresAt.Before(olderThan) {
				s.removeRecordLocked(rec)
				removed++
			}
		}
	}

	// Rebuild bySession to remove nil / pruned entries.
	s.rebuildBySessionLocked()

	return removed, nil
}

// removeRecordLocked removes a single record from byID and byObsolete indexes.
// It does NOT touch bySession (caller must call rebuildBySessionLocked after).
// Must be called with mu held.
func (s *store) removeRecordLocked(rec *tokenRecord) {
	delete(s.byID, rec.id)
	if rec.obsoleteToken != "" {
		delete(s.byObsolete, rec.obsoleteToken)
	}
	// Mark the record itself as unreachable by zeroing its session link.
	// rebuildBySessionLocked will drop it from bySession.
	rec.sessionID = ""
}

// rebuildBySessionLocked reconstructs bySession by scanning byID.
// Must be called with mu held.
func (s *store) rebuildBySessionLocked() {
	newBySession := make(map[string][]*tokenRecord, len(s.bySession))
	for _, rec := range s.byID {
		if rec.sessionID != "" {
			newBySession[rec.sessionID] = append(newBySession[rec.sessionID], rec)
		}
	}
	s.bySession = newBySession
}
