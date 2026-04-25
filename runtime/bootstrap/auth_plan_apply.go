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
//   - err:        non-nil when a plan is not recognised (defensive; sealed interface
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
				return nil, nil, "", errcode.New(errcode.ErrCellInvalidConfig,
					fmt.Sprintf("listener %q: AuthJWTFromAssembly verifier not resolved; "+
						"phase4 (runAuthPlanValidateHooks) must complete before applyListenerAuthChain",
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
				auth.WithServiceTokenNonceStore(p.Store),
			))

		default:
			// Sealed interface: this branch is theoretically unreachable.
			return nil, nil, "", errcode.New(errcode.ErrCellInvalidConfig,
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
			// Write back the resolved verifier into the chain element's atomic.Pointer.
			// p was already extracted via type assertion above; SetResolved writes
			// through the internal atomic.Pointer which is shared with the original.
			p.SetResolved(v)
			// Also update cfg so subsequent reads see the resolved verifier.
			b.listenerConfigs[ref] = cfg
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
		return nil, errcode.New(errcode.ErrCellInvalidConfig,
			"bootstrap: AuthJWTFromAssembly.Assembly is nil; use cell.NewAuthJWTFromAssembly(asm)")
	}
	var (
		found   auth.IntentTokenVerifier
		foundID string
	)
	for _, id := range asm.CellIDs() {
		// AssemblyRef is a minimal interface; assemblyWithCell adds Cell(id).
		// Bootstrap bridges the gap via asmCellLookup.
		ap, ok := asmCellLookup(asm, id)
		if !ok {
			continue
		}
		// cell.AuthProvider.TokenVerifier() returns cell.IntentTokenVerifier.
		// auth.IntentTokenVerifier is a type alias of cell.IntentTokenVerifier
		// (F6), so the assignment is direct with no runtime conversion needed.
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

// assemblyWithCell is the internal interface needed by discoverAuthVerifierFromAssembly
// to look up cells by ID. *assembly.CoreAssembly satisfies this.
type assemblyWithCell interface {
	cell.AssemblyRef
	Cell(id string) cell.Cell
}

// asmCellLookup type-asserts asm to assemblyWithCell and looks up a cell.AuthProvider.
// Returns (nil, false) if asm doesn't have the Cell(id) method or the cell doesn't
// implement cell.AuthProvider.
func asmCellLookup(asm cell.AssemblyRef, id string) (cell.AuthProvider, bool) {
	awc, ok := asm.(assemblyWithCell)
	if !ok {
		return nil, false
	}
	ap, ok := awc.Cell(id).(cell.AuthProvider)
	return ap, ok
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
				httputil.WriteError(r.Context(), w, http.StatusUnauthorized,
					"ERR_AUTH_MTLS_REQUIRED", "mTLS client certificate required")
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
