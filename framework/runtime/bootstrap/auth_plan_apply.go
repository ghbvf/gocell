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
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"sort"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/spiffeid"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/http/middleware"
	"github.com/ghbvf/gocell/framework/runtime/http/router"
)

// Cross-bind wiring message constants — MESSAGE-CONST-LITERAL-01.
const (
	msgCrossBindNoServerCert = "bootstrap: listener combines AuthMTLS + AuthServiceToken but has no server certificate;" +
		" the cross-bind guard needs the server cert's SPIFFE trust domain (set WithListenerTLS with a cell cert)"
	msgCrossBindServerCertNoID = "bootstrap: listener server certificate carries no cell SPIFFE ID" +
		" (URI SAN spiffe://<td>/cell/<cell>); the cross-bind guard cannot derive the expected peer trust domain"
	// msgCrossBindServerCertBadSet 是非法 cell SPIFFE ID 集合（非 canonical SPIFFE URI，或携带
	// 来自 ≥2 个不同 trust domain 的 cell SPIFFE ID）的专用错误消息，与无 cell SPIFFE ID 的情况
	// （msgCrossBindServerCertNoID）区分。
	msgCrossBindServerCertBadSet = "bootstrap: listener server certificate carries an invalid cell SPIFFE ID set" +
		" (a non-canonical SPIFFE URI or cell IDs from more than one trust domain);" +
		" a workload cert must present canonical cell SPIFFE IDs from exactly one trust domain"
)

// kauth.AuthProvider is the kernel-defined interface for auth provider cells.
// Bootstrap uses it instead of a private interface to eliminate the two-definition
// redundancy (G — Architecture A1). Any cell whose TokenVerifier() returns a
// non-nil kauth.IntentTokenVerifier automatically satisfies kauth.AuthProvider
// because kauth.IntentTokenVerifier and kauth.IntentTokenVerifier are structurally
// identical (kauth.TokenIntent = kauth.TokenIntent; kauth.Claims = kauth.Claims).

// applyListenerAuthChain applies all plans in chain to a listener, returning:
//   - mws:        non-JWT middleware functions to install on the listener mux.
//   - routerOpts: router.Options for JWT (WithAuthMiddleware).
//   - describe:   human-readable summary for startup logs.
//   - err:        non-nil when a plan is not recognized (defensive; sealed interface
//     means this branch is theoretically unreachable).
func (b *Bootstrap) applyListenerAuthChain(
	ref cell.ListenerRef,
	chain []kauth.ListenerAuth,
) (mws []func(http.Handler) http.Handler, routerOpts []router.Option, describe string, err error) {
	for _, plan := range chain {
		switch p := plan.(type) {
		case kauth.AuthNone:
			// no-op

		case kauth.AuthJWT:
			authOpts, aerr := b.buildAuthRouterOptions(p.Verifier)
			if aerr != nil {
				return nil, nil, "", aerr
			}
			routerOpts = append(routerOpts, authOpts...)

		case kauth.AuthJWTFromAssembly:
			v := p.ResolvedVerifier()
			if v == nil {
				// phase4 must have run before phase5; this is a programmer error.
				return nil, nil, "", errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
					"listener AuthJWTFromAssembly verifier not resolved; phase ordering violation: phase4 must complete before applyListenerAuthChain",
					errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("listener=%q", ref.String()))))
			}
			authOpts, aerr := b.buildAuthRouterOptions(v)
			if aerr != nil {
				return nil, nil, "", aerr
			}
			routerOpts = append(routerOpts, authOpts...)

		case kauth.AuthMTLS:
			mws = append(mws, middleware.MTLS())

		case kauth.AuthServiceToken:
			mws = append(mws, auth.ServiceTokenMiddleware(
				p.Ring,
				b.clock,
				auth.WithServiceTokenNonceStore(p.Store),
			))

		case kauth.AuthOperator:
			// Operator-credential gate for the admin control-plane. The plan
			// carries kernel-level deps (raw credentials, kernel-projection
			// OperatorRateLimiter, optional observer func); the concrete HTTP
			// middleware is the same per-IP-rate-limit + Basic-Auth +
			// constant-time-compare chain as the per-cell setup/admin endpoint.
			// p.Limiter (kauth.OperatorRateLimiter) and p.OnAuthFail satisfy the
			// runtime/auth parameter types by structural interface/func identity.
			mws = append(mws, auth.NewBootstrapMiddleware(
				auth.BootstrapCredentials{Username: p.Username, Password: p.Password},
				p.Limiter,
				p.OnAuthFail,
			))

		default:
			// Sealed interface: this branch is theoretically unreachable.
			return nil, nil, "", errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
				"unknown AuthPlan type (sealed interface violation)",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("listener=%q type=%T", ref.String(), plan))))
		}
	}
	mws, err = b.appendCrossBindMiddleware(mws, ref, chain)
	if err != nil {
		return nil, nil, "", err
	}
	describe = describeAuthChain(chain)
	return mws, routerOpts, describe, nil
}

// appendCrossBindMiddleware installs the #2263 peer-cell cross-bind guard LAST
// when a listener combines AuthMTLS (transport peer auth) with a service-token
// plan (message-layer caller identity), so it runs after both middlewares —
// binding the client cert's full cell SPIFFE ID to the service-token caller cell.
// Auto-appending at this single AuthPlan→middleware assembly point means an
// internal listener cannot enable mTLS+service-token yet forget the cross-bind.
//
// The expected peer trust domain is the listener's own server-cert trust domain
// (peers must share it), derived here so it is bound to the actual server
// identity with no extra wiring. A server cert without a cell SPIFFE ID fails
// fast. Returns mws unchanged when the combination is absent.
func (b *Bootstrap) appendCrossBindMiddleware(
	mws []func(http.Handler) http.Handler, ref cell.ListenerRef, chain []kauth.ListenerAuth,
) ([]func(http.Handler) http.Handler, error) {
	if !chainContainsAuthMTLS(chain) || !chainContainsServiceToken(chain) {
		return mws, nil
	}
	td, err := serverCertTrustDomain(b.listenerConfigs[ref].tls)
	if err != nil {
		return nil, err
	}
	return append(mws, auth.PeerCellCrossBindMiddleware(td)), nil
}

// chainContainsServiceToken reports whether any plan in the chain is
// AuthServiceToken (companion to chainContainsAuthMTLS).
func chainContainsServiceToken(chain []kauth.ListenerAuth) bool {
	for _, p := range chain {
		if _, ok := p.(kauth.AuthServiceToken); ok {
			return true
		}
	}
	return false
}

// serverCertTrustDomain extracts the SPIFFE trust domain from a listener's server
// certificate (the leaf's cell SPIFFE ID URI SANs). It is the expected peer trust
// domain for the cross-bind guard: a peer must present a cert in the SAME trust
// domain as the server. A multi-cell workload cert carries several cell SPIFFE IDs,
// all sharing one trust domain (#2297). Fails closed if there is no TLS config / no
// server cert / the leaf carries no cell SPIFFE ID / the leaf bridges trust domains
// — a mTLS+service-token listener whose server cert lacks a single cell identity
// trust domain cannot bind peer identities safely (#2263).
//
// phase0 validateAuthPlanMTLSBindings has already ensured an AuthMTLS listener
// has a non-nil TLS config with a client-CA pool, so the nil/empty paths here are
// defense-in-depth.
func serverCertTrustDomain(tlsCfg *tls.Config) (string, error) {
	if tlsCfg == nil || len(tlsCfg.Certificates) == 0 || len(tlsCfg.Certificates[0].Certificate) == 0 {
		return "", errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgCrossBindNoServerCert)
	}
	leaf := tlsCfg.Certificates[0].Leaf
	if leaf == nil {
		parsed, err := x509.ParseCertificate(tlsCfg.Certificates[0].Certificate[0])
		if err != nil {
			return "", errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig,
				msgCrossBindServerCertNoID, err)
		}
		leaf = parsed
	}
	set, err := spiffeid.CellSetFromURIs(leaf.URIs)
	if err != nil {
		// cert 携带了非法 cell SPIFFE ID 集合：非 canonical SPIFFE URI
		// （userinfo/port/query/fragment），或来自 ≥2 个不同 trust domain 的 cell
		// SPIFFE ID。两者都是独立根因——fail closed 并保留精确原因。
		return "", errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgCrossBindServerCertBadSet, err)
	}
	if set.IsEmpty() {
		// cert 不含任何 cell SPIFFE ID URI SAN。
		return "", errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, msgCrossBindServerCertNoID)
	}
	return set.TrustDomain(), nil
}

// runAuthPlanValidateHooks iterates over all listener chains and, for any
// AuthJWTFromAssembly plan, discovers the verifier from the assembly and
// caches it in the plan's atomic pointer. Called during phase4.
func (b *Bootstrap) runAuthPlanValidateHooks() error {
	refs := sortedListenerRefs(b.listenerConfigs)
	for _, ref := range refs {
		cfg := b.listenerConfigs[ref]
		for _, plan := range cfg.authChain {
			p, ok := plan.(kauth.AuthJWTFromAssembly)
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
func discoverAuthVerifierFromAssembly(asm kauth.AssemblyRef) (kauth.IntentTokenVerifier, error) {
	if validation.IsNilInterface(asm) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: AuthJWTFromAssembly.Assembly is nil; use kauth.NewAuthJWTFromAssembly(asm)")
	}
	var (
		found   kauth.IntentTokenVerifier
		foundID string
	)
	for _, id := range asm.CellIDs() {
		// asm.Cell returns nil for unknown IDs; the AuthProvider type
		// assertion then yields ok=false and the cell is skipped.
		ap, ok := asm.Cell(id).(kauth.AuthProvider)
		if !ok {
			continue
		}
		// kauth.AuthProvider.TokenVerifier() returns kauth.IntentTokenVerifier.
		// kauth.IntentTokenVerifier is a Go type alias of kauth.IntentTokenVerifier
		// (runtime/auth/auth.go:56, F6), so the assignment is direct with no
		// runtime conversion needed. validation.IsNilInterface catches both
		// untyped nil and typed-nil verifiers (e.g. `var v *MyVerifier`)
		// before they propagate into router wiring.
		v := ap.TokenVerifier()
		if validation.IsNilInterface(v) {
			return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
				"bootstrap: authProvider cell TokenVerifier() returned nil",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("cell=%q", id))))
		}
		if found != nil {
			return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
				"bootstrap: multiple authProvider cells discovered; keep only one or supply the verifier explicitly via kauth.NewAuthJWT(verifier)",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("first_cell=%q second_cell=%q", foundID, id))))
		}
		found = v
		foundID = id
	}
	if found == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: AuthJWTFromAssembly found no authProvider cell in the assembly; "+
				"register a cell implementing kauth.AuthProvider whose TokenVerifier() returns a non-nil kauth.IntentTokenVerifier, "+
				"or wire the verifier explicitly via kauth.NewAuthJWT(verifier)")
	}
	return found, nil
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
