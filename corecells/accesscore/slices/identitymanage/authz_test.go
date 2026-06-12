package identitymanage

// Local authz test stub for identitymanage handler tests.
//
// accesscoretest imports the accesscore cell (and this slice), so it cannot be
// imported back from any package-internal accesscore test without an import cycle;
// every accesscore slice + cell_test therefore defines its own local Authorizer.
// Verdict construction (authz.Allow) stays in this _test.go per
// AUTHZ-DECISION-ALLOW-DENY-CALLER-01 (PR-10c #1348). Success paths inject an allow
// Authorizer (auth.RequirePermission fails closed without one); 403 paths use the
// no-Authorizer fail-closed branch, and the PDP-deny + action-pin spectrum is
// covered at the cell level by TestAccessCore_ProductionAuthGateLock.

import (
	"context"

	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/runtime/auth"
)

// fixedAuthorizer is a test-only auth.Authorizer that returns a fixed Decision.
type fixedAuthorizer struct {
	decision authz.Decision
}

func (a fixedAuthorizer) Authorize(_ context.Context, _, _, _ string) (authz.Decision, error) {
	return a.decision, nil
}

// withAllowAuthorizer wraps ctx with an allow-all Authorizer (the success-path PDP).
func withAllowAuthorizer(ctx context.Context) context.Context {
	dec, err := authz.Allow(authz.Obligations{})
	if err != nil {
		panic("test withAllowAuthorizer: authz.Allow: " + err.Error())
	}
	return auth.WithAuthorizer(ctx, fixedAuthorizer{decision: dec})
}
