package identitymanage

// Local authz test stubs for identitymanage handler tests.
//
// accesscoretest imports the accesscore cell (and this slice), so it cannot be
// imported back from any package-internal accesscore test without an import cycle;
// every accesscore slice + cell_test therefore defines its own local
// action-capturing Authorizer. Verdict construction (authz.Allow/Deny) stays in
// these _test.go files per AUTHZ-DECISION-ALLOW-DENY-CALLER-01 (PR-10c #1348).

import (
	"context"

	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/runtime/auth"
)

// capturingAuthorizer is a test-only auth.Authorizer that returns a fixed
// Decision and records the action of the last Authorize call.
type capturingAuthorizer struct {
	decision  authz.Decision
	err       error
	gotAction string
}

func (c *capturingAuthorizer) Authorize(_ context.Context, _, _, action string) (authz.Decision, error) {
	c.gotAction = action
	return c.decision, c.err
}

// allowAuthorizer returns a capturingAuthorizer that grants every request.
func allowAuthorizer() *capturingAuthorizer {
	dec, err := authz.Allow(authz.Obligations{})
	if err != nil {
		panic("test allowAuthorizer: authz.Allow: " + err.Error())
	}
	return &capturingAuthorizer{decision: dec}
}

// withAllowAuthorizer wraps ctx with an allow-all Authorizer.
func withAllowAuthorizer(ctx context.Context) context.Context {
	return auth.WithAuthorizer(ctx, allowAuthorizer())
}

// withDenyAuthorizer wraps ctx with a deny-all Authorizer (PDP-deny → 403 path).
func withDenyAuthorizer(ctx context.Context) context.Context {
	return auth.WithAuthorizer(ctx, &capturingAuthorizer{decision: authz.Deny("test: denied")})
}
