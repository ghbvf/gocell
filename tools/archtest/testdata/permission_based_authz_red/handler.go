// Package permission_based_authz_red is a synthetic RED fixture for the
// PERMISSION-BASED-AUTHZ-01 archtest. It exercises both arms of the rule:
//   - registerRoutes calls auth.AnyRole (the role-literal helper gate ban).
//   - principalRoleBranch hand-rolls a (*auth.Principal).HasRole authorization
//     branch (the HasRole semantic-equivalence arm).
//
// It also carries a GREEN control (domainHasRole) — a same-named HasRole method on
// a non-Principal receiver — that the type-aware HasRole scan must NOT flag.
//
// DO NOT use this package in production code.
package permission_based_authz_red

import (
	"context"
	"net/http"

	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// registerRoutes is a RED fixture: it calls auth.AnyRole in a business handler
// context not covered by the PERMISSION-BASED-AUTHZ-01 allowlist.
// VIOLATION: role-literal authorization gate in a business handler.
func registerRoutes(mux *http.ServeMux) {
	policy := auth.AnyRole(auth.RoleAdmin) // VIOLATION: role-literal gate
	_ = policy
}

// principalRoleBranch is a RED fixture for the HasRole arm: a business handler that
// authorizes by branching on (*auth.Principal).HasRole instead of the ABAC PDP.
// VIOLATION: hand-rolled principal role-authorization branch.
func principalRoleBranch(ctx context.Context) bool {
	p, ok := auth.FromContext(ctx)
	if !ok || p == nil {
		return false
	}
	return p.HasRole(auth.RoleAdmin) // VIOLATION: hand-rolled principal role branch
}

// domainHasRole is a GREEN control: a same-named HasRole method on a NON-Principal
// receiver (mirrors the rbaccheck domain service). The type-aware scan must spare a
// call to it — only (*auth.Principal).HasRole is a caller-authorization branch.
type domainHasRole struct{}

func (domainHasRole) HasRole(_ context.Context, _, _ string) (bool, error) { return false, nil }

func callDomainHasRole(s domainHasRole) {
	_, _ = s.HasRole(context.Background(), "u", "admin") // GREEN: not a Principal method
}
