package bootstrap

import (
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
)

// PolicyJWT returns a cell.Policy marking a listener as JWT-authenticated.
//
// The returned Policy carries the verifier in its Extension field; Bootstrap
// extracts it during phase5 and installs the router-aware AuthMiddleware via
// router.WithAuthMiddleware. The router is the only place that has the
// compiled Public / PasswordResetExempt matchers (built by FinalizeAuth from
// auth.Mount declarations), so JWT auth must flow through the router for
// Public:true routes to bypass cleanly.
//
// Direct use as listener default policy:
//
//	bootstrap.WithListener(cell.PrimaryListener, ":8080", bootstrap.PolicyJWT(verifier))
//
// Fail-fast: a nil verifier panics with "bootstrap: PolicyJWT verifier must
// not be nil". Use PolicyJWTFromAssembly when the verifier should be
// discovered from an authProvider cell.
//
// Note: Middleware is intentionally nil. Bootstrap rebuilds the JWT chain on
// the listener's router via router.WithAuthMiddleware so the middleware sees
// the router's compiled Public / PasswordResetExempt matchers. Mounting the
// Policy without going through Bootstrap (e.g. as a sub-policy inside
// PolicyStack) is not supported — the verifier never reaches the router.
//
// ref: go-kratos/kratos transport/http/server.go — middleware injected at server build time.
// ref: zeromicro/go-zero rest/server.go — WithJwt at server option level.
func PolicyJWT(v auth.IntentTokenVerifier) cell.Policy {
	if v == nil {
		panic("bootstrap: PolicyJWT verifier must not be nil; use PolicyJWTFromAssembly(asm) to discover from an authProvider cell")
	}
	return cell.Policy{
		Name:      "jwt",
		Extension: jwtVerifierGetterFn(func() auth.IntentTokenVerifier { return v }),
	}
}

// jwtVerifierGetter is the contract that runtime/bootstrap uses to extract a
// resolved verifier from a "jwt" cell.Policy.Extension field. Defined as a
// function-typed adapter so both eager (PolicyJWT) and lazy
// (PolicyJWTFromAssembly) variants share a single call site in Bootstrap.
type jwtVerifierGetter interface {
	verifier() auth.IntentTokenVerifier
}

type jwtVerifierGetterFn func() auth.IntentTokenVerifier

func (f jwtVerifierGetterFn) verifier() auth.IntentTokenVerifier { return f() }
