//go:build archtest_fixture

// Package withauthorizercallerfixture is a RED fixture for
// AUTH-WITHAUTHORIZER-CALLER-01. It references runtime/auth.WithAuthorizer from a
// package that is NOT on withAuthorizerCallerAllowlist, so the use-based detector
// must flag the reference. It is never imported by production code; it exists only
// so the archtest reverse self-check can prove the detector fires.
//
// Gated behind the archtest_fixture build tag so it is invisible to the
// Production() scan (which would otherwise flag it as an unsanctioned caller) but
// loaded by the Fixture() façade in the reverse self-check.
package withauthorizercallerfixture

import (
	"context"

	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// InjectAuthorizer injects an Authorizer outside the sanctioned composition-root /
// test-wiring sites (RED) — the exact shape of a cell RouteGroup.Middleware that
// would swap the PDP its own RequirePermission gate consults.
func InjectAuthorizer(ctx context.Context, a auth.Authorizer) context.Context {
	return auth.WithAuthorizer(ctx, a)
}
