// Package refresh provides the server-side opaque refresh token store interface
// and in-memory implementation used by the accesscore session slices.
//
// Design: append-only lineage with selector/verifier split. Every Issue and
// every Rotate inserts a new row; nothing is ever updated in place except the
// one-way flips rotated_at and revoked_at. The wire token returned to clients
// is base64url(selector_16B) + "." + base64url(verifier_32B) — 66 chars
// deterministic. Only the selector is indexed; only SHA-256(verifier) is
// persisted. A DB snapshot therefore contains no credential-equivalent data.
//
// ref: ory/fosite token/hmac/hmacsha.go (base64url nopad, 32 B entropy, hmac.Equal)
// ref: ory/hydra persistence/sql/persister_oauth2.go (CAS chain + reuse detection)
// ref: zitadel/zitadel internal/api/oidc/token_refresh.go (revoke-on-use baseline)
package refresh

import (
	"time"

	"github.com/google/uuid"
)

// Token is the persisted refresh token metadata returned by Issue and Rotate.
//
// The wire token (the opaque string clients present) is returned separately
// from Issue/Rotate as a string; Token itself carries only server-side identity
// and lifetime, never the verifier or any credential-equivalent value.
type Token struct {
	ID        uuid.UUID
	SessionID string
	SubjectID string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// Policy controls Rotate semantics and token lifetime.
//
// ReuseInterval is the grace window: if a parent token is presented a second
// time within this duration after it was rotated, the store issues another
// child rather than flagging a reuse attack. This absorbs legitimate
// double-submissions from SPAs and at-least-once client retries.
//
// MaxAge bounds Token.ExpiresAt - Token.CreatedAt.
//
// Defaults (not enforced; zero values panic in constructors):
//
//	ReuseInterval = 2 * time.Second  (matches Ory Hydra grace_period)
//	MaxAge        = 7 * 24 * time.Hour
type Policy struct {
	ReuseInterval time.Duration
	MaxAge        time.Duration
}

// Clock abstracts time.Now for deterministic testing.
type Clock interface {
	Now() time.Time
}
