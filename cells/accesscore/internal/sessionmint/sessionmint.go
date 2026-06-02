// Package sessionmint centralizes access-JWT issuance so that login,
// IssueForUser (change-password flow), and refresh share a single fail-closed
// "fetch roles → sign access" pipeline. Opaque refresh tokens are issued by
// runtime/auth/refresh.Store directly from the slice layer, so sessionmint
// no longer deals with refresh tokens at all.
//
// Fail-closed contract: if the RoleRepository cannot resolve a user's roles
// (infrastructure fault), MintAccess returns ErrAuthRoleFetchFailed so the
// caller aborts the in-flight authn action instead of silently issuing a
// token with empty roles — an outcome that looks like a successful
// authentication but strips every RBAC capability.
//
// ref: kubernetes/apiserver pkg/authentication/request/union/union.go
// (FailOnError: credential error short-circuits the chain, never fallthrough)
// ref: kratos middleware/auth/jwt — claim parse failure aborts, never issues
// a "token without claims".
package sessionmint

import (
	"context"
	"fmt"
	"time"

	kauth "github.com/ghbvf/gocell/kernel/auth"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/runtime/auth"
)

// TokenIssuer is the minimal surface MintAccess needs from a JWT issuer.
// Exposing the interface keeps sessionmint unit-testable with a stub issuer.
// Production code passes in *auth.JWTIssuer directly (it satisfies this
// interface by method set).
type TokenIssuer interface {
	Issue(intent kauth.TokenIntent, subject string, opts auth.IssueOptions) (string, error)
}

// Deps injects the collaborators MintAccess needs.
type Deps struct {
	// Issuer signs the access JWT. Required.
	Issuer TokenIssuer
	// RoleRepo resolves the user's current role names. Required.
	RoleRepo ports.RoleRepository
}

// Request is the per-call input for a MintAccess.
type Request struct {
	UserID                string
	SessionID             string
	PasswordResetRequired bool
	// TenantID scopes the role lookup to the correct tenant.
	// Required for GetByUserID (RoleRepository.GetByUserID takes tenant param).
	TenantID tenant.TenantID
}

// Result is the MintAccess output.
//
// ExpiresAt is sampled from Deps.Clk (or clock.Real()) at MintAccess entry; the
// JWT's own exp claim is stamped independently inside the issuer a moment
// later. Treat Result.ExpiresAt as the business-layer expiry (used for Session
// persistence) — the authoritative wire value is the JWT exp claim.
//
// JTI is the per-token unique identifier embedded in the JWT `jti` claim
// (RFC 9068 §2.2.4). It is generated fresh inside MintAccess so each access
// token — including post-rotation tokens minted by Refresh — carries its own
// jti, satisfying ADR-credential D1's per-token uniqueness invariant.
type Result struct {
	AccessToken string
	Roles       []string
	ExpiresAt   time.Time
	JTI         string
}

// MintAccess fetches the user's role names and signs the access JWT. Role-
// fetch failure propagates as ErrAuthRoleFetchFailed (HTTP 500) so the caller
// aborts login / refresh / IssueForUser rather than issue an empty-role token.
//
// The jti claim is a fresh UUIDv4 per token (RFC 9068 §2.2.4); collision
// probability is negligible and uuid.NewString never errors. Carrying jti
// alongside sid matches ADR-credential D1 so observability + revocation
// diagnostics can trace individual tokens. Result.JTI is returned for callers
// that persist the value (e.g. session row fingerprint when FingerprintJTIRef
// is in use). The authz_epoch claim has been removed from the JWT in S4d —
// epoch provenance is now stored exclusively in the session/refresh rows.
func MintAccess(ctx context.Context, clk clock.Clock, deps Deps, req Request) (Result, error) {
	clock.MustHaveClock(clk, "sessionmint.MintAccess")
	roles, err := fetchRoleNames(ctx, deps.RoleRepo, req.TenantID, req.UserID)
	if err != nil {
		return Result{}, errcode.Wrap(errcode.KindInternal, errcode.ErrAuthRoleFetchFailed,
			"sessionmint: fetch roles", err,
			errcode.WithCategory(errcode.CategoryInfra))
	}

	expiresAt := clk.Now().Add(auth.DefaultAccessTokenTTL)

	jti := uuid.NewString()
	access, err := deps.Issuer.Issue(kauth.TokenIntentAccess, req.UserID, auth.IssueOptions{
		Roles:                 roles,
		SessionID:             req.SessionID,
		PasswordResetRequired: req.PasswordResetRequired,
		JTI:                   jti,
	})
	if err != nil {
		return Result{}, fmt.Errorf("sessionmint: issue access token: %w", err)
	}

	return Result{
		AccessToken: access,
		Roles:       roles,
		ExpiresAt:   expiresAt,
		JTI:         jti,
	}, nil
}

// fetchRoleNames resolves role names for userID within the given tenant.
// A nil slice (user has no roles) is a valid state; MintAccess signs a token
// with empty roles. Only a repo error triggers fail-closed.
func fetchRoleNames(ctx context.Context, repo ports.RoleRepository, tid tenant.TenantID, userID string) ([]string, error) {
	roles, err := repo.GetByUserID(ctx, tid, userID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.Name)
	}
	return names, nil
}
