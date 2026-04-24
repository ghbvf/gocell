package refresh

import (
	"context"
	"time"
)

// Store persists refresh token chains with CAS-protected Rotate and
// reuse detection. Implementations MUST honour the append-only lineage
// model: Issue and Rotate only INSERT rows; rotated_at and revoked_at are
// one-way timestamp flips; verifier_hash is never updated in place.
//
// Every unhappy Peek/Rotate path returns ErrRejected. Internal diagnostic
// reasons surface through the slog structured field "reason", not through
// error shape (enumeration / timing side-channel defence).
//
// ref: ory/fosite token/hmac/hmacsha.go (base64url nopad + hmac.Equal)
// ref: ory/hydra persistence/sql/persister_oauth2.go (CAS + grace + chain revoke)
type Store interface {
	// Issue creates a new refresh chain for (sessionID, subjectID). The
	// Store generates an opaque wire token of the form
	// base64url(selector_16B) "." base64url(verifier_32B) and persists only
	// (selector, sha256(verifier)). Token.ExpiresAt = now + Policy.MaxAge.
	//
	// Consistency: L1 LocalTx — single INSERT, no outbox event.
	Issue(ctx context.Context, sessionID, subjectID string) (wire string, tok *Token, err error)

	// Peek validates the presented wire token and returns the metadata for the
	// currently-presented row without issuing a child and without flipping
	// rotated_at. Callers use this for no-side-effect preflight checks before
	// deciding whether to commit a rotation.
	//
	// Branches and public error shape match Rotate exactly. Implementations
	// MUST still cascade-revoke on reuse detection beyond Policy.ReuseInterval;
	// that is a security response to an attack, not a successful state advance.
	//
	// Consistency: L1 LocalTx — read-only for valid tokens and non-reuse
	// rejections; reuse-detection cascade revoke is committed before returning
	// ErrRejected.
	Peek(ctx context.Context, presentedWire string) (tok *Token, err error)

	// Rotate consumes the presented wire token and advances the chain by
	// issuing a new child. Returns the new wire token alongside its
	// metadata on success.
	//
	// Branches (all unhappy paths return ErrRejected with a diagnostic
	// "reason" emitted via slog — callers MUST NOT distinguish):
	//
	//   - active happy path: presented token is the current live row →
	//     INSERT child, flip parent rotated_at, return new wire.
	//   - grace retry: parent's rotated_at was already set but the retry
	//     arrived within Policy.ReuseInterval → INSERT another child,
	//     return a distinct new wire. Preserves idempotency for SPA
	//     double-submit without weakening reuse detection.
	//   - reuse detection: parent's rotated_at was set beyond
	//     Policy.ReuseInterval → cascade-revoke the entire session_id
	//     lineage, emit slog Error "reuse_detected", return ErrRejected.
	//   - malformed / unknown selector / verifier mismatch / expired /
	//     revoked: return ErrRejected with corresponding slog "reason".
	//
	// Consistency: L1 LocalTx — INSERT child + UPDATE parent within one
	// transaction; reuse-detection cascade revoke is committed before
	// returning ErrRejected.
	Rotate(ctx context.Context, presentedWire string) (wire string, tok *Token, err error)

	// RevokeSession marks every row in the session_id lineage as revoked.
	// Idempotent — 0 rows affected is not an error. Called by logout and
	// reuse-detection cascade.
	//
	// Consistency: L1 LocalTx — single UPDATE.
	RevokeSession(ctx context.Context, sessionID string) error

	// RevokeUser marks every row belonging to subjectID as revoked.
	// Called by user-delete, user-lock, and change-password flows to
	// invalidate every refresh chain owned by the subject in one atomic
	// statement. Idempotent.
	//
	// Consistency: L1 LocalTx — single UPDATE.
	RevokeUser(ctx context.Context, subjectID string) error

	// GC removes rows whose expires_at < olderThan. Safe to run from a
	// background worker; batched with SKIP LOCKED to avoid contending with
	// active Rotate traffic.
	//
	// Consistency: L0 LocalOnly — best-effort cleanup.
	GC(ctx context.Context, olderThan time.Time) (removed int, err error)
}
