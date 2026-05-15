package refresh

import (
	"context"
	"time"
)

// Store persists refresh token chains with CAS-protected Rotate and
// reuse detection. Implementations MUST honor the append-only lineage
// model: Issue and Rotate only INSERT rows; rotated_at and revoked_at are
// one-way timestamp flips; verifier_hash is never updated in place.
//
// Error vocabulary (Peek / Rotate):
//
//   - happy / grace-retry → (non-nil *Token, nil)
//   - reuse detected     → (non-nil *Token, ErrReused) — Token MUST carry
//     SubjectID and SessionID so callers can drive a user-wide
//     credential-invalidation cascade (epoch bump + revoke other sessions +
//     revoke other refresh chains). The single-session cascade-revoke is
//     committed by the store before return; user-wide invalidation is the
//     service layer's responsibility and depends on this metadata.
//   - any other rejection (malformed / selector_miss / verifier_miss /
//     revoked / expired / idle_expired) → (nil, ErrRejected). Internal
//     diagnostic reasons surface through the slog structured field "reason",
//     not through error shape (enumeration / timing side-channel defense).
//
// Returning (nil, ErrReused) is a contract violation — the service layer
// would silently lose the cross-session cascade. The runtime/auth/refresh/
// storetest conformance suite enforces this invariant against every
// implementation.
//
// ref: ory/fosite token/hmac/hmacsha.go (base64url nopad + hmac.Equal)
// ref: ory/hydra persistence/sql/persister_oauth2.go (CAS + grace + chain revoke)
// ref: keycloak TokenManager — reuse → full session-scoped revocation
type Store interface {
	// Issue creates a new refresh chain for (sessionID, subjectID). The
	// Store generates an opaque wire token of the form
	// base64url(selector_16B) "." base64url(verifier_32B) and persists only
	// (selector, sha256(verifier)). Token.ExpiresAt = now + Policy.MaxAge.
	//
	// authzEpochAtIssue is the snapshot of users.authz_epoch at issue time;
	// it is persisted on the row and returned to validate paths so that
	// sessionrefresh can reject stale grants (S4d). Zero is invalid:
	// implementations return ErrValidationFailed (storetest conformance
	// T-S4D-2 enforces).
	//
	// Consistency: L1 LocalTx — single INSERT, no outbox event.
	Issue(ctx context.Context, sessionID, subjectID string, authzEpochAtIssue int64) (wire string, tok *Token, err error)

	// Peek validates the presented wire token and returns the metadata for the
	// currently-presented row without issuing a child and without flipping
	// rotated_at. Callers use this for no-side-effect preflight checks before
	// deciding whether to commit a rotation.
	//
	// Return shape matches the package-level vocabulary above:
	//   - valid token            → (*Token{SubjectID, SessionID, ...}, nil)
	//   - reuse detected         → (*Token{SubjectID, SessionID, ...}, ErrReused)
	//   - any other rejection    → (nil, ErrRejected)
	//
	// Implementations MUST still commit the per-session cascade-revoke before
	// returning ErrReused — that is a security response and persists
	// regardless of caller transaction outcome. The token returned alongside
	// ErrReused conveys *only* the row identity (SubjectID, SessionID, ID,
	// CreatedAt, ExpiresAt); it is not a usable refresh credential.
	//
	// Consistency: L1 LocalTx — read-only for valid tokens; reuse-detection
	// cascade revoke is committed before returning ErrReused.
	Peek(ctx context.Context, presentedWire string) (tok *Token, err error)

	// Rotate consumes the presented wire token and advances the chain by
	// issuing a new child. Returns the new wire token alongside its
	// metadata on success.
	//
	// Branches (return shape matches the package-level vocabulary):
	//
	//   - active happy path: presented token is the current live row →
	//     INSERT child, flip parent rotated_at, return new wire +
	//     (*Token{...}, nil).
	//   - grace retry: parent's rotated_at was already set but the retry
	//     arrived within Policy.ReuseInterval → INSERT another child,
	//     return a distinct new wire + (*Token{...}, nil). Preserves
	//     idempotency for SPA double-submit without weakening reuse
	//     detection.
	//   - reuse detection: parent's rotated_at was set beyond
	//     Policy.ReuseInterval, OR grace counter cap exhausted →
	//     cascade-revoke the entire session_id lineage, emit slog Error
	//     "reuse_detected", return ("", *Token{SubjectID, SessionID, ...},
	//     ErrReused). The token conveys row identity for the service-layer
	//     user-wide invalidation cascade.
	//   - malformed / unknown selector / verifier mismatch / expired /
	//     revoked: return ("", nil, ErrRejected) with corresponding slog
	//     "reason".
	//
	// Consistency: L1 LocalTx — INSERT child + UPDATE parent within one
	// transaction; reuse-detection cascade revoke is committed before
	// returning ErrReused.
	Rotate(ctx context.Context, presentedWire string) (wire string, tok *Token, err error)

	// RevokeSession marks every row in the session_id lineage as revoked.
	// Idempotent — 0 rows affected is not an error. Called by business flows
	// such as logout, where refresh-chain revoke must share the caller's
	// transaction boundary with session state and outbox writes.
	//
	// Consistency: L1 LocalTx — single UPDATE.
	RevokeSession(ctx context.Context, sessionID string) error

	// RevokeSessionDetached marks every row in the session_id lineage as
	// revoked outside the caller's ambient transaction/cancellation boundary.
	// It is reserved for security/compensation paths where the revoke must
	// persist even if the triggering request is canceled or the surrounding
	// business transaction rolls back.
	//
	// Consistency: L1 LocalTx — single UPDATE committed independently by
	// durable implementations. In-memory implementations may share the same
	// critical section as RevokeSession.
	RevokeSessionDetached(ctx context.Context, sessionID string) error

	// RevokeUser marks every row belonging to subjectID as revoked.
	// Called by user-delete, user-lock, and change-password flows to
	// invalidate every refresh chain owned by the subject in one atomic
	// statement. Idempotent. There is intentionally no detached RevokeUser:
	// these user-level revokes are business state transitions that must remain
	// atomic with user/session mutations and related outbox writes. Session-
	// level cascade revoke is split because it also serves security responses
	// to token reuse and compensating cleanup.
	//
	// Consistency: L1 LocalTx — single UPDATE.
	RevokeUser(ctx context.Context, subjectID string) error

	// GC removes rows whose effective expiry has passed olderThan. Effective
	// expiry is the earlier of expires_at (hard cap) and idle_expires_at
	// (sliding window driven by Policy.MaxIdle). Implementations MUST sweep on
	// the LEAST(expires_at, idle_expires_at) so an idle-abandoned chain is
	// reclaimed without waiting for the hard MaxAge horizon.
	//
	// Safe to run from a background worker; batched with SKIP LOCKED to avoid
	// contending with active Rotate traffic.
	//
	// Consistency: L0 LocalOnly — best-effort cleanup.
	GC(ctx context.Context, olderThan time.Time) (removed int, err error)
}
