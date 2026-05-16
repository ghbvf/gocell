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
	// AuthzEpochAtIssue snapshots users.authz_epoch at the moment this
	// refresh row was inserted (Issue / Rotate child). sessionrefresh rejects
	// a presented token whose AuthzEpochAtIssue != current users.authz_epoch
	// via an independent stale-epoch code path (Rotate returns a stale-epoch
	// error which the slice routes to `cascadeRevoke("stale-epoch")`,
	// a session-scoped revocation). This is separate from handleReuseDetected,
	// which is reserved for double-submit reuse detection and triggers a
	// user-wide Invalidator.Apply cascade. Without this column, refresh
	// re-mints access tokens with live user.epoch and stale grants "upgrade"
	// to current epoch (PR #490 review P1-#2).
	//
	// ADR-credential §A6 (stale-epoch path) + §A8 (row-level credential
	// provenance).
	AuthzEpochAtIssue int64
}

// Default policy values. Callers must set these explicitly — Validate does not
// apply implicit defaults (must be set explicitly by callers; no implicit defaults
// applied by Validate).
const (
	// DefaultMaxIdle is the standard idle-expiry window (30 days).
	// Matches Zitadel auth-token default retention period.
	// ref: zitadel/zitadel internal/repository/session expire.go
	DefaultMaxIdle = 30 * 24 * time.Hour

	// DefaultGraceMaxReuses is the standard grace reuse counter cap (3 re-uses).
	// Tolerates SPA double-submit + network retry within a grace window without
	// treating them as reuse attacks. Exceeding the cap triggers cascade revoke.
	// ref: ory/fosite handler/oauth2/refresh.go (COALESCE reuse guard)
	DefaultGraceMaxReuses = 3

	// CascadeRevokeTimeout is the maximum time allowed for a cascade-revoke DB
	// write that runs detached from the caller's cancellation context.
	//
	// Cascade revoke is a security response (reuse-attack or subject-mismatch)
	// that MUST persist even when the HTTP request that triggered it is canceled
	// or times out. The detached context is constructed via
	// pkg/ctxutil.WithDetachedTimeout so the write gets its own 5-second budget
	// independent of the caller's deadline.
	//
	// ref: golang/go context.WithoutCancel (proposal#40221)
	// ref: hashicorp/vault vault/token_store.go quitContext (detached critical write)
	// ref: ADR docs/architecture/202605051800-adr-refresh-store-ambient-tx-and-idle-grace.md
	CascadeRevokeTimeout = 5 * time.Second
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
// MaxIdle is the sliding-window idle-expiry duration. Issue and Rotate write
// the newly issued row's idle deadline as now + MaxIdle. A token row that is
// not rotated before its idle deadline is rejected as idle-expired.
// Must be positive; use DefaultMaxIdle for the standard 30-day window.
//
// GraceMaxReuses caps how many times a parent token may be re-presented within
// the ReuseInterval grace window. Once the cap is reached, the next re-present
// triggers a cascade revoke (same as an out-of-window reuse attack).
// Must be positive; use DefaultGraceMaxReuses for the standard cap of 3.
//
// All four fields are required; Validate returns an error for any non-positive
// or negative value.
type Policy struct {
	ReuseInterval  time.Duration
	MaxAge         time.Duration
	MaxIdle        time.Duration
	GraceMaxReuses int
}

// Validate returns an error if the Policy contains invalid field values.
//
// MaxAge must be positive. ReuseInterval must not be negative.
// MaxIdle must be positive. GraceMaxReuses must be positive.
func (p Policy) Validate() error {
	if p.MaxAge <= 0 {
		return errorf("Policy.MaxAge must be positive")
	}
	if p.ReuseInterval < 0 {
		return errorf("Policy.ReuseInterval must not be negative")
	}
	if p.MaxIdle <= 0 {
		return errorf("Policy.MaxIdle must be positive")
	}
	if p.GraceMaxReuses <= 0 {
		return errorf("Policy.GraceMaxReuses must be positive")
	}
	return nil
}

func errorf(msg string) error {
	// Use a simple wrapper to avoid importing errcode from the runtime layer.
	return &policyError{msg: msg}
}

type policyError struct{ msg string }

func (e *policyError) Error() string { return "refresh.Policy: " + e.msg }
