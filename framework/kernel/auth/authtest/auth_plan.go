// Package authtest provides composition-root convenience helpers for AuthPlan
// constructors. These wrap the error-first auth.NewAuth* with panic-on-error
// semantics for test fixtures (K8s `resource.MustParse` model).
//
// Import path: `github.com/ghbvf/gocell/framework/kernel/auth/authtest`. K8s
// `httptest.NewRecorder` precedent for test fixture sub-packages.
//
// Production composition roots (cmd/, examples/) must propagate errors from
// auth.NewAuth* directly; this helper exists in the authtest sub-package so
// the symbol cannot be reached from production code without explicitly
// importing a `*test*`-named package — making the intent visible at every
// call site.
package authtest

import (
	"github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
)

// MustAuthJWT wraps auth.NewAuthJWT with panic-on-error. Callers must be
// `_test.go` files; production composition roots use auth.NewAuthJWT and
// propagate the error to bootstrap.
func MustAuthJWT(v auth.IntentTokenVerifier) auth.AuthJWT {
	plan, err := auth.NewAuthJWT(v)
	if err != nil {
		panic(panicregister.Approved("authtest-auth-jwt",
			errcode.Assertion("authtest: MustAuthJWT: %v", err)))
	}
	return plan
}

// MustAuthJWTFromAssembly wraps auth.NewAuthJWTFromAssembly with
// panic-on-error. Same caller policy as MustAuthJWT.
func MustAuthJWTFromAssembly(asm auth.AssemblyRef) auth.AuthJWTFromAssembly {
	plan, err := auth.NewAuthJWTFromAssembly(asm)
	if err != nil {
		panic(panicregister.Approved("authtest-auth-jwt-assembly",
			errcode.Assertion("authtest: MustAuthJWTFromAssembly: %v", err)))
	}
	return plan
}

// MustAuthServiceToken wraps auth.NewAuthServiceToken with panic-on-error.
// Same caller policy as MustAuthJWT.
func MustAuthServiceToken(store auth.NonceStore, ring auth.ServiceKeyring) auth.AuthServiceToken {
	plan, err := auth.NewAuthServiceToken(store, ring)
	if err != nil {
		panic(panicregister.Approved("authtest-auth-service-token",
			errcode.Assertion("authtest: MustAuthServiceToken: %v", err)))
	}
	return plan
}
