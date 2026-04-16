package auth

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// RequireSelfOrRole checks that the authenticated subject matches targetID
// or holds one of the specified bypass roles. Returns nil on success.
//
// ref: go-kratos/kratos middleware/auth/auth.go — adopted: subject-from-context
// pattern; deviated: combined self+role check instead of separate authz middleware.
//
// Errors:
//   - ErrAuthUnauthorized: no subject in context (auth middleware did not run)
//   - ErrAuthForbidden: subject does not match targetID and lacks bypass roles
func RequireSelfOrRole(ctx context.Context, targetID string, bypassRoles ...string) error {
	subject, ok := ctxkeys.SubjectFrom(ctx)
	if !ok || subject == "" {
		return errcode.New(errcode.ErrAuthUnauthorized, "authentication required")
	}

	if targetID == "" {
		slog.Warn("authz: RequireSelfOrRole called with empty targetID",
			slog.String("subject", subject))
	}

	if targetID != "" && subject == targetID {
		return nil
	}

	if hasAnyRole(ctx, bypassRoles) {
		return nil
	}

	return errcode.New(errcode.ErrAuthForbidden, "access denied")
}

// hasAnyRole checks whether the authenticated Claims in ctx carry at least
// one of the specified roles. Returns false when roles is empty, Claims are
// absent, or no role matches.
func hasAnyRole(ctx context.Context, roles []string) bool {
	if len(roles) == 0 {
		return false
	}
	claims, ok := ClaimsFrom(ctx)
	if !ok {
		return false
	}
	roleSet := make(map[string]bool, len(roles))
	for _, r := range roles {
		roleSet[r] = true
	}
	for _, r := range claims.Roles {
		if roleSet[r] {
			return true
		}
	}
	return false
}

// RequireAnyRole checks that the authenticated subject holds at least one of
// the specified roles. Returns nil on success.
//
// Use this instead of RequireSelfOrRole for admin-only endpoints where there
// is no target resource owner to compare against.
//
// Calling with zero roles always returns ErrAuthForbidden (no role can match).
//
// Errors:
//   - ErrAuthUnauthorized: no subject in context (auth middleware did not run)
//   - ErrAuthForbidden: subject does not hold any of the required roles
func RequireAnyRole(ctx context.Context, roles ...string) error {
	subject, ok := ctxkeys.SubjectFrom(ctx)
	if !ok || subject == "" {
		return errcode.New(errcode.ErrAuthUnauthorized, "authentication required")
	}

	if hasAnyRole(ctx, roles) {
		return nil
	}

	return errcode.New(errcode.ErrAuthForbidden, "access denied")
}

// TestContext creates a context carrying the given subject and roles for use
// in handler tests across cells/. Follows the net/http/httptest naming pattern.
func TestContext(subject string, roles []string) context.Context {
	ctx := ctxkeys.WithSubject(context.Background(), subject)
	return WithClaims(ctx, Claims{Subject: subject, Roles: roles})
}
