package auth

import (
	"net/http"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// validRouteMethods mirrors exempt.validExemptMethods and middleware.validMethods.
// Keeping three lists in agreement is intentional — divergence would let
// Declare accept a method that the public-endpoint or password-reset
// compilers later reject at FinalizeAuth time. RouteDecl validation is the
// first gate; the later compilers act as defence in depth.
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

// RouteDecl is the full route declaration a Cell supplies to auth.Declare.
// Method + Path + Handler are mandatory; Policy and the three auth flags
// describe how the AuthMiddleware should treat the route.
//
// Constraints (validated by validateOrPanic at Declare time, fail-fast):
//
//   - Method must be a recognised HTTP verb (see validRouteMethods).
//   - Path must start with '/'.
//   - Handler must be non-nil.
//   - Public && Policy != nil panics — a public route has no server-side
//     authorization to enforce.
//   - Public && PasswordResetExempt panics — the password-reset gate runs
//     only for authenticated tokens, so marking a public route exempt is
//     a configuration smell.
type RouteDecl struct {
	Method              string
	Path                string
	Handler             http.Handler
	Policy              Policy
	Public              bool
	PasswordResetExempt bool
	Delegated           bool
}

// Declare registers an HTTP route. It is the legacy entry point retained
// so pre-Mount Cells keep compiling; internally it forwards to Mount with
// Contract left zero (route is registered untraced by wrapper, as before).
// Prefer Mount + a wrapper.ContractSpec in new code so trace spans carry
// gocell.contract.id automatically.
//
// ref: go-zero rest/engine route metadata; Kratos transport/http server
// Route — registration-time auth context is the single source of truth.
func Declare(mux cell.RouteHandler, d RouteDecl) {
	Mount(mux, Route{
		Handler:             d.Handler,
		Policy:              d.Policy,
		Public:              d.Public,
		PasswordResetExempt: d.PasswordResetExempt,
		Delegated:           d.Delegated,
		Method:              d.Method,
		Path:                d.Path,
	})
}

// RequirePolicy lifts a Policy into a middleware-shaped
// `func(http.Handler) http.Handler`. On policy failure it writes the domain
// error via httputil.WriteDomainError and short-circuits the chain; on
// success it delegates to next.
//
// A nil policy panics at wrap time — failing fast during startup/test is
// preferred over silently skipping authorization at request time.
//
// ref: grpc-ecosystem/go-grpc-middleware auth.UnaryServerInterceptor —
// policy is declared at registration time, not inside the handler body.
// ref: go-chi/jwtauth Authenticator — short-circuit pattern with response
// written inside the guard.
func RequirePolicy(p Policy) func(http.Handler) http.Handler {
	if p == nil {
		panic("auth.RequirePolicy: policy must not be nil")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := p(r); err != nil {
				httputil.WriteDomainError(r.Context(), w, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
