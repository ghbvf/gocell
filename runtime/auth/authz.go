package auth

import (
	"context"
	"slices"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// RequireSelfOrRole checks that the authenticated subject matches targetID
// or holds one of the specified bypass roles. Returns nil on success.
//
// Deprecated: use auth.SelfOr with auth.Secured instead. This internal
// function remains for Policy implementation but should not be called
// directly from handlers.
//
// ref: go-kratos/kratos middleware/auth/auth.go — adopted: subject-from-context
// pattern; deviated: combined self+role check instead of separate authz middleware.
//
// Errors:
//   - ErrAuthUnauthorized: no Principal in context (auth middleware did not run)
//   - ErrAuthForbidden: subject does not match targetID and lacks bypass roles
func RequireSelfOrRole(ctx context.Context, targetID string, bypassRoles ...string) error {
	p, ok := FromContext(ctx)
	if !ok {
		return errcode.New(errcode.ErrAuthUnauthorized, "authentication required")
	}

	if targetID == "" {
		loggerFrom(ctx).Warn("authz: RequireSelfOrRole called with empty targetID",
			"subject", p.Subject)
	}

	if targetID != "" && p.Subject == targetID {
		return nil
	}

	if principalHasAnyRole(p, bypassRoles) {
		return nil
	}

	return errcode.New(errcode.ErrAuthForbidden, "access denied")
}

// principalHasAnyRole checks whether p holds at least one of the given roles.
// Returns false when roles is empty or p is nil.
func principalHasAnyRole(p *Principal, roles []string) bool {
	if p == nil || len(roles) == 0 {
		return false
	}
	return slices.ContainsFunc(roles, p.HasRole)
}

// RequireAnyRole checks that the authenticated subject holds at least one of
// the specified roles. Returns nil on success.
//
// Deprecated: use auth.AnyRole with auth.Secured instead. This internal
// function remains for Policy implementation but should not be called
// directly from handlers.
//
// Use this instead of RequireSelfOrRole for admin-only endpoints where there
// is no target resource owner to compare against.
//
// Calling with zero roles always returns ErrAuthForbidden (no role can match).
//
// Errors:
//   - ErrAuthUnauthorized: no Principal in context (auth middleware did not run)
//   - ErrAuthForbidden: subject does not hold any of the required roles
func RequireAnyRole(ctx context.Context, roles ...string) error {
	p, ok := FromContext(ctx)
	if !ok {
		return errcode.New(errcode.ErrAuthUnauthorized, "authentication required")
	}

	if principalHasAnyRole(p, roles) {
		return nil
	}

	return errcode.New(errcode.ErrAuthForbidden, "access denied")
}

// TestContext creates a context carrying the given subject and roles for use
// in handler tests across cells/. Follows the net/http/httptest naming pattern.
func TestContext(subject string, roles []string) context.Context {
	p := &Principal{
		Kind:    PrincipalUser,
		Subject: subject,
		Roles:   append([]string(nil), roles...),
	}
	return WithPrincipal(context.Background(), p)
}
