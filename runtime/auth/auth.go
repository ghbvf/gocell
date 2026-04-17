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
	// Extra holds additional claims not covered by the standard fields.
	Extra map[string]any
}

// TokenVerifier verifies an authentication token and returns the decoded claims.
// Implementations live in cells/access-core (e.g., JWT, opaque token).
type TokenVerifier interface {
	// Verify validates the token string and returns claims on success.
	Verify(ctx context.Context, token string) (Claims, error)
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
