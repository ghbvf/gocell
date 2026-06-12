package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// authorizerProvider is the structural interface satisfied by any cell that
// exposes an ABAC Authorizer (PDP) — e.g. accesscore via Authorizer()
// auth.Authorizer. PrimaryAuthorizerOption discovers the provider by this
// duck-type so composition roots do NOT import corecells/ directly (the
// corebundle-no-cells depguard, COMPOSITION-MODULE-API-01 #1085); ssobff and
// corebundlestarter share the same discovery without their own copy.
//
// Idempotency contract: Authorizer() MUST be idempotent and side-effect-free
// post-Init. lazyAuthorizer may call it from ResolveAuthorizer (startup) and the
// first request; both must observe the same value once Init completes.
type authorizerProvider interface {
	Authorizer() auth.Authorizer
}

// msgLazyAuthorizerNilProvider is the const message for the fail-closed guard
// when the provider returns nil after Init. Required by MESSAGE-CONST-LITERAL-01.
const msgLazyAuthorizerNilProvider = "authorization provider returned nil Authorizer after Init"

// lazyAuthorizer is an auth.Authorizer that wraps an authorizerProvider and
// defers the actual Authorizer() lookup until ResolveAuthorizer (bootstrap
// startup) or the first Authorize call. This is necessary because the cell's
// Authorizer() returns nil until cell.Init (bootstrap phase3), which runs AFTER
// the composition root assembles bootstrap options. By the time any HTTP request
// reaches the primary listener, Init has completed and provider.Authorizer()
// returns the live service.
//
// Lifecycle: bootstrap phase3 calls Init before any listener starts, and the
// router-build step calls ResolveAuthorizer (after Init, before serve) so a nil
// provider fails the boot rather than 503-ing on the first request. The Authorize
// nil guard remains as defense-in-depth.
type lazyAuthorizer struct {
	provider authorizerProvider
	// resolved caches the Authorizer after the first successful lookup to avoid
	// repeated calls to provider.Authorizer() on the hot request path.
	resolved atomic.Pointer[auth.Authorizer]
}

var _ auth.Authorizer = (*lazyAuthorizer)(nil)

// ResolveAuthorizer eagerly resolves the provider's Authorizer and caches it.
// Bootstrap calls it once at router build (after all cell Init has run, so
// provider.Authorizer() returns the live service) and BEFORE any listener serves,
// so a provider that yields nil fails the whole boot rather than 503-ing on the
// first request. The request-path nil guard in Authorize remains as
// defense-in-depth. It is idempotent: a second call re-resolves and re-stores.
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
		// Provider returned nil after Init — a programming error in the cell. Log
		// at Error so operators can diagnose; the %T detail goes to InternalDetail
		// (server-side only, never on wire).
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

// PrimaryAuthorizerOption scans cells for exactly one authorizerProvider and
// returns a WithPrimaryAuthorizer option wiring the ABAC PDP into the primary
// listener's request context via a lazyAuthorizer. It is the single shared entry
// for every composition root (corebundle, corebundlestarter, ssobff): each calls
// it with its own assembled cell list, so the PDP-wiring + lazy-resolution +
// fail-fast logic lives in one place rather than being copied per root.
//
// Fail-fast rules (强依赖 fail-fast, per runtime-api.md option 范式):
//   - Zero matching cells → error: PDP wiring is mandatory for any assembly that
//     serves permission-gated business routes (every auditquery-serving assembly).
//     Silently skipping would leave RequirePermission deny-all without operator
//     visibility.
//   - More than one matching cell → error: ambiguous PDP is a misconfiguration.
//
// The authorizerProvider.Authorizer() call is intentionally deferred to startup
// (ResolveAuthorizer) / request time via lazyAuthorizer because the cell's service
// is set during Init(), which runs inside bootstrap (phase3) — after the composition
// root builds these options.
func PrimaryAuthorizerOption(cells []cell.Cell) (Option, error) {
	var provider authorizerProvider
	for _, c := range cells {
		ap, ok := c.(authorizerProvider)
		if !ok {
			continue
		}
		if provider != nil {
			return nil, fmt.Errorf(
				"PrimaryAuthorizerOption: multiple cells implement authorizerProvider; "+
					"exactly one PDP provider is required (duplicate: cell %T)", c,
			)
		}
		provider = ap
	}
	if provider == nil {
		return nil, fmt.Errorf(
			"PrimaryAuthorizerOption: no cell implements authorizerProvider " +
				"(Authorizer() auth.Authorizer); PDP wiring is mandatory — " +
				"ensure accesscore is included in the assembly",
		)
	}
	return WithPrimaryAuthorizer(&lazyAuthorizer{provider: provider}), nil
}
