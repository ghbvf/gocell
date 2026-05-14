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

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// TokenIssuer is the minimal surface MintAccess needs from a JWT issuer.
// Exposing the interface keeps sessionmint unit-testable with a stub issuer.
// Production code passes in *auth.JWTIssuer directly (it satisfies this
// interface by method set).
type TokenIssuer interface {
	Issue(intent auth.TokenIntent, subject string, opts auth.IssueOptions) (string, error)
}

// Deps injects the collaborators MintAccess needs.
type Deps struct {
	// Issuer signs the access JWT. Required.
	Issuer TokenIssuer
	// RoleRepo resolves the user's current role names. Required.
	RoleRepo ports.RoleRepository
	// Clk supplies the current time. Required; MustHaveClock panics if nil.
	Clk clock.Clock
}

// Request is the per-call input for a MintAccess.
type Request struct {
	UserID                string
	SessionID             string
	PasswordResetRequired bool
	// AuthzEpoch is the user's current authz_epoch value written into the
	// "authz_epoch" JWT claim. It must reflect the epoch at the time of
	// issuance — typically the value returned by UserRepository.BumpAuthzEpoch
	// (or the existing epoch when no credential invalidation occurred).
	//
	// F18 trade-off: ChangePassword calls IssueForUser after the tx commit;
	// the new token therefore carries the post-commit epoch naturally, without
	// a second BumpAuthzEpoch call.
	AuthzEpoch int64
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
// alongside sid/authz_epoch matches ADR-credential D1 (access JWT carries
// {sid, jti, authz_epoch}) so observability + revocation diagnostics can
// trace individual tokens. Result.JTI is returned for callers that persist
// the value (e.g. session row fingerprint when FingerprintJTIRef is in use).
func MintAccess(ctx context.Context, deps Deps, req Request) (Result, error) {
	roles, err := fetchRoleNames(ctx, deps.RoleRepo, req.UserID)
	if err != nil {
		return Result{}, errcode.Wrap(errcode.KindInternal, errcode.ErrAuthRoleFetchFailed,
			"sessionmint: fetch roles", err,
			errcode.WithCategory(errcode.CategoryInfra))
	}

	clk := deps.Clk
	clock.MustHaveClock(clk, "sessionmint.MintAccess")
	expiresAt := clk.Now().Add(auth.DefaultAccessTokenTTL)

	jti := uuid.NewString()
	access, err := deps.Issuer.Issue(auth.TokenIntentAccess, req.UserID, auth.IssueOptions{
		Roles:                 roles,
		SessionID:             req.SessionID,
		PasswordResetRequired: req.PasswordResetRequired,
		AuthzEpoch:            req.AuthzEpoch,
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

// fetchRoleNames resolves role names for userID. A nil slice (user has no
// roles) is a valid state; MintAccess signs a token with empty roles. Only a
// repo error triggers fail-closed.
func fetchRoleNames(ctx context.Context, repo ports.RoleRepository, userID string) ([]string, error) {
	roles, err := repo.GetByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.Name)
	}
	return names, nil
}
