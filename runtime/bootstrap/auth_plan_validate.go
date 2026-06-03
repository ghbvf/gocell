package bootstrap

// auth_plan_validate.go — phase0/phase4 typed validation for AuthPlan chains.
//
// Four validation functions are used by bootstrap_phases.go:
//
//   validateAuthJWTFromAssemblyPlans — phase0: AuthJWTFromAssembly must be
//                                      constructor-built with a non-nil Assembly
//                                      matching WithAssembly when registered.
//   validateAuthPlanMTLSBindings   — phase0: any listener with AuthMTLS must
//                                    have ClientAuth + ClientCAs set on tls.Config.
//   validateAuthChainJWTSingleton  — phase0: at most 1 JWT plan in a listener chain,
//                                    and it must be the first element.
//   runAuthPlanValidateHooks       — phase4: call each plan's Validate hook after
//                                    cells are started and verifiers are discovered.
//
// ref: kubernetes/apiserver pkg/server/options/authentication.go — typed dispatch
//      BuiltInAuthenticationOptions + fail-fast startup validation.

import (
	"crypto/tls"
	"fmt"

	"github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/cell"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

const (
	internalListenerPositionFmt    = "listener=%q position=%d"
	internalListenerPositionMinFmt = internalListenerPositionFmt + " got=%d min=%d"
)

// validateAuthJWTFromAssemblyPlans catches malformed AuthJWTFromAssembly
// literals at phase0, then enforces the single-assembly invariant when
// WithAssembly is present. This prevents nil/typed-nil panics, rejected
// constructor bypass, and the silent bug where AuthJWTFromAssembly(asmA) +
// WithAssembly(asmB) would discover auth in asmA while running the rest of
// bootstrap against asmB.
func (b *Bootstrap) validateAuthJWTFromAssemblyPlans() error {
	for ref, cfg := range b.listenerConfigs {
		for i, plan := range cfg.authChain {
			p, ok := plan.(auth.AuthJWTFromAssembly)
			if !ok {
				continue
			}
			if err := b.validateAuthJWTFromAssemblyPlan(ref.String(), i, p); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *Bootstrap) validateAuthJWTFromAssemblyPlan(
	listener string,
	position int,
	p auth.AuthJWTFromAssembly,
) error {
	if validation.IsNilInterface(p.Assembly) {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"AuthJWTFromAssembly Assembly must not be nil; construct it with auth.NewAuthJWTFromAssembly(asm)",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalListenerPositionFmt, listener, position))))
	}
	if !p.IsConstructed() {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"AuthJWTFromAssembly was constructed as a struct literal; "+
				"use auth.NewAuthJWTFromAssembly(asm)",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalListenerPositionFmt, listener, position))))
	}
	if b.assemblyCore == nil {
		return nil
	}
	if p.Assembly != b.assemblyCore {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: AuthJWTFromAssembly carries a different assembly than WithAssembly; "+
				"the composition root must wire the same *assembly.CoreAssembly instance everywhere",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(
				"listener=%q plan_assembly=%q bootstrap_assembly=%q",
				listener, p.Assembly.ID(), b.assemblyCore.ID()))))
	}
	return nil
}

// validateAuthPlanMTLSBindings enforces that any listener using AuthMTLS has
// a *tls.Config with ClientAuth >= VerifyClientCertIfGiven AND a non-nil
// ClientCAs pool. The handshake-layer check (crypto/tls) only runs when these
// are set, so AuthMTLS without proper TLS config is a programming error that
// must fail fast at startup.
//
// PR269 round-3: RouteGroup-level Auth no longer exists; mTLS bindings are
// validated only at listener scope.
func (b *Bootstrap) validateAuthPlanMTLSBindings() error {
	for ref, cfg := range b.listenerConfigs {
		if !chainContainsAuthMTLS(cfg.authChain) {
			continue
		}
		source := fmt.Sprintf("listener %q", ref.String())
		if err := validateMTLSTLSConfig(source, cfg.tls); err != nil {
			return err
		}
	}
	return nil
}

// validateMTLSTLSConfig checks that tlsCfg is non-nil, has ClientAuth >=
// VerifyClientCertIfGiven, and has a non-nil ClientCAs pool.
func validateMTLSTLSConfig(source string, tlsCfg *tls.Config) error {
	if tlsCfg == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: listener uses AuthMTLS without WithListenerTLS; set "+
				"tls.Config.ClientAuth=RequireAndVerifyClientCert and "+
				"ClientCAs=<pool> so the handshake layer enforces the chain",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("source=%s", source))))
	}
	if tlsCfg.ClientAuth < tls.VerifyClientCertIfGiven {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: listener uses AuthMTLS but tls.Config.ClientAuth is "+
				"too permissive; set ClientAuth >= tls.VerifyClientCertIfGiven "+
				"(RequireAndVerifyClientCert recommended)",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("source=%s client_auth=%v", source, tlsCfg.ClientAuth))))
	}
	if tlsCfg.ClientCAs == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: listener uses AuthMTLS but tls.Config.ClientCAs is nil; set ClientCAs to the CA pool the handshake should accept",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("source=%s", source))))
	}
	return nil
}

// validateAuthChainJWTSingleton enforces the JWT-in-chain constraint:
// at most 1 JWT plan (AuthJWT or AuthJWTFromAssembly) is allowed per listener
// chain, and it must be the first element. JWT plans carry their verifier out-of-band
// (via router.WithAuthMiddleware) and must be installed at the router level, not
// as a stacked middleware. Having multiple JWTs or a non-first JWT would cause
// silent drops.
func (b *Bootstrap) validateAuthChainJWTSingleton() error {
	for ref, cfg := range b.listenerConfigs {
		if err := checkJWTSingleton(ref.String(), cfg.authChain); err != nil {
			return err
		}
	}
	return nil
}

// validateAuthNoneExclusive rejects chains that mix AuthNone with real auth
// plans. AuthNone is an explicit no-auth declaration, not a decoration; mixing
// it with guards makes startup logs and reviews ambiguous.
func (b *Bootstrap) validateAuthNoneExclusive() error {
	for ref, cfg := range b.listenerConfigs {
		hasNone := false
		hasGuard := false
		for _, plan := range cfg.authChain {
			if _, ok := plan.(auth.AuthNone); ok {
				hasNone = true
				continue
			}
			hasGuard = true
		}
		if hasNone && hasGuard {
			return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
				"AuthNone cannot be mixed with other ListenerAuth plans; "+
					"use []auth.ListenerAuth{auth.AuthNone{}} only for no-auth "+
					"listeners or remove AuthNone from protected chains",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("listener=%q", ref.String()))))
		}
	}
	return nil
}

// validateAuthServiceTokenPlans catches malformed AuthServiceToken literals at
// phase0. The public constructor already enforces the nil/noop/ring invariants,
// but direct struct literals can otherwise reach phase5 and fail inside HTTP
// middleware assembly rather than at the option boundary.
//
// It additionally closes the #1410 review F1 loop: composition validates the
// declared SharedDeps.NonceStore.Kind(), but the store that ACTUALLY guards
// /internal/v1/* is whatever the caller's RuntimeOptionsFunc put in this auth
// plan. When composition injects the deployment Topology (WithControlPlaneTopology),
// the real auth-plan store is validated here against it — in real adapter mode an
// in-memory store is rejected for multi-pod and unrecognized kinds are rejected
// fail-closed — mirroring fx.ValidateApp (validate the constructed graph, not a
// parallel declaration).
func (b *Bootstrap) validateAuthServiceTokenPlans() error {
	// Topology-dependent replay-safety applies only in real adapter mode and only
	// when composition injected the topology; requireDistributed narrows it to
	// multi-pod. The always-on nil/noop/ring checks run regardless of topology.
	enforceReplaySafe := b.controlPlaneTopology.RequireProductionControlPlane()
	requireDistributed := b.controlPlaneTopology.RequiresDistributedReplay()
	for ref, cfg := range b.listenerConfigs {
		seen := 0
		for i, plan := range cfg.authChain {
			p, ok := plan.(auth.AuthServiceToken)
			if !ok {
				continue
			}
			seen++
			if seen > 1 {
				return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
					"at most one AuthServiceToken plan allowed in authChain",
					errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("listener=%q", ref.String()))))
			}
			if err := validateAuthServiceTokenPlan(ref.String(), i, p, enforceReplaySafe, requireDistributed); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateAuthServiceTokenPlan(
	listener string,
	position int,
	p auth.AuthServiceToken,
	enforceReplaySafe, requireDistributed bool,
) error {
	if validation.IsNilInterface(p.Store) {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"AuthServiceToken Store must not be nil; construct it with auth.NewAuthServiceToken(store, ring)",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalListenerPositionFmt, listener, position))))
	}
	if validation.IsNilInterface(p.Ring) {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"AuthServiceToken Ring must not be nil; construct it with auth.NewAuthServiceToken(store, ring)",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalListenerPositionFmt, listener, position))))
	}
	if p.Store.Kind() == auth.NonceStoreKindNoop {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"AuthServiceToken Store must not be NonceStoreKindNoop; service-token guards require replay protection",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalListenerPositionFmt, listener, position))))
	}
	// Topology-dependent replay-safety (#1410 review F1): in real adapter mode the
	// store that actually guards the listener must be replay-safe for the
	// deployment topology — in-memory rejected for multi-pod, unrecognized kinds
	// rejected fail-closed. Single-sourced with composition via
	// NonceStoreKind.ReplaySafe. (Noop is already rejected above, unconditionally.)
	if enforceReplaySafe && !p.Store.Kind().ReplaySafe(requireDistributed) {
		return errcode.New(errcode.KindInternal, errcode.ErrControlplaneNonceStoreMissing,
			"internal-listener service-token guard NonceStore is not replay-safe for this topology "+
				"in adapter mode \"real\"; a distributed store is required for multi-pod (an in-memory "+
				"store needs single-pod acknowledgement) and unrecognized kinds are refused fail-open. "+
				"Build the auth plan from the validated SharedDeps.NonceStore",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(
				internalListenerPositionFmt+" kind=%q requireDistributed=%t",
				listener, position, p.Store.Kind(), requireDistributed))))
	}
	if got := len(p.Ring.Current()); got < auth.MinHMACKeyBytes {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"AuthServiceToken Ring.Current() is too short",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(
				internalListenerPositionMinFmt,
				listener, position, got, auth.MinHMACKeyBytes))))
	}
	return nil
}

// validateAuthOperatorPlans enforces the AuthOperator (operator control-plane)
// invariants at phase0:
//   - AuthOperator may appear only on cell.AdminListener — operator credentials
//     are the admin-plane gate; they have no meaning on the public or
//     internal/cell→cell listeners.
//   - At most one AuthOperator per chain.
//   - A struct-literal AuthOperator must carry non-empty credentials and a
//     non-nil limiter. The NewAuthOperator constructor already enforces this,
//     but a direct struct literal would otherwise reach phase5 and fail inside
//     HTTP middleware assembly rather than at the option boundary.
//   - cell.AdminListener MUST carry an AuthOperator: the admin plane is
//     authenticated by operator credentials (loopback isolation alone is not
//     sufficient — defense in depth). An AuthNone-only AdminListener is rejected
//     here; the AuthNone+guard mix is separately rejected by
//     validateAuthNoneExclusive.
func (b *Bootstrap) validateAuthOperatorPlans() error {
	for ref, cfg := range b.listenerConfigs {
		seen := 0
		for i, plan := range cfg.authChain {
			p, ok := plan.(auth.AuthOperator)
			if !ok {
				continue
			}
			seen++
			if seen > 1 {
				return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
					"at most one AuthOperator plan allowed in authChain",
					errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("listener=%q", ref.String()))))
			}
			if ref != cell.AdminListener {
				return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
					"AuthOperator may only be used on cell.AdminListener; operator credentials "+
						"are the admin control-plane gate, not a cell→cell or public scheme",
					errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalListenerPositionFmt, ref.String(), i))))
			}
			if len(p.Username) == 0 || len(p.Password) == 0 {
				return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
					"AuthOperator requires non-empty operator credentials; construct it with auth.NewAuthOperator(...)",
					errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalListenerPositionFmt, ref.String(), i))))
			}
			if validation.IsNilInterface(p.Limiter) {
				return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
					"AuthOperator Limiter must not be nil; construct it with auth.NewAuthOperator(...)",
					errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalListenerPositionFmt, ref.String(), i))))
			}
		}
		if ref == cell.AdminListener && seen == 0 {
			return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
				"cell.AdminListener requires an AuthOperator plan; the operator control-plane must be "+
					"authenticated (loopback isolation alone is not sufficient). Wire "+
					"bootstrap.WithListener(cell.AdminListener, addr, []kauth.ListenerAuth{<auth.NewAuthOperator(...)>})",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("listener=%q", ref.String()))))
		}
	}
	return nil
}

// checkJWTSingleton validates that chain contains at most one JWT plan and it
// is in the first position.
func checkJWTSingleton(listenerDesc string, chain []auth.ListenerAuth) error {
	jwtCount := 0
	jwtPos := -1
	for i, p := range chain {
		switch p.(type) {
		case auth.AuthJWT, auth.AuthJWTFromAssembly:
			jwtCount++
			if jwtPos == -1 {
				jwtPos = i
			}
		}
	}
	if jwtCount == 0 {
		return nil
	}
	if jwtCount > 1 {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"at most one AuthJWT/AuthJWTFromAssembly plan allowed in chain",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("listener=%q count=%d", listenerDesc, jwtCount))))
	}
	// Exactly one JWT plan — it must be at position 0.
	if jwtPos != 0 {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"AuthJWT/AuthJWTFromAssembly must be sole/first plan in chain",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalListenerPositionFmt, listenerDesc, jwtPos))))
	}
	return nil
}
