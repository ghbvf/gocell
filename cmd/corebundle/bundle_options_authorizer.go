package main

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// authorizerProvider is a local structural interface satisfied by any cell that
// exposes an ABAC Authorizer (PDP). AccessCore satisfies this interface via its
// Authorizer() auth.Authorizer method (cell_providers.go:23-26). Defined here to
// avoid importing corecells/ directly, which is forbidden by the
// corebundle-no-cells depguard rule (COMPOSITION-MODULE-API-01, #1085).
type authorizerProvider interface {
	Authorizer() auth.Authorizer
}

// lazyAuthorizer is an auth.Authorizer that wraps an authorizerProvider and
// defers the actual Authorizer() lookup to the first Authorize call. This is
// necessary because cell.Init() (which constructs the authzSvc inside AccessCore)
// runs during bootstrap phase3, AFTER runtimeOptsFunc has assembled the bootstrap
// options (which is when primaryAuthorizerOption runs). By the time any HTTP
// request reaches the primary listener, Init has completed and provider.Authorizer()
// returns the live service.
//
// Lifecycle guarantee: bootstrap phase3 calls Init before any listener starts,
// so Authorize is never reachable before the provider's service is ready. If a
// provider returns nil even after Init (programming error), Authorize fails closed
// with a clear error rather than panicking.
type lazyAuthorizer struct {
	provider authorizerProvider
	// resolved caches the Authorizer after the first successful lookup to avoid
	// repeated calls to provider.Authorizer() on the hot request path.
	resolved atomic.Pointer[auth.Authorizer]
}

// Authorize implements auth.Authorizer.
func (l *lazyAuthorizer) Authorize(ctx context.Context, subject, resource, action string) (authz.Decision, error) {
	if ptr := l.resolved.Load(); ptr != nil {
		return (*ptr).Authorize(ctx, subject, resource, action)
	}
	a := l.provider.Authorizer()
	if a == nil {
		return authz.Decision{}, fmt.Errorf(
			"lazyAuthorizer: provider %T returned nil Authorizer — Init may not have completed yet", l.provider)
	}
	l.resolved.Store(&a)
	return a.Authorize(ctx, subject, resource, action)
}

var _ auth.Authorizer = (*lazyAuthorizer)(nil)

// primaryAuthorizerOption scans cells for exactly one authorizerProvider and
// returns a bootstrap.WithPrimaryAuthorizer option wiring the ABAC PDP into
// the primary listener's request context via a lazyAuthorizer.
//
// Fail-fast rules (强依赖 fail-fast, per runtime-api.md option 范式):
//   - Zero matching cells → error: PDP wiring is mandatory for PR-10a. Silently
//     skipping would leave RequirePermission deny-all without operator visibility.
//   - More than one matching cell → error: ambiguous PDP is a misconfiguration.
//
// The authorizerProvider.Authorizer() call is intentionally deferred to request
// time via lazyAuthorizer because the cell's authzSvc is set during Init(), which
// runs inside bootstrap (phase3) — after runtimeOptsFunc builds these options.
func primaryAuthorizerOption(cells []cell.Cell) (bootstrap.Option, error) {
	var provider authorizerProvider
	for _, c := range cells {
		ap, ok := c.(authorizerProvider)
		if !ok {
			continue
		}
		if provider != nil {
			return nil, fmt.Errorf(
				"primaryAuthorizerOption: multiple cells implement authorizerProvider; "+
					"exactly one PDP provider is required (duplicate: cell %T)", c)
		}
		provider = ap
	}
	if provider == nil {
		return nil, fmt.Errorf(
			"primaryAuthorizerOption: no cell implements authorizerProvider " +
				"(Authorizer() auth.Authorizer); PDP wiring is mandatory — " +
				"ensure accesscore is included in the assembly")
	}
	return bootstrap.WithPrimaryAuthorizer(&lazyAuthorizer{provider: provider}), nil
}
