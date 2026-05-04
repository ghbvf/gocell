// ref: go-kratos/kratos middleware/auth/auth.go — auth middleware pattern
// Adopted: middleware wrapping pattern, Claims extraction from context.
// Deviated: separate TokenVerifier and Authorizer interfaces (GoCell splits
// authn from authz); no dependency on specific JWT library at this layer.
package auth

import (
	"context"
	"crypto/rsa"

	"github.com/ghbvf/gocell/kernel/cell"
)

// TestServiceContext creates a context carrying a service Principal with the
// given callerCell for use in handler/mount tests. Follows the net/http/httptest
// naming pattern. Use this instead of TestContext for tests that exercise
// internal endpoints protected by RequireCallerCell.
func TestServiceContext(callerCell string) context.Context {
	return WithPrincipal(context.Background(), &Principal{
		Kind:         PrincipalService,
		CallerCellID: callerCell,
		AuthMethod:   "test_service",
	})
}

// TokenIntent is a type alias of cell.TokenIntent so that runtime/auth and
// kernel/cell share a single canonical type without conversion at package
// boundaries. All existing code that references auth.TokenIntent continues
// to compile without modification.
//
// ref: RFC 9068 §2.1 (typ: at+jwt), RFC 8725 §3.11 (token confusion defense)
// ref: AWS Cognito token_use claim ("access"/"id"), Keycloak TokenUtil.java
type TokenIntent = cell.TokenIntent

const (
	// TokenIntentAccess marks a short-lived credential for calling business
	// endpoints. Verifier rejects any access token replayed at /auth/refresh.
	TokenIntentAccess = cell.TokenIntentAccess
)

// Claims is a type alias of cell.Claims so that runtime/auth and kernel/cell
// share a single canonical struct without conversion at package boundaries.
// All existing code that references auth.Claims continues to compile.
type Claims = cell.Claims

// IntentTokenVerifier is a type alias of cell.IntentTokenVerifier so that
// runtime/auth and kernel/cell share a single canonical interface without
// conversion at package boundaries. All existing code that references
// auth.IntentTokenVerifier continues to compile without modification.
//
// This is the only verification interface in GoCell. Every production
// verification path must declare the expected intent to prevent token-confusion
// attacks (RFC 8725 §3.11).
type IntentTokenVerifier = cell.IntentTokenVerifier

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
