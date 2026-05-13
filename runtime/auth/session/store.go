package session

import (
	"context"
	"time"
)

// Session is the canonical server-side session record exchanged between
// session.Store implementations and consumers. Field shape is fixed by
// ADR-Session (`docs/architecture/202605101400-adr-credential-session-protocol.md`)
// D1/D2 — jti-only fingerprint reference, AuthzEpoch ordering snapshot, no
// access-token plaintext.
//
// Session is a value record; behavior (revoked/expired predicates) is left
// to call sites until ≥ 3 distinct call sites emerge (then we can extract
// methods — go-standards.md repetition rule).
type Session struct {
	// ID is the opaque server-side session identifier. Storage backends pick
	// the format (UUID, ULID, etc.); the protocol does not interpret it.
	ID string

	// SubjectID is the user identifier that owns this session. The protocol
	// treats this as an opaque string; callers may use any non-empty format
	// (UUID, ULID, etc.).
	//
	// Backends MAY enforce additional shape constraints. For example, the
	// PG-backed store (adapters/postgres.PGSessionStore) requires SubjectID
	// to be a UUID string for FK relationships to the users.id column.
	// Mem store accepts any non-empty string.
	SubjectID string

	// JTI is the JWT jti claim reference (RFC 9068 §2.2.4) — the canonical
	// fingerprint mode for FingerprintJTIRef (ADR-Session D1). The session
	// row holds this reference; the JWT itself never lands in the store.
	JTI string

	// AuthzEpochAtIssue is the user.authz_epoch snapshot captured at sign-in
	// (ADR-Session D2). Validate paths reject when claim.epoch <
	// user.authz_epoch (i.e. the user's epoch has been bumped since issue).
	AuthzEpochAtIssue int64

	// CreatedAt is the issue timestamp in UTC.
	CreatedAt time.Time

	// ExpiresAt is the GC eligibility timestamp in UTC — when the row may be
	// physically deleted by a sweep (migration 018 idx_sessions_expires).
	// It is NOT a validate gate: Store.Get returns a *ValidateView that does
	// not expose this field, so validate paths cannot reach it. JWT exp claim
	// guards access-token lifetime; RevokedAt guards revocation.
	//
	// ref: ory/fosite handler/oauth2/strategy_jwt.go ValidateAccessToken
	// (JWT exp only); hashicorp/vault expiration.go (leaseEntry.ExpireTime
	// physically isolated from token lookup path).
	ExpiresAt time.Time

	// RevokedAt is non-nil iff Revoke / RevokeForSubject has marked this row
	// dead. Once set it must never be cleared (append-only revoke semantics
	// — ADR-Session D3 fail-closed).
	RevokedAt *time.Time
}

// ValidateView is the narrow projection of a Session exposed by Store.Get.
// It carries exactly the fields validate paths (sessionvalidate, sessionrefresh,
// sessionlogout) need to make their decision: identity (ID, SubjectID) and
// revocation (RevokedAt). Session.ExpiresAt is intentionally absent — it is
// GC eligibility metadata, not a validate gate (see Session.ExpiresAt godoc).
//
// This type-level partition mirrors hashicorp/vault's barrier-view isolation:
// token lookup paths physically cannot reach leaseEntry.ExpireTime, so the
// "validate by time comparison" anti-pattern is unrepresentable. Here, the
// equivalent guard is at the Go type level — sess.ExpiresAt is not a field
// on ValidateView, so re-introducing the double-gate fails to compile.
type ValidateView struct {
	ID        string
	SubjectID string
	RevokedAt *time.Time
}

// Store persists session records. Implementations must obey the protocol
// decisions encoded in *Protocol — Create rejects records that violate
// FingerprintMode shape (e.g. empty JTI under FingerprintJTIRef), and
// RevokeForSubject revokes every active session for the subject regardless
// of which CredentialEvent triggered it (D3 fail-closed by default).
//
// Method semantics (ADR-Session §4.2):
//   - Create: persist a new session. Nil session, empty Session.ID, or empty
//     Session.SubjectID return ErrValidationFailed. Records violating the
//     protocol-configured FingerprintMode (e.g. empty JTI under
//     FingerprintJTIRef) return ErrValidationFailed. Duplicate Session.ID
//     returns ErrSessionConflict; the protocol does not mandate uniqueness
//     on (SubjectID, JTI) — that is a backend decision (PG schema in S3+S5).
//   - Get: fetch validate projection by Session.ID. Missing →
//     ErrSessionNotFound; revoked sessions are still returned (caller checks
//     RevokedAt). Session.ExpiresAt (GC eligibility) is intentionally not
//     exposed — validate paths must not gate on it.
//   - Revoke: mark a single session dead. Idempotent: already-revoked or
//     missing IDs are no-ops returning nil (防枚举 — must not leak existence).
//     RevokedAt is set exactly once; subsequent Revoke calls do not re-stamp.
//   - RevokeForSubject: mark every active session for SubjectID dead. Empty
//     subjectID returns ErrValidationFailed; an event value not declared in
//     the CredentialEvent enum returns ErrValidationFailed. With valid
//     arguments, returns nil even when the subject has no sessions; pre-
//     revoked sessions for the subject preserve their original RevokedAt
//     timestamp (append-only revoke per ADR-Session D3).
type Store interface {
	Create(ctx context.Context, s *Session) error
	Get(ctx context.Context, id string) (*ValidateView, error)
	Revoke(ctx context.Context, id string) error
	RevokeForSubject(ctx context.Context, subjectID string, event CredentialEvent) error
}
