package bootstrap

// auth_plan_apply.go — type-switch dispatcher for AuthPlan → HTTP middleware.
//
// This is the single place in bootstrap that converts a typed AuthPlan into
// concrete HTTP middleware and/or router options. No string-based dispatch
// anywhere in this file.
//
// ref: zeromicro/go-zero rest/engine.go appendAuthHandler@master
//      — typed plan + single assembly point.
// ref: go-kratos/kratos transport/http/server.go
//      — middleware injected at server build time.

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/http/router"
)

// cell.AuthProvider is the kernel-defined interface for auth provider cells.
// Bootstrap uses it instead of a private interface to eliminate the two-definition
// redundancy (G — Architecture A1). Any cell whose TokenVerifier() returns a
// non-nil auth.IntentTokenVerifier automatically satisfies cell.AuthProvider
// because auth.IntentTokenVerifier and cell.IntentTokenVerifier are structurally
// identical (auth.TokenIntent = cell.TokenIntent; auth.Claims = cell.Claims).

// applyListenerAuthChain applies all plans in chain to a listener, returning:
//   - mws:        non-JWT middleware functions to install on the listener mux.
//   - routerOpts: router.Options for JWT (WithAuthMiddleware).
//   - describe:   human-readable summary for startup logs.
//   - err:        non-nil when a plan is not recognized (defensive; sealed interface
//     means this branch is theoretically unreachable).
func (b *Bootstrap) applyListenerAuthChain(
	ref cell.ListenerRef,
	chain []cell.ListenerAuth,
) (mws []func(http.Handler) http.Handler, routerOpts []router.Option, describe string, err error) {
	for _, plan := range chain {
		switch p := plan.(type) {
		case cell.AuthNone:
			// no-op

		case cell.AuthJWT:
			authOpts, aerr := b.buildAuthRouterOptions(p.Verifier)
			if aerr != nil {
				return nil, nil, "", aerr
			}
			routerOpts = append(routerOpts, authOpts...)

		case cell.AuthJWTFromAssembly:
			v := p.ResolvedVerifier()
			if v == nil {
				// phase4 must have run before phase5; this is a programmer error.
				return nil, nil, "", errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
					fmt.Sprintf("listener %q: AuthJWTFromAssembly verifier not resolved; "+
						"phase ordering violation: phase4 must complete before applyListenerAuthChain",
						ref.String()))
			}
			authOpts, aerr := b.buildAuthRouterOptions(v)
			if aerr != nil {
				return nil, nil, "", aerr
			}
			routerOpts = append(routerOpts, authOpts...)

		case cell.AuthMTLS:
			mws = append(mws, mtlsMiddleware())

		case cell.AuthServiceToken:
			mws = append(mws, auth.ServiceTokenMiddleware(
				p.Ring,
				b.clock,
				auth.WithServiceTokenNonceStore(p.Store),
			))

		default:
			// Sealed interface: this branch is theoretically unreachable.
			return nil, nil, "", errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
				fmt.Sprintf("listener %q: unknown AuthPlan type %T (sealed interface violation)",
					ref.String(), plan))
		}
	}
	describe = describeAuthChain(chain)
	return mws, routerOpts, describe, nil
}

// runAuthPlanValidateHooks iterates over all listener chains and, for any
// AuthJWTFromAssembly plan, discovers the verifier from the assembly and
// caches it in the plan's atomic pointer. Called during phase4.
func (b *Bootstrap) runAuthPlanValidateHooks() error {
	refs := sortedListenerRefs(b.listenerConfigs)
	for _, ref := range refs {
		cfg := b.listenerConfigs[ref]
		for _, plan := range cfg.authChain {
			p, ok := plan.(cell.AuthJWTFromAssembly)
			if !ok {
				continue
			}
			v, err := discoverAuthVerifierFromAssembly(p.Assembly)
			if err != nil {
				return fmt.Errorf("bootstrap: listener %q: %w", ref.String(), err)
			}
			// SetResolved writes through the plan's internal *atomic.Pointer,
			// which is shared by every value-copy of this AuthJWTFromAssembly
			// (the pointer is set once at NewAuthJWTFromAssembly time). All
			// later reads via plan.ResolvedVerifier() — including from inside
			// applyListenerAuthChain — observe the new value without any
			// listenerConfigs map write-back.
			p.SetResolved(v)
		}
	}
	return nil
}

// discoverAuthVerifierFromAssembly walks the assembly's cells in deterministic
// order and returns the unique IntentTokenVerifier exposed by an authProvider
// cell. Errors on zero, multiple, or nil verifiers.
//
// Moved from policy_jwt_from_assembly.go; kept bootstrap-private.
func discoverAuthVerifierFromAssembly(asm cell.AssemblyRef) (auth.IntentTokenVerifier, error) {
	if asm == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: AuthJWTFromAssembly.Assembly is nil; use cell.NewAuthJWTFromAssembly(asm)")
	}
	var (
		found   auth.IntentTokenVerifier
		foundID string
	)
	for _, id := range asm.CellIDs() {
		// asm.Cell returns nil for unknown IDs; the AuthProvider type
		// assertion then yields ok=false and the cell is skipped.
		ap, ok := asm.Cell(id).(cell.AuthProvider)
		if !ok {
			continue
		}
		// cell.AuthProvider.TokenVerifier() returns cell.IntentTokenVerifier.
		// auth.IntentTokenVerifier is a Go type alias of cell.IntentTokenVerifier
		// (runtime/auth/auth.go:56, F6), so the assignment is direct with no
		// runtime conversion needed.
		v := ap.TokenVerifier()
		if v == nil {
			return nil, fmt.Errorf(
				"bootstrap: cell %q implements authProvider (cell.AuthProvider) but TokenVerifier() returned nil", id)
		}
		if found != nil {
			return nil, fmt.Errorf(
				"bootstrap: multiple authProvider cells discovered: %q and %q; "+
					"keep only one or supply the verifier explicitly via cell.NewAuthJWT(verifier)",
				foundID, id)
		}
		found = v
		foundID = id
	}
	if found == nil {
		return nil, fmt.Errorf(
			"bootstrap: AuthJWTFromAssembly found no authProvider cell in the assembly; " +
				"register a cell implementing cell.AuthProvider whose TokenVerifier() returns a non-nil auth.IntentTokenVerifier, " +
				"or wire the verifier explicitly via cell.NewAuthJWT(verifier)")
	}
	return found, nil
}

// ─── Middleware factories ─────────────────────────────────────────────────────

// mtlsMiddleware returns the peer-cert-presence guard. The handshake layer
// has already done the chain check (see auth_plan_validate.go phase0), so
// the middleware only asserts that the connection terminated as TLS with at
// least one peer cert.
//
// Moved from policy_mtls.go.
func mtlsMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
				httputil.WriteError(r.Context(), w,
					errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "mTLS client certificate required"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// sortedListenerRefs returns listener refs in deterministic string order.
func sortedListenerRefs(configs map[cell.ListenerRef]listenerConfig) []cell.ListenerRef {
	refs := make([]cell.ListenerRef, 0, len(configs))
	for ref := range configs {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].String() < refs[j].String() })
	return refs
}
