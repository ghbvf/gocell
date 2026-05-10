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

	// CreatedAt and ExpiresAt are the issue / expiry timestamps in UTC.
	CreatedAt time.Time
	ExpiresAt time.Time

	// RevokedAt is non-nil iff Revoke / RevokeForSubject has marked this row
	// dead. Once set it must never be cleared (append-only revoke semantics
	// — ADR-Session D3 fail-closed).
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
//   - Get: fetch by Session.ID. Missing → ErrSessionNotFound; revoked /
//     expired sessions are still returned (caller decides via fields).
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
	Get(ctx context.Context, id string) (*Session, error)
	Revoke(ctx context.Context, id string) error
	RevokeForSubject(ctx context.Context, subjectID string, event CredentialEvent) error
}
