package cell

// auth_types.go — kernel/cell narrow interfaces for auth plan dependencies.
//
// These interfaces mirror the signatures of runtime/auth concrete types so that
// AuthPlan structs (AuthJWT, AuthServiceToken, etc.) can hold their dependencies
// as kernel-level interfaces, keeping kernel/ free of runtime/ imports.
//
// Design: every interface declared here must be structurally satisfied by the
// corresponding runtime/auth concrete type without modification, or the concrete
// type is adjusted in the same PR (see runtime/auth/servicetoken.go).
//
// ref: kubernetes/apiserver pkg/authentication/authenticator/interfaces.go — sealed
// interface + segregated Token/Request/Password authenticators.

import (
	"context"
	"time"
)

// TokenIntent distinguishes how a JWT is meant to be used. The values here are
// the canonical definition; runtime/auth.TokenIntent is a type alias of this type.
type TokenIntent string

const (
	// TokenIntentAccess marks a short-lived credential for calling business
	// endpoints. Verifier rejects any access token replayed at /auth/refresh.
	TokenIntentAccess TokenIntent = "access"
)

// IsValid reports whether the intent is one of the known enum values.
func (t TokenIntent) IsValid() bool {
	return t == TokenIntentAccess
}

// Claims represents the decoded token claims. This is the canonical definition;
// runtime/auth.Claims is a type alias of this type so callers share the same struct
// without conversion at package boundaries.
type Claims struct {
	// Subject is the principal identifier (user ID, service name, etc.).
	Subject string
	// Issuer identifies the token issuer.
	Issuer string
	// Audience is the intended recipient(s).
	Audience []string
	// ExpiresAt is the expiration time.
	ExpiresAt time.Time
	// IssuedAt is the token issue time.
	IssuedAt time.Time
	// Roles is the set of roles associated with the subject.
	Roles []string
	// TokenUse records the intent declared by the JWT's token_use claim.
	TokenUse TokenIntent
	// SessionID is the "sid" claim binding the token to a specific session.
	SessionID string
	// PasswordResetRequired indicates that the subject must change their password.
	PasswordResetRequired bool
	// JTI is the JWT ID claim ("jti"), a unique identifier for the token.
	// Empty string when the claim is absent.
	JTI string
	// AuthzEpoch is the authorization epoch counter ("authz_epoch"). When non-zero
	// it carries a monotonically increasing version that authorization policy
	// engines use to detect stale token caches. Zero is a valid epoch value
	// (it is always present in tokens issued after S4b Batch 1A).
	AuthzEpoch int64
	// Extra holds additional claims not covered by the standard fields.
	Extra map[string]any
}

// IntentTokenVerifier verifies a JWT token and requires its declared intent
// to match the expected value. This is the kernel projection of the same
// interface in runtime/auth; runtime/auth.JWTVerifier satisfies it structurally.
type IntentTokenVerifier interface {
	VerifyIntent(ctx context.Context, token string, expected TokenIntent) (Claims, error)
}

// NonceStoreKind classifies a NonceStore implementation for startup validation.
// Mirrors runtime/auth.NonceStoreKind; kept as a string for extensibility.
type NonceStoreKind string

const (
	// NonceStoreKindNoop is the explicit disable-replay-check sentinel.
	// Production deployments must reject this kind for service-token guards.
	NonceStoreKindNoop NonceStoreKind = "noop"
	// NonceStoreKindInMemory is the single-process map-backed implementation.
	NonceStoreKindInMemory NonceStoreKind = "in_memory"
	// NonceStoreKindDistributed is reserved for shared backends (Redis, etc.).
	NonceStoreKindDistributed NonceStoreKind = "distributed"
)

// NonceStore tracks nonces for replay prevention. This is the kernel projection
// of runtime/auth.NonceStore; runtime/auth.InMemoryNonceStore and
// runtime/auth.NoopNonceStore satisfy it structurally.
// Note: the kernel interface uses (ctx, key, ttl) to accommodate future
// distributed implementations; runtime/auth.NonceStore uses (ctx, nonce) without ttl.
// We match the existing runtime/auth.NonceStore signature so no adapter is needed.
type NonceStore interface {
	// CheckAndMark checks whether nonce has been seen within its TTL window.
	// If not, it marks the nonce and returns nil. Returns ErrNonceReused on replay.
	CheckAndMark(ctx context.Context, nonce string) error
	// Kind reports the implementation classification.
	Kind() NonceStoreKind
}

// HMACKeyring supplies HMAC secrets for service token operations.
// This is the kernel projection of runtime/auth.HMACKeyRing; *HMACKeyRing
// satisfies it structurally (Current/Secrets methods already exist).
type HMACKeyring interface {
	// Current returns a copy of the active signing secret.
	Current() []byte
	// Secrets returns all secrets in try-order: current first, then previous.
	Secrets() [][]byte
}

// AuthProvider is an optional cell-level interface that exposes an
// IntentTokenVerifier for runtime authentication. AuthJWTFromAssembly walks
// the assembly's CellIDs in deterministic order during bootstrap phase4 and
// promotes the unique implementer's verifier; zero, multiple, or nil
// verifiers are rejected with a startup error.
type AuthProvider interface {
	TokenVerifier() IntentTokenVerifier
}
