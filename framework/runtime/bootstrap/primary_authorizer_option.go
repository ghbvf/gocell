package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth"
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

// msgLazyAuthorizerNotSubjectAuthorizer is the const message for the fail-closed
// guard when the resolved Authorizer does not also implement the explicit-subject
// SubjectAuthorizer interface (AuthorizeAs). Required by MESSAGE-CONST-LITERAL-01.
const msgLazyAuthorizerNotSubjectAuthorizer = "authorization provider does not implement SubjectAuthorizer (explicit-subject AuthorizeAs)"

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

var (
	_ auth.Authorizer        = (*lazyAuthorizer)(nil)
	_ auth.SubjectAuthorizer = (*lazyAuthorizer)(nil)
)

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
	a, err := l.currentAuthorizer()
	if err != nil {
		return authz.Decision{}, err
	}
	return a.Authorize(ctx, subject, resource, action)
}

// AuthorizeAs implements auth.SubjectAuthorizer by delegating to the resolved
// PDP's explicit-subject path. The same lazyAuthorizer instance therefore serves
// the HTTP/gRPC ambient-principal gate (Authorize) AND the non-HTTP cert-signing
// reuse seam (AuthorizeAs), resolving and caching the single PDP exactly once.
// This is the ONLY composition-root path that reaches AuthorizeAs: the cert
// adapter (certsigning/pdpauthz) is handed this value, never a forged subject —
// the AUTHZ-AUTHORIZE-AS-CALLER-FUNNEL-01 archtest allowlists this package's
// delegation alongside pdpauthz.
//
// Fail-closed: if the resolved Authorizer does not also implement
// SubjectAuthorizer the call denies (zero Decision) with KindUnavailable rather
// than silently allowing — the explicit-subject path must never widen access.
func (l *lazyAuthorizer) AuthorizeAs(ctx context.Context, subject auth.SubjectDescriptor, resource, action string) (authz.Decision, error) {
	a, err := l.currentAuthorizer()
	if err != nil {
		return authz.Decision{}, err
	}
	sa, ok := a.(auth.SubjectAuthorizer)
	if !ok {
		// The PDP cell exposes an Authorizer that lacks AuthorizeAs — a wiring
		// defect (only the ABAC engine implements SubjectAuthorizer). Log at Error
		// with the %T detail server-side (never on wire) and fail closed.
		slog.Error(
			msgLazyAuthorizerNotSubjectAuthorizer,
			slog.String("provider_type", authorizerProviderTypeName(l.provider)),
		)
		return authz.Decision{}, errcode.New(
			errcode.KindUnavailable, errcode.ErrServiceUnavailable,
			msgLazyAuthorizerNotSubjectAuthorizer,
			errcode.WithInternal(errcode.InternalAttr("provider_type", authorizerProviderTypeName(l.provider))),
		)
	}
	return sa.AuthorizeAs(ctx, subject, resource, action)
}

// currentAuthorizer returns the resolved Authorizer from the cache, or resolves
// it from the provider on first use (caching the result). It returns a
// fail-closed KindUnavailable error when the provider yields nil after Init.
// Shared by Authorize and AuthorizeAs so the resolve/cache/nil-guard logic lives
// in one place.
func (l *lazyAuthorizer) currentAuthorizer() (auth.Authorizer, error) {
	if ptr := l.resolved.Load(); ptr != nil {
		return *ptr, nil
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
		return nil, errcode.New(
			errcode.KindUnavailable, errcode.ErrServiceUnavailable,
			msgLazyAuthorizerNilProvider,
			errcode.WithInternal(errcode.InternalAttr("provider_type", authorizerProviderTypeName(l.provider))),
		)
	}
	l.resolved.Store(&a)
	return a, nil
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
	a, err := AuthorizerFromCells(cells)
	if err != nil {
		return nil, err
	}
	return WithPrimaryAuthorizer(a), nil
}

// AuthorizerFromCells scans cells for exactly one authorizerProvider and returns a
// lazy auth.Authorizer wrapping it (deferred Authorizer() lookup, see
// lazyAuthorizer). It is the discovery+lazy-wrap core shared by PrimaryAuthorizerOption
// (HTTP primary listener) and the gRPC interceptor wiring (interceptor.Deps.Authorizer):
// both need the same single PDP provider resolved from the assembled cell list without
// importing corecells/ (the corebundle-no-cells depguard). The returned authorizer is
// safe to feed BOTH the HTTP WithPrimaryAuthorizer option and the gRPC Deps — sharing one
// instance means the startup ResolveAuthorizer (HTTP router build) resolves it once and the
// gRPC gate observes the same live PDP. Same fail-fast rules as PrimaryAuthorizerOption:
// zero matching cells or more than one is a misconfiguration error.
//
// Callers SHOULD reuse the single returned value for both the HTTP
// WithPrimaryAuthorizer and the gRPC interceptor.Deps.Authorizer wiring points.
// Calling AuthorizerFromCells twice for the same cell list yields two independent
// lazyAuthorizer instances: each resolves its own copy of the PDP and caches it
// independently. This is functionally correct but wastes a resolve call at startup
// and splits the cache — one instance resolved by HTTP router build will not warm
// the gRPC gate's separate instance.
func AuthorizerFromCells(cells []cell.Cell) (auth.Authorizer, error) {
	provider, err := findAuthorizerProvider(cells)
	if err != nil {
		return nil, err
	}
	return &lazyAuthorizer{provider: provider}, nil
}

// findAuthorizerProvider returns the single cell that exposes an ABAC Authorizer,
// erroring if zero or more than one match. Shared discovery for PrimaryAuthorizerOption
// and AuthorizerFromCells so the "exactly one PDP" rule lives in one place.
func findAuthorizerProvider(cells []cell.Cell) (authorizerProvider, error) {
	var provider authorizerProvider
	for _, c := range cells {
		ap, ok := c.(authorizerProvider)
		if !ok {
			continue
		}
		if provider != nil {
			return nil, fmt.Errorf(
				"AuthorizerFromCells: multiple cells implement authorizerProvider; "+
					"exactly one PDP provider is required (duplicate: cell %T)", c,
			)
		}
		provider = ap
	}
	if provider == nil {
		return nil, fmt.Errorf(
			"AuthorizerFromCells: no cell implements authorizerProvider " +
				"(Authorizer() auth.Authorizer); PDP wiring is mandatory — " +
				"ensure accesscore is included in the assembly",
		)
	}
	return provider, nil
}
