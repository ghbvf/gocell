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

// Default policy values used when the caller does not configure optional fields.
//
// Zero values for MaxIdle and GraceMaxReuses are accepted by NewRefreshStore /
// memstore.New (via Policy.Validate) and mean "feature disabled" — pre-016
// stores or deployments that intentionally opt out of idle expiry / grace
// counter cap. Callers that want the standard behavior set these constants.
// MaxAge and ReuseInterval are still strictly validated (positive / non-negative).
const (
	// DefaultMaxIdle is the default idle-expiry window (30 days).
	// Matches Zitadel auth-token default retention period.
	// ref: zitadel/zitadel internal/repository/session expire.go
	DefaultMaxIdle = 30 * 24 * time.Hour

	// DefaultGraceMaxReuses is the default grace reuse counter cap (3 re-uses).
	// Tolerates SPA double-submit + network retry within a grace window without
	// treating them as reuse attacks. Exceeding the cap triggers cascade revoke.
	// ref: ory/fosite handler/oauth2/refresh.go (COALESCE reuse guard)
	DefaultGraceMaxReuses = 3
)

// Policy controls Rotate semantics and token lifetime.
//
// ReuseInterval is the grace window: if a parent token is presented a second
// time within this duration after it was rotated, the store issues another
// child rather than flagging a reuse attack. This absorbs legitimate
// double-submissions from SPAs and at-least-once client retries.
//
// MaxAge bounds Token.ExpiresAt - Token.CreatedAt.
//
// MaxIdle is the sliding-window idle-expiry duration. Every Rotate call
// resets the idle clock on the parent row. A token that is not rotated within
// MaxIdle of its last rotation (or creation) is rejected as idle-expired.
// Zero value disables idle-expiry check (stores that have not migrated yet).
//
// GraceMaxReuses caps how many times a parent token may be re-presented within
// the ReuseInterval grace window. Once the cap is reached, the next re-present
// triggers a cascade revoke (same as an out-of-window reuse attack). Zero value
// disables the counter cap.
//
// Defaults (not enforced; zero values are accepted for backward compat in
// stores that have not yet applied migration 016):
//
//	ReuseInterval  = 2 * time.Second  (matches Ory Hydra grace_period)
//	MaxAge         = 7 * 24 * time.Hour
//	MaxIdle        = DefaultMaxIdle   (30 days)
//	GraceMaxReuses = DefaultGraceMaxReuses (3)
//
// Validate() returns an error if MaxAge is non-positive, ReuseInterval is negative,
// MaxIdle is negative, or GraceMaxReuses is negative. Zero values for MaxIdle and
// GraceMaxReuses are accepted and mean "disabled".
type Policy struct {
	ReuseInterval  time.Duration
	MaxAge         time.Duration
	MaxIdle        time.Duration
	GraceMaxReuses int
}

// Validate returns an error if the Policy contains invalid field values.
//
// MaxAge must be positive. ReuseInterval must not be negative.
// MaxIdle and GraceMaxReuses may be zero (zero = disabled): zero MaxIdle disables
// idle-expiry checks (stores that have not applied migration 016 set MaxIdle=0);
// zero GraceMaxReuses disables the grace counter cap. This aligns with
// NewRefreshStore and memstore.New which treat zero values as disabled rather than
// invalid, and with the far-future sentinel used when MaxIdle is zero.
func (p Policy) Validate() error {
	if p.MaxAge <= 0 {
		return errorf("Policy.MaxAge must be positive")
	}
	if p.ReuseInterval < 0 {
		return errorf("Policy.ReuseInterval must not be negative")
	}
	// MaxIdle == 0 means idle-expiry disabled; negative is invalid.
	if p.MaxIdle < 0 {
		return errorf("Policy.MaxIdle must not be negative (use zero to disable idle expiry)")
	}
	// GraceMaxReuses == 0 means grace counter cap disabled; negative is invalid.
	if p.GraceMaxReuses < 0 {
		return errorf("Policy.GraceMaxReuses must not be negative (use zero to disable grace cap)")
	}
	return nil
}

func errorf(msg string) error {
	// Use a simple wrapper to avoid importing errcode from the runtime layer.
	return &policyError{msg: msg}
}

type policyError struct{ msg string }

func (e *policyError) Error() string { return "refresh.Policy: " + e.msg }
