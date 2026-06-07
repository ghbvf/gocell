// ref: go-kratos/kratos middleware/auth/auth.go — auth middleware pattern
// Adopted: middleware wrapping pattern, Claims extraction from context.
// Deviated: separate TokenVerifier and Authorizer interfaces (GoCell splits
// authn from authz); no dependency on specific JWT library at this layer.
package auth

import (
	"context"
	"crypto/rsa"

	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/pkg/authz"
)

// TestServiceContext creates a context carrying a service Principal with the
// given callerCell for use in handler/mount tests. Follows the net/http/httptest
// naming pattern. Use this instead of TestContext for tests that exercise
// internal endpoints protected by RequireCallerCell.
//
// See also: TestContext for user-principal tests.
func TestServiceContext(callerCell string) context.Context {
	return WithPrincipal(context.Background(), &Principal{
		Kind:         PrincipalService,
		CallerCellID: callerCell,
		AuthMethod:   "test_service",
	})
}

// TokenIntent is a type alias of kauth.TokenIntent so that runtime/auth and
// kernel/cell share a single canonical type without conversion at package
// boundaries. All existing code that references kauth.TokenIntent continues
// to compile without modification.
//
// ref: RFC 9068 §2.1 (typ: at+jwt), RFC 8725 §3.11 (token confusion defense)
// ref: AWS Cognito token_use claim ("access"/"id"), Keycloak TokenUtil.java
type TokenIntent = kauth.TokenIntent

const (
	// TokenIntentAccess marks a short-lived credential for calling business
	// endpoints. Verifier rejects any access token replayed at /auth/refresh.
	TokenIntentAccess = kauth.TokenIntentAccess
)

// Claims is a type alias of kauth.Claims so that runtime/auth and kernel/cell
// share a single canonical struct without conversion at package boundaries.
// All existing code that references kauth.Claims continues to compile.
type Claims = kauth.Claims

// IntentTokenVerifier is a type alias of kauth.IntentTokenVerifier so that
// runtime/auth and kernel/cell share a single canonical interface without
// conversion at package boundaries. All existing code that references
// kauth.IntentTokenVerifier continues to compile without modification.
//
// This is the only verification interface in GoCell. Every production
// verification path must declare the expected intent to prevent token-confusion
// attacks (RFC 8725 §3.11).
type IntentTokenVerifier = kauth.IntentTokenVerifier

// Authorizer is the PDP (policy decision point) contract: it evaluates whether
// the subject may perform action on resource and returns a sealed authz.Decision
// (effect + obligations). Implementations may use ABAC, RBAC, or external policy
// engines; the production implementation is the accesscore authorizationdecide
// ABAC engine.
//
// Fail-closed contract: when err != nil the returned Decision is ALWAYS non-Allow
// (the zero authz.Decision{} has IsAllow()==false), so a caller may treat any
// error as a deny and never has to inspect the Decision on the error path. The
// error's errcode Kind classifies the failure (e.g. KindUnavailable when the
// policy store is unreachable → HTTP 503; KindPermissionDenied when the request
// carries no tenant scope). When err == nil the Decision is the policy verdict.
//
// PR-7 (#1345) note for wiring (PR-10): as of the ABAC engine landing, the
// subject/resource/action parameters do NOT themselves gate evaluation — the
// engine evaluates the tenant's policy conditions against the authenticated
// principal's attributes (from ctx) plus environment attributes. subject is
// carried for observability; resource/action are reserved for PR-9 resource-
// attribute lookup. Do not assume coarse-grained (subject,resource,action)
// matching is enforced yet. Decision.Reason() carries a deny diagnostic; the
// current PEP (middleware) does not yet surface it — wire it into deny logs when
// connecting business endpoints.
type Authorizer interface {
	Authorize(ctx context.Context, subject, resource, action string) (authz.Decision, error)
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
