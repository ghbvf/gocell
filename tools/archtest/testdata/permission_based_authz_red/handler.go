// Package permission_based_authz_red is a synthetic RED fixture for the
// PERMISSION-BASED-AUTHZ-01 archtest: it defines a business handler that
// calls auth.AnyRole, which the rule forbids outside the migration allowlist.
// The archtest must detect this as a violation.
//
// DO NOT use this package in production code.
package permission_based_authz_red

import (
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
