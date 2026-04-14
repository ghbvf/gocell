package auth

import (
	"context"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// RequireSelfOrRole checks that the authenticated subject matches targetID
// or holds one of the specified bypass roles. Returns nil on success.
//
// Errors:
//   - ErrAuthUnauthorized: no subject in context (auth middleware did not run)
//   - ErrAuthForbidden: subject does not match targetID and lacks bypass roles
func RequireSelfOrRole(ctx context.Context, targetID string, bypassRoles ...string) error {
	subject, ok := ctxkeys.SubjectFrom(ctx)
	if !ok || subject == "" {
		return errcode.New(errcode.ErrAuthUnauthorized, "authentication required")
	}

	if targetID != "" && subject == targetID {
		return nil
	}

	if len(bypassRoles) > 0 {
		claims, hasClaims := ClaimsFrom(ctx)
		if hasClaims {
			roleSet := make(map[string]bool, len(bypassRoles))
			for _, r := range bypassRoles {
				roleSet[r] = true
			}
			for _, r := range claims.Roles {
				if roleSet[r] {
					return nil
				}
			}
		}
	}

	return errcode.New(errcode.ErrAuthForbidden, "access denied")
}
