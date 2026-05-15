// Package memstore provides an in-memory implementation of refresh.Store.
//
// This implementation is the oracle for the storetest contract suite and
// serves as a unit-test double for services that depend on refresh.Store.
// It is NOT suitable for production — state is not persisted across restarts.
//
// Data model: a single append-only slice of tokenRecord. Each Issue and each
// Rotate appends one record; rotated_at and revoked_at are one-way flips.
// Parent→child lineage is tracked via parentID (uuid.Nil for roots).
package memstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

// tokenRecord is one append-only row. Fields are never mutated after insert
// except rotatedAt, revokedAt, firstUsedAt, and usedTimes (one-way flips /
// monotonic increment).
type tokenRecord struct {
	id            uuid.UUID
	parentID      uuid.UUID
	sessionID     string
	subjectID     string
	selector      []byte
	verifierHash  [sha256.Size]byte
	createdAt     time.Time
	expiresAt     time.Time
	idleExpiresAt time.Time // sliding window; always set (Policy.MaxIdle required positive)
	rotatedAt     time.Time // zero means live-latest
	revokedAt     time.Time // zero means not revoked
	firstUsedAt   time.Time // zero until first grace re-use
	usedTimes     int       // grace re-use counter
	// authzEpochAtIssue: snapshot of users.authz_epoch at Issue time. Children
	// of the chain (created via Rotate) inherit the parent's value — refresh
	// is the continuation of the original grant (OAuth2 §1.5 / ADR-D4.1), so
	// the entire chain shares one issue-time epoch. Stale-grant detection is
	// the service layer's job (sessionrefresh compares against current
	// users.authz_epoch before Rotate).
	authzEpochAtIssue int64
}

func (r *tokenRecord) isRotated() bool { return !r.rotatedAt.IsZero() }
func (r *tokenRecord) isRevoked() bool { return !r.revokedAt.IsZero() }

func (r *tokenRecord) toToken() *refresh.Token {
	return &refresh.Token{
		ID:                r.id,
		SessionID:         r.sessionID,
		SubjectID:         r.subjectID,
		CreatedAt:         r.createdAt,
		ExpiresAt:         r.expiresAt,
		AuthzEpochAtIssue: r.authzEpochAtIssue,
	}
}

// store is the in-memory implementation of refresh.Store.
//
// Concurrency: one Mutex guards all mutable state. Memstore targets the
// storetest oracle, not production throughput.
type store struct {
	mu     sync.Mutex
	policy refresh.Policy
	clock  clock.Clock
	rand   io.Reader

	rows []*tokenRecord
}

// New constructs an in-memory refresh.Store.
//
// Returns an error when clock is nil or when the Policy is invalid (see
// refresh.Policy.Validate). If randReader is nil, crypto/rand.Reader is used.
func New(policy refresh.Policy, clock clock.Clock, randReader io.Reader) (refresh.Store, error) {
	if validation.IsNilInterface(clock) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "memstore.New: clock must not be nil")
	}
	if err := policy.Validate(); err != nil {
		return nil, errcode.Wrap(errcode.KindInvalid, errcode.ErrValidationFailed, "memstore.New", err)
	}
	if validation.IsNilInterface(randReader) {
		randReader = rand.Reader
	}
	return &store{policy: policy, clock: clock, rand: randReader}, nil
}

func (s *store) generatePair() (selector []byte, verifier []byte, err error) {
	return refresh.GeneratePair(s.rand)
}

// Issue creates a new refresh chain root. L1 LocalTx.
//
// authzEpochAtIssue must be > 0 (S4d): a zero value indicates the caller did
// not snapshot the user's current authz_epoch, which would let stale grants
// validate forever once users.authz_epoch is bumped.
func (s *store) Issue(_ context.Context, sessionID, subjectID string, authzEpochAtIssue int64) (string, *refresh.Token, error) {
	if authzEpochAtIssue == 0 {
		return "", nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"refresh.Issue: authzEpochAtIssue required (non-zero)")
	}
	sel, ver, err := s.generatePair()
	if err != nil {
		return "", nil, err
	}
	now := s.clock.Now()
	rec := &tokenRecord{
		id:                uuid.New(),
		parentID:          uuid.Nil,
		sessionID:         sessionID,
		subjectID:         subjectID,
		selector:          sel,
		verifierHash:      sha256.Sum256(ver),
		createdAt:         now,
		expiresAt:         now.Add(s.policy.MaxAge),
		idleExpiresAt:     s.idleDeadline(now),
		authzEpochAtIssue: authzEpochAtIssue,
	}

	s.mu.Lock()
	s.rows = append(s.rows, rec)
	s.mu.Unlock()

	return refresh.EncodeOpaque(sel, ver), rec.toToken(), nil
}

// idleDeadline returns now + MaxIdle. Policy.MaxIdle is guaranteed positive by
// Validate(), so no zero-check is needed.
func (s *store) idleDeadline(now time.Time) time.Time {
	return now.Add(s.policy.MaxIdle)
}

// Peek validates the presented wire token without advancing the lineage.
//
// Per refresh.Store contract: on ErrReused the row identity must be returned
// alongside the error so the service layer can drive a user-wide credential
// invalidation cascade. validatePresentedLocked preserves rec on the reuse
// branches; this method propagates it.
func (s *store) Peek(_ context.Context, presented string) (*refresh.Token, error) {
	sel, ver, ok := refresh.ParseOpaque(presented)
	if !ok {
		return nil, rejectWithReason("malformed")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.validatePresentedLocked(sel, ver)
	if err != nil {
		if errors.Is(err, refresh.ErrReused) && rec != nil {
			return rec.toToken(), err
		}
		return nil, err
	}
	return rec.toToken(), nil
}

// Rotate advances the chain one generation by appending a child record.
// See Store.Rotate contract for branch behavior.
//
// ErrReused contract: when reuse is detected the row identity must be
// returned alongside the error so the service layer can drive the user-wide
// cascade.
func (s *store) Rotate(_ context.Context, presented string) (string, *refresh.Token, error) {
	sel, ver, ok := refresh.ParseOpaque(presented)
	if !ok {
		return "", nil, rejectWithReason("malformed")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rec, err := s.validatePresentedLocked(sel, ver)
	if err != nil {
		if errors.Is(err, refresh.ErrReused) && rec != nil {
			return "", rec.toToken(), err
		}
		return "", nil, err
	}
	now := s.clock.Now()

	// Happy path or grace retry — both issue a child. The child's idleExpiresAt
	// is reset to now+MaxIdle (sliding window per Rotate).
	newSel, newVer, err := s.generatePair()
	if err != nil {
		return "", nil, err
	}
	child := &tokenRecord{
		id:            uuid.New(),
		parentID:      rec.id,
		sessionID:     rec.sessionID,
		subjectID:     rec.subjectID,
		selector:      newSel,
		verifierHash:  sha256.Sum256(newVer),
		createdAt:     now,
		expiresAt:     now.Add(s.policy.MaxAge),
		idleExpiresAt: s.idleDeadline(now),
		// Refresh is the continuation of the original grant; child inherits
		// the chain's issue-time epoch (ADR-D4.1 + S4d §A8). Stale-grant
		// detection happens in sessionrefresh, not here.
		authzEpochAtIssue: rec.authzEpochAtIssue,
	}
	s.rows = append(s.rows, child)

	// Flip parent.rotatedAt if this is the first rotation.
	if !rec.isRotated() {
		rec.rotatedAt = now
	} else {
		// Grace retry: increment grace counter.
		if rec.firstUsedAt.IsZero() {
			rec.firstUsedAt = now
		}
		rec.usedTimes++
	}

	return refresh.EncodeOpaque(newSel, newVer), child.toToken(), nil
}

func (s *store) validatePresentedLocked(sel, ver []byte) (*tokenRecord, error) {
	now := s.clock.Now()
	rec := s.findBySelectorLocked(sel)
	if rec == nil {
		return nil, rejectWithReason("selector_miss")
	}

	presentedHash := sha256.Sum256(ver)
	if subtle.ConstantTimeCompare(presentedHash[:], rec.verifierHash[:]) != 1 {
		return nil, rejectWithReason("verifier_miss")
	}
	if rec.isRevoked() {
		return nil, rejectWithReason("revoked")
	}
	if !rec.expiresAt.After(now) {
		return nil, rejectWithReason("expired")
	}
	// X12: idle-expiry check. Policy.MaxIdle is required (must be positive),
	// so idleExpiresAt is always set to a meaningful future time.
	if !rec.idleExpiresAt.After(now) {
		return nil, rejectWithReason("idle_expired")
	}

	if rec.isRotated() {
		// X14: grace counter cap check. Policy.GraceMaxReuses is required (must
		// be positive) — no zero-check needed.
		if rec.usedTimes >= s.policy.GraceMaxReuses {
			s.revokeSessionLocked(rec.sessionID, now)
			slog.Error("refresh token grace counter exhausted",
				slog.String("session_id", rec.sessionID),
				slog.String("reason", "reuse_detected"),
				slog.Int("used_times", rec.usedTimes),
			)
			// Carry rec so callers can route the user-wide cascade — refresh.Store
			// contract mandates non-nil *Token alongside ErrReused.
			return rec, refresh.ErrReused
		}

		// Parent already rotated — either grace retry or reuse attack.
		if now.Sub(rec.rotatedAt) > s.policy.ReuseInterval {
			s.revokeSessionLocked(rec.sessionID, now)
			slog.Error("refresh token reuse detected",
				slog.String("session_id", rec.sessionID),
				slog.String("reason", "reuse_detected"),
			)
			return rec, refresh.ErrReused
		}
	}

	return rec, nil
}

// RevokeSession marks every row in the session_id lineage as revoked.
func (s *store) RevokeSession(_ context.Context, sessionID string) error {
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revokeSessionLocked(sessionID, now)
	return nil
}

// RevokeSessionDetached marks every row in the session_id lineage as revoked.
// Memstore has no ambient transaction/cancellation boundary, so it shares the
// same locked critical section as RevokeSession.
func (s *store) RevokeSessionDetached(ctx context.Context, sessionID string) error {
	return s.RevokeSession(ctx, sessionID)
}

// RevokeUser marks every row owned by subjectID as revoked.
func (s *store) RevokeUser(_ context.Context, subjectID string) error {
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.rows {
		if rec.subjectID == subjectID && !rec.isRevoked() {
			rec.revokedAt = now
		}
	}
	return nil
}

// GC removes rows whose effective expiry is before olderThan. The effective
// expiry is LEAST(expiresAt, idleExpiresAt). L0 best-effort.
func (s *store) GC(_ context.Context, olderThan time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.rows[:0]
	removed := 0
	for _, rec := range s.rows {
		effectiveExpiry := rec.expiresAt
		if rec.idleExpiresAt.Before(effectiveExpiry) {
			effectiveExpiry = rec.idleExpiresAt
		}
		if effectiveExpiry.Before(olderThan) {
			removed++
			continue
		}
		kept = append(kept, rec)
	}
	s.rows = kept
	return removed, nil
}

// findBySelectorLocked returns the sole row with this selector, or nil.
//
// Selector uniqueness is enforced by generatePair's 128-bit entropy (collision
// probability negligible); we still prefer the latest-inserted row if multiple
// ever existed, matching PG's idx_refresh_tokens_selector_live semantics.
func (s *store) findBySelectorLocked(sel []byte) *tokenRecord {
	for _, v := range slices.Backward(s.rows) {
		if subtle.ConstantTimeCompare(v.selector, sel) == 1 {
			return v
		}
	}
	return nil
}

func (s *store) revokeSessionLocked(sessionID string, now time.Time) {
	for _, rec := range s.rows {
		if rec.sessionID == sessionID && !rec.isRevoked() {
			rec.revokedAt = now
		}
	}
}

// rejectWithReason emits a Warn slog line and returns ErrRejected. Every
// unhappy Rotate branch (except reuse_detected, which is Error-level)
// funnels through here so shape and log cadence are uniform.
func rejectWithReason(reason string) error {
	slog.Warn("refresh token rejected", slog.String("reason", reason))
	return refresh.ErrRejected
}
