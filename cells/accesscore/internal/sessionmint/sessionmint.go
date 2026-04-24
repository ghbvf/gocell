// Package sessionmint centralises session-token issuance so that login,
// IssueForUser (change-password flow), and refresh share a single fail-closed
// "fetch roles → sign access → sign refresh" pipeline.
//
// Fail-closed contract: if the RoleRepository cannot resolve a user's roles
// (infrastructure fault), Mint returns ErrAuthRoleFetchFailed so the caller
// aborts the in-flight authn action instead of silently issuing a token with
// empty roles — an outcome that looks like a successful authentication but
// strips every RBAC capability and is indistinguishable from a deliberate
// privilege escalation bypass.
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

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// TokenIssuer is the minimal surface Mint needs from a JWT issuer. Exposing
// the interface (not the concrete *auth.JWTIssuer) keeps sessionmint testable
// with a stub issuer — which lets unit tests cover the access / refresh Issue
// error paths without constructing a broken keyset. Production code passes in
// *auth.JWTIssuer directly (it satisfies this interface by method set).
type TokenIssuer interface {
	Issue(intent auth.TokenIntent, subject string, opts auth.IssueOptions) (string, error)
}

// Deps injects the collaborators Mint needs. Logger is intentionally omitted:
// callers already log success/failure at the business layer with richer
// context (user_id, session_id), so duplicating them here would only produce
// double-log noise.
type Deps struct {
	// Issuer signs the access and refresh JWTs. Required.
	Issuer TokenIssuer
	// RoleRepo resolves the user's current role names. Required.
	RoleRepo ports.RoleRepository
	// Now returns the current time; defaults to time.Now when nil. Injectable
	// so tests can assert deterministic ExpiresAt without clock jitter.
	Now func() time.Time
}

// Request is the per-call input for a session-token mint.
type Request struct {
	UserID                string
	SessionID             string
	PasswordResetRequired bool
}

// Result is the Mint output. Roles is returned so callers can log / audit
// exactly which roles went into the access-token claim without re-querying.
//
// ExpiresAt is sampled from Deps.Now (or time.Now) at Mint entry; the JWT's
// own exp claim is stamped independently inside the issuer a moment later.
// Treat Result.ExpiresAt as the business-layer expiry (used for Session
// persistence) — the authoritative wire value is the JWT exp claim. Under
// any realistic clock jitter the two are within sub-millisecond agreement.
type Result struct {
	AccessToken  string
	RefreshToken string
	Roles        []string
	ExpiresAt    time.Time
}

// Mint fetches the user's role names, then signs access and refresh JWTs.
// Role-fetch failure propagates as ErrAuthRoleFetchFailed (HTTP 500) so the
// caller aborts login/refresh/IssueForUser rather than issue an empty-role
// token. Access-token issuer errors wrap with "issue access token"; refresh
// token errors wrap with "issue refresh token".
func Mint(ctx context.Context, deps Deps, req Request) (Result, error) {
	roles, err := fetchRoleNames(ctx, deps.RoleRepo, req.UserID)
	if err != nil {
		return Result{}, errcode.WrapInfra(errcode.ErrAuthRoleFetchFailed,
			"sessionmint: fetch roles", err)
	}

	now := time.Now
	if deps.Now != nil {
		now = deps.Now
	}
	expiresAt := now().Add(auth.DefaultAccessTokenTTL)

	access, err := deps.Issuer.Issue(auth.TokenIntentAccess, req.UserID, auth.IssueOptions{
		Roles:                 roles,
		SessionID:             req.SessionID,
		PasswordResetRequired: req.PasswordResetRequired,
	})
	if err != nil {
		return Result{}, fmt.Errorf("sessionmint: issue access token: %w", err)
	}

	refresh, err := deps.Issuer.Issue(auth.TokenIntentRefresh, req.UserID, auth.IssueOptions{
		SessionID: req.SessionID,
	})
	if err != nil {
		return Result{}, fmt.Errorf("sessionmint: issue refresh token: %w", err)
	}

	return Result{
		AccessToken:  access,
		RefreshToken: refresh,
		Roles:        roles,
		ExpiresAt:    expiresAt,
	}, nil
}

// fetchRoleNames resolves role names for userID. The repo may return a nil
// slice (user has no roles) — that is a valid state, not an error, and Mint
// will sign a token with empty roles in that case. Only a repo error triggers
// fail-closed.
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
