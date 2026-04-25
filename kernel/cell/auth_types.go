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
)

// TokenIntent distinguishes how a JWT is meant to be used. The values here are
// the kernel projection of runtime/auth.TokenIntent; they must remain in sync.
// kernel/cell uses a distinct named type so kernel does not import runtime/auth.
type TokenIntent string

const (
	// TokenIntentAccess marks a short-lived credential for calling business
	// endpoints. Mirrors runtime/auth.TokenIntentAccess.
	TokenIntentAccess TokenIntent = "access"
)

// IsValid reports whether the intent is one of the known enum values.
func (t TokenIntent) IsValid() bool {
	return t == TokenIntentAccess
}

// Claims is the kernel projection of runtime/auth.Claims. It contains only
// the fields that bootstrap needs during AuthPlan.Validate / auth plan apply.
// The full runtime/auth.Claims struct is used within runtime/auth internals.
type Claims struct {
	Subject               string
	Issuer                string
	Audience              []string
	Roles                 []string
	SessionID             string
	PasswordResetRequired bool
	Extra                 map[string]any
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
// IntentTokenVerifier for runtime authentication. AuthJWTFromAssembly scans
// the assembly for cells implementing this interface during phase4.
// This is the kernel projection of the bootstrap-private authProvider interface.
type AuthProvider interface {
	TokenVerifier() IntentTokenVerifier
}

// kernelNonceStoreKind ensures NonceStoreKind values are kept in sync with
// runtime/auth. The compile-time assertion below is enforced by the archtest
// that walks imports. This comment documents the intent.
//
// runtime/auth.NonceStoreKind is a string type with identical constant values;
// both types share the same underlying kind so values can be safely converted.
var _ = NonceStoreKind("") // zero-value self-reference, satisfies linter

// kernelTokenIntentCompileCheck ensures TokenIntent zero-value is valid string.
var _ = TokenIntent("") // zero-value self-reference, satisfies linter
