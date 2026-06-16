package auth

// auth_types.go — kernel/auth narrow interfaces for auth plan dependencies.
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

// PrincipalKindClaim is the wire value of the "principal_kind" JWT claim — the
// signed marker that distinguishes a device bearer token from an ordinary user
// token. It is orthogonal to TokenIntent (access vs refresh): principal_kind
// answers "who is the subject" (user vs device), not "how is the token used".
//
// The claim is absent on every token issued before device tokens existed, which
// decodes to PrincipalKindClaimUser (the default). Only the values below are
// legal; an unknown value fails closed at JWTVerifier.VerifyIntent so a typo can
// never silently fall through to a user mint. The marker is trustworthy because
// it lives inside the signed JWT payload.
//
// runtime/auth.PrincipalKindClaim is a type alias of this type.
type PrincipalKindClaim string

const (
	// PrincipalKindClaimUser is the default (absent claim) — an ordinary user
	// principal. It is the empty string so an unset claim decodes to it.
	PrincipalKindClaimUser PrincipalKindClaim = ""
	// PrincipalKindClaimDevice marks a device bearer token. The device-principal
	// issuer (runtime/auth.mintDevicePrincipal) is the sole sanctioned consumer.
	PrincipalKindClaimDevice PrincipalKindClaim = "device"
)

// IsValid reports whether the claim value is one of the known enum values.
// Mirrors TokenIntent.IsValid — the verifier rejects anything else fail-closed.
func (k PrincipalKindClaim) IsValid() bool {
	switch k {
	case PrincipalKindClaimUser, PrincipalKindClaimDevice:
		return true
	}
	return false
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
	// TenantID is the "tenant_id" claim identifying the tenant isolation
	// boundary the subject belongs to. Empty in single-tenant deployments (no
	// tenant claim issued). When non-empty it MUST be a valid UUID; the
	// runtime/auth JWTVerifier.VerifyIntent validates and canonicalizes it via
	// pkg/tenant.ParseTenantID before returning Claims (a malformed tenant claim
	// fails closed), so consumers may treat a non-empty value as a canonical
	// lowercase UUID. Typed string (not pkg/tenant.TenantID) to keep this kernel
	// wire type free of the tenant package's UUID dependency; the runtime
	// boundary owns validation.
	TenantID string
	// PasswordResetRequired indicates that the subject must change their password.
	PasswordResetRequired bool
	// PrincipalKind is the signed "principal_kind" claim marking the token's
	// principal kind. Empty (claim absent) = user; "device" = a device bearer
	// token. Validated at JWTVerifier.VerifyIntent (unknown → fail closed); the
	// device-principal issuer reads it to mint a PrincipalDevice. See
	// PrincipalKindClaim.
	PrincipalKind PrincipalKindClaim
	// JTI is the JWT ID claim ("jti"), a unique identifier for the token.
	// Empty string when the claim is absent.
	JTI string
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

// ReplaySafe reports whether a nonce store of this kind provides adequate
// service-token replay defense for the given topology requirement.
// requireDistributed is true for real multi-pod deployments, where a
// single-process (in-memory) store cannot coordinate replay defense across pods.
//
// This is the single source of truth for "which NonceStoreKind is replay-safe".
// Both composition.SharedDeps.validateProductionNonceStore (config-time, on the
// declared SharedDeps.NonceStore, with rich diagnostics) and runtime/bootstrap's
// phase0 auth-plan check (on the store that ACTUALLY guards the listener) gate on
// this one predicate so the two enforcement points cannot drift. The noop
// sentinel and any unrecognized kind are never replay-safe — fail-closed
// (#1410 review F2/F1).
func (k NonceStoreKind) ReplaySafe(requireDistributed bool) bool {
	switch k {
	case NonceStoreKindDistributed:
		// Coordinates replay defense across pods; safe for any topology.
		return true
	case NonceStoreKindInMemory:
		// Single-process state: sufficient only when the deployment is not
		// multi-pod (single-pod acknowledged via Topology.SinglePodReplayProtection).
		return !requireDistributed
	default:
		// NonceStoreKindNoop (no replay defense) and any unrecognized kind
		// (cannot be proven safe) are rejected fail-closed.
		return false
	}
}

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

// ServiceKeyring supplies per-cell HMAC subkeys for service-token sign/verify.
// This is the kernel projection of the runtime/auth keyrings: MasterDerivedKeyring
// (monolith — one master, per-cell subkeys derived in-process) and
// ProvisionedKeyring (split — master-absent, only this process's own signing
// subkey + its declared callers' verify subkeys). Both satisfy it structurally.
//
// Per-cell isolation (#2153): a token signed as cell X is keyed by an HKDF subkey
// bound to X (HKDF(parent, X)). A process that does not hold X's subkey cannot
// forge X's caller identity — in split, a compromised cell holds neither the
// master nor other cells' subkeys, so cross-cell forgery is cryptographically
// fail-closed. The Hard property requires master ABSENCE at the cell; HKDF over a
// still-shared master (monolith) provides no isolation and is documented as such.
//
// SigningSecrets/VerifySecrets return ordered subkeys (current first, then
// previous) so master/key rotation still verifies in-flight tokens.
type ServiceKeyring interface {
	// SigningSecrets returns the subkeys used to sign tokens as ownCell. The
	// returned slice is ordered: index 0 MUST be the current signing key (the one
	// used to sign new tokens); any previous-generation keys follow at index ≥1.
	// Returns an error if this process is not authorized to sign as ownCell
	// (split: ownCell != this process's cell identity).
	SigningSecrets(ownCell string) ([][]byte, error)
	// VerifySecrets returns the subkeys used to verify a token claiming
	// callerCell (current first, then previous). Returns an error if callerCell
	// is not in this process's authorized caller set (split least-privilege —
	// fail-closed, not a silent empty set).
	VerifySecrets(callerCell string) ([][]byte, error)
	// Validate reports whether the key material is well-formed (every subkey is
	// at least MinHMACKeyBytes). Called once at AuthServiceToken construction.
	Validate() error
}

// AuthProvider is an optional cell-level interface that exposes an
// IntentTokenVerifier for runtime authentication. AuthJWTFromAssembly walks
// the assembly's CellIDs in deterministic order during bootstrap phase4 and
// promotes the unique implementer's verifier; zero, multiple, or nil
// verifiers are rejected with a startup error.
type AuthProvider interface {
	TokenVerifier() IntentTokenVerifier
}
