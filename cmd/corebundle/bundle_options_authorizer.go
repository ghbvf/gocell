package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// authorizerProvider is a local structural interface satisfied by any cell that
// exposes an ABAC Authorizer (PDP). AccessCore satisfies this interface via its
// Authorizer() auth.Authorizer method (cell_providers.go:23-26). Defined here to
// avoid importing corecells/ directly, which is forbidden by the
// corebundle-no-cells depguard rule (COMPOSITION-MODULE-API-01, #1085).
//
// AI-robust Grade: Medium — structural duck-type; compile-breaks if AccessCore
// drops Authorizer() auth.Authorizer. Hard-ification path: generate this
// interface from contract.yaml → codegen in PR-13.
//
// Idempotency contract: Authorizer() MUST be idempotent and side-effect-free
// post-Init. lazyAuthorizer may call it concurrently from multiple goroutines
// on the first request (benign TOCTOU: atomic.Pointer is safe, provider must
// return the same value each time after Init completes).
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
// so Authorize is never reachable before the provider's service is ready.
// Bootstrap then calls ResolveAuthorizer at router build (after Init, before any
// listener serves), so a provider that returns nil even after Init (programming
// error) fails the boot there (F8) instead of 503-ing on the first request. The
// nil guard inside Authorize remains as defense-in-depth.
type lazyAuthorizer struct {
	provider authorizerProvider
	// resolved caches the Authorizer after the first successful lookup to avoid
	// repeated calls to provider.Authorizer() on the hot request path.
	resolved atomic.Pointer[auth.Authorizer]
}

// msgLazyAuthorizerNilProvider is the const message for the fail-closed guard
// when the provider returns nil after Init. Required by MESSAGE-CONST-LITERAL-01.
const msgLazyAuthorizerNilProvider = "authorization provider returned nil Authorizer after Init"

// ResolveAuthorizer eagerly resolves the provider's Authorizer and caches it.
// Bootstrap calls it once after all cell Init has run (so provider.Authorizer()
// returns the live service) and BEFORE any listener serves, so a provider that
// yields nil fails the whole boot rather than 503-ing on the first request (F8).
// The request-path nil guard in Authorize remains as defense-in-depth.
//
// It is idempotent: a second call re-resolves and re-stores the same value.
func (l *lazyAuthorizer) ResolveAuthorizer() error {
	a := l.provider.Authorizer()
	if a == nil {
		slog.Error(
			msgLazyAuthorizerNilProvider,
			slog.String("provider_type", authorizerProviderTypeName(l.provider)),
		)
		return errcode.New(
			errcode.KindUnavailable, errcode.ErrServiceUnavailable,
			msgLazyAuthorizerNilProvider,
			errcode.WithInternal(errcode.InternalAttr("provider_type", authorizerProviderTypeName(l.provider))),
		)
	}
	l.resolved.Store(&a)
	return nil
}

// Authorize implements auth.Authorizer.
func (l *lazyAuthorizer) Authorize(ctx context.Context, subject, resource, action string) (authz.Decision, error) {
	if ptr := l.resolved.Load(); ptr != nil {
		return (*ptr).Authorize(ctx, subject, resource, action)
	}
	a := l.provider.Authorizer()
	if a == nil {
		// Provider returned nil after Init — this is a programming error in the
		// cell. Log at Error so operators can diagnose; the %T detail goes to
		// InternalDetail (server-side only, never on wire).
		slog.Error(
			msgLazyAuthorizerNilProvider,
			slog.String("provider_type", authorizerProviderTypeName(l.provider)),
		)
		return authz.Decision{}, errcode.New(
			errcode.KindUnavailable, errcode.ErrServiceUnavailable,
			msgLazyAuthorizerNilProvider,
			errcode.WithInternal(errcode.InternalAttr("provider_type", authorizerProviderTypeName(l.provider))),
		)
	}
	l.resolved.Store(&a)
	return a.Authorize(ctx, subject, resource, action)
}

// authorizerProviderTypeName returns the %T string of p for diagnostic use in
// InternalDetail. Using a helper avoids fmt.Sprintf in the hot path when the
// provider is non-nil (resolved.Load() short-circuits before this function).
func authorizerProviderTypeName(p authorizerProvider) string {
	if p == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%T", p)
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
					"exactly one PDP provider is required (duplicate: cell %T)", c,
			)
		}
		provider = ap
	}
	if provider == nil {
		return nil, fmt.Errorf(
			"primaryAuthorizerOption: no cell implements authorizerProvider " +
				"(Authorizer() auth.Authorizer); PDP wiring is mandatory — " +
				"ensure accesscore is included in the assembly",
		)
	}
	return bootstrap.WithPrimaryAuthorizer(&lazyAuthorizer{provider: provider}), nil
}
