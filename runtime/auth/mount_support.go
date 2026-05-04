package auth

import (
	"fmt"
	"net/http"

	"github.com/ghbvf/gocell/pkg/httputil"
)

// validRouteMethods lists the HTTP verbs auth.Mount accepts on Contract.Method.
// Keeping this list in sync with exempt.validExemptMethods and
// middleware.validMethods is intentional — divergence would let Mount accept
// a method that the public-endpoint or password-reset compilers later reject
// at FinalizeAuth time. Mount validation is the first gate; later compilers
// act as defense in depth.
var validRouteMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodPost:    true,
	http.MethodPut:     true,
	http.MethodPatch:   true,
	http.MethodDelete:  true,
	http.MethodOptions: true,
	http.MethodConnect: true,
	http.MethodTrace:   true,
}

// RequirePolicy lifts a Policy into a middleware-shaped
// `func(http.Handler) http.Handler`. On policy failure it writes the domain
// error via httputil.WriteError and short-circuits the chain; on
// success it delegates to next.
//
// ref: grpc-ecosystem/go-grpc-middleware auth.UnaryServerInterceptor —
// policy is declared at registration time, not inside the handler body.
// ref: go-chi/jwtauth Authenticator — short-circuit pattern with response
// written inside the guard.
func RequirePolicy(p Policy) (func(http.Handler) http.Handler, error) {
	if p == nil {
		return nil, fmt.Errorf("auth.RequirePolicy: policy must not be nil")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := p(r); err != nil {
				httputil.WriteError(r.Context(), w, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}, nil
}
