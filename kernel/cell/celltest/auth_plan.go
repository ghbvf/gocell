// auth_plan.go — composition-root convenience helpers for AuthPlan
// constructors. These wrap the error-first cell.NewAuth* with panic-on-error
// semantics for test fixtures (K8s `resource.MustParse` model).
//
// Production composition roots (cmd/, examples/) must propagate errors from
// cell.NewAuth* directly; this helper exists in the celltest sub-package so
// the symbol cannot be reached from production code without explicitly
// importing a `*test*`-named package — making the intent visible at every
// call site.
package celltest

import (
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// MustAuthJWT wraps cell.NewAuthJWT with panic-on-error. Callers must be
// `_test.go` files; production composition roots use cell.NewAuthJWT and
// propagate the error to bootstrap.
func MustAuthJWT(v cell.IntentTokenVerifier) cell.AuthJWT {
	plan, err := cell.NewAuthJWT(v)
	if err != nil {
		panic(panicregister.Approved("celltest-auth-jwt",
			errcode.Assertion("celltest: MustAuthJWT: %v", err)))
	}
	return plan
}

// MustAuthJWTFromAssembly wraps cell.NewAuthJWTFromAssembly with
// panic-on-error. Same caller policy as MustAuthJWT.
func MustAuthJWTFromAssembly(asm cell.AssemblyRef) cell.AuthJWTFromAssembly {
	plan, err := cell.NewAuthJWTFromAssembly(asm)
	if err != nil {
		panic(panicregister.Approved("celltest-auth-jwt-assembly",
			errcode.Assertion("celltest: MustAuthJWTFromAssembly: %v", err)))
	}
	return plan
}

// MustAuthServiceToken wraps cell.NewAuthServiceToken with panic-on-error.
// Same caller policy as MustAuthJWT.
func MustAuthServiceToken(store cell.NonceStore, ring cell.HMACKeyring) cell.AuthServiceToken {
	plan, err := cell.NewAuthServiceToken(store, ring)
	if err != nil {
		panic(panicregister.Approved("celltest-auth-service-token",
			errcode.Assertion("celltest: MustAuthServiceToken: %v", err)))
	}
	return plan
}
