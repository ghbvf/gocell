package authtest

import (
	"net/http"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// RequireAuthenticated returns a Policy that asserts a non-anonymous Principal
// is present in context. ONLY use in runtime/auth middleware behavior tests;
// production cells must declare an explicit role policy via auth.AnyRole(...).
//
// The defence-in-depth check on PrincipalUser.Subject is preserved from the
// legacy auth.Authenticated() implementation.
//
// see auth.TestContext for handler test fixture
func RequireAuthenticated() auth.Policy {
	return func(r *http.Request) error {
		p, ok := auth.FromContext(r.Context())
		if !ok {
			return errcode.New(errcode.ErrAuthUnauthorized, "authentication required")
		}
		if p.Kind == auth.PrincipalAnonymous {
			return errcode.New(errcode.ErrAuthUnauthorized, "anonymous principal not permitted")
		}
		if p.Kind == auth.PrincipalUser && p.Subject == "" {
			return errcode.New(errcode.ErrAuthUnauthorized, "principal subject missing")
		}
		return nil
	}
}
