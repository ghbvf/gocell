// ref: go-kratos/kratos middleware/auth/auth.go — auth middleware pattern
// Adopted: middleware wrapping pattern, Claims extraction from context.
// Deviated: separate TokenVerifier and Authorizer interfaces (GoCell splits
// authn from authz); no dependency on specific JWT library at this layer.
package auth

import (
	"context"
	"crypto/rsa"
	"time"
)

// DefaultJWTAudience is the audience value written by JWTIssuer.Issue and
// expected by JWTVerifier.VerifyIntent in production deployments. Centralised
// here so issuer (sessionlogin/sessionrefresh) and verifier (buildJWTDeps) stay
// in sync without drift.
const DefaultJWTAudience = "gocell"

// TokenIntent distinguishes how a JWT is meant to be used, preventing
// token-confusion attacks where a refresh token is replayed at a business
// endpoint, or an access token is submitted to /auth/refresh.
//
// ref: RFC 9068 §2.1 (typ: at+jwt), RFC 8725 §3.11 (token confusion defense)
// ref: AWS Cognito token_use claim ("access"/"id"), Keycloak TokenUtil.java
type TokenIntent string

const (
	// TokenIntentAccess marks a short-lived credential for calling business
	// endpoints. Verifier rejects any access token replayed at /auth/refresh.
	TokenIntentAccess TokenIntent = "access"
	// TokenIntentRefresh marks a long-lived credential consumed only by
	// /auth/refresh to rotate the session. Verifier rejects any refresh token
	// presented at a business endpoint.
	TokenIntentRefresh TokenIntent = "refresh"
)

// IsValid reports whether the intent is one of the known enum values.
func (t TokenIntent) IsValid() bool {
	return t == TokenIntentAccess || t == TokenIntentRefresh
}

// Claims represents the decoded token claims.
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
	// TokenUse records the intent declared by the JWT's token_use claim (see
	// TokenIntent). Empty when absent — callers that enforce intent must
	// treat empty as fail-closed.
	TokenUse TokenIntent
	// Extra holds additional claims not covered by the standard fields.
	Extra map[string]any
}

// TokenVerifier verifies an authentication token and returns the decoded claims.
// Implementations live in cells/access-core (e.g., JWT, opaque token).
type TokenVerifier interface {
	// Verify validates the token string and returns claims on success.
	Verify(ctx context.Context, token string) (Claims, error)
}

// IntentTokenVerifier extends TokenVerifier with intent-aware validation.
// Callers that know the required token intent (e.g., HTTP middleware expecting
// access tokens, /auth/refresh expecting refresh tokens) should use
// VerifyIntent so the verifier can reject token-confusion attempts with a
// distinct ErrAuthInvalidTokenIntent code.
type IntentTokenVerifier interface {
	TokenVerifier
	// VerifyIntent validates the token and additionally requires that its
	// declared intent (token_use claim + typ header) matches expected.
	// Returns ErrAuthInvalidTokenIntent when the intent does not match, is
	// missing, or header/claim diverge.
	VerifyIntent(ctx context.Context, token string, expected TokenIntent) (Claims, error)
}

// Authorizer checks whether a subject is allowed to perform an action on a resource.
// Implementations may use RBAC, ABAC, or external policy engines.
type Authorizer interface {
	// Authorize returns true if the subject is authorized to perform the
	// given action on the resource.
	Authorize(ctx context.Context, subject, resource, action string) (bool, error)
}

// SigningKeyProvider supplies the active signing key for JWT issuance.
// Implementations must be safe for concurrent use.
//
// *KeySet satisfies this interface.
type SigningKeyProvider interface {
	// SigningKey returns the active RSA private key for signing tokens.
	SigningKey() *rsa.PrivateKey
	// SigningKeyID returns the kid (key identifier) of the active signing key.
	SigningKeyID() string
}

// VerificationKeyStore looks up public keys for JWT verification by kid.
// Implementations must be safe for concurrent use.
//
// *KeySet satisfies this interface.
type VerificationKeyStore interface {
	// PublicKeyByKID returns the public key matching the given kid.
	// Returns an error for unknown or expired kids.
	PublicKeyByKID(kid string) (*rsa.PublicKey, error)
}

// claimsKey is the context key for storing Claims.
type claimsKey struct{}

// WithClaims returns a new context carrying the given Claims.
func WithClaims(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}

// ClaimsFrom extracts Claims from ctx. The boolean indicates presence.
func ClaimsFrom(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(claimsKey{}).(Claims)
	return c, ok
}
