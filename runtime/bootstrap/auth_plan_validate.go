package bootstrap

// auth_plan_validate.go — phase0/phase4 typed validation for AuthPlan chains.
//
// Four validation functions are used by bootstrap_phases.go:
//
//   validateAuthPlanAssemblyMatch  — phase0: AuthJWTFromAssembly.Assembly must be
//                                    the same instance as WithAssembly's assembly.
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

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// validateAuthPlanAssemblyMatch enforces the single-assembly invariant:
// any AuthJWTFromAssembly in a listener chain must carry the same AssemblyRef
// instance that was passed to WithAssembly. This prevents the silent bug where
// PolicyJWTFromAssembly(asmA) + WithAssembly(asmB) would discover auth in asmA
// while running the rest of bootstrap against asmB.
//
// Replaces: validateListenerPolicyAssemblyMatch (bootstrap_phases.go).
func (b *Bootstrap) validateAuthPlanAssemblyMatch() error {
	if b.assembly == nil {
		return nil
	}
	for ref, cfg := range b.listenerConfigs {
		for _, plan := range cfg.authChain {
			p, ok := plan.(cell.AuthJWTFromAssembly)
			if !ok {
				continue
			}
			// Identity check: same pointer, not just same ID.
			if p.Assembly != b.assembly {
				return errcode.New(errcode.ErrCellInvalidConfig,
					fmt.Sprintf(
						"bootstrap: listener %q AuthJWTFromAssembly carries assembly %q but WithAssembly registered %q; "+
							"the composition root must wire the same *assembly.CoreAssembly instance everywhere",
						ref.String(), p.Assembly.ID(), b.assembly.ID()))
			}
		}
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
		return errcode.New(errcode.ErrCellInvalidConfig,
			fmt.Sprintf(
				"bootstrap: %s uses AuthMTLS without WithListenerTLS; "+
					"set tls.Config.ClientAuth=RequireAndVerifyClientCert and ClientCAs=<pool> "+
					"so the handshake layer enforces the chain",
				source))
	}
	if tlsCfg.ClientAuth < tls.VerifyClientCertIfGiven {
		return errcode.New(errcode.ErrCellInvalidConfig,
			fmt.Sprintf(
				"bootstrap: %s uses AuthMTLS but tls.Config.ClientAuth=%v; "+
					"set ClientAuth >= tls.VerifyClientCertIfGiven (RequireAndVerifyClientCert recommended)",
				source, tlsCfg.ClientAuth))
	}
	if tlsCfg.ClientCAs == nil {
		return errcode.New(errcode.ErrCellInvalidConfig,
			fmt.Sprintf(
				"bootstrap: %s uses AuthMTLS but tls.Config.ClientCAs is nil; "+
					"set ClientCAs to the CA pool the handshake should accept",
				source))
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

// checkJWTSingleton validates that chain contains at most one JWT plan and it
// is in the first position.
func checkJWTSingleton(listenerDesc string, chain []cell.ListenerAuth) error {
	jwtCount := 0
	jwtPos := -1
	for i, p := range chain {
		switch p.(type) {
		case cell.AuthJWT, cell.AuthJWTFromAssembly:
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
		return errcode.New(errcode.ErrCellInvalidConfig,
			fmt.Sprintf("listener %q: at most one AuthJWT/AuthJWTFromAssembly plan allowed in chain, found %d",
				listenerDesc, jwtCount))
	}
	// Exactly one JWT plan — it must be at position 0.
	if jwtPos != 0 {
		return errcode.New(errcode.ErrCellInvalidConfig,
			fmt.Sprintf("listener %q: AuthJWT/AuthJWTFromAssembly must be sole/first plan in chain (found at position %d)",
				listenerDesc, jwtPos))
	}
	return nil
}
