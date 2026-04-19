package auth

import (
	"fmt"
	"net/http"
	"path"
	"strings"

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

// Declare is the single entry point a Cell uses to register an HTTP route.
// It validates the declaration, composes the final handler (optionally
// wrapping it in a Policy-enforcing middleware), and registers it on the
// supplied mux. When the mux additionally implements cell.AuthRouteDeclarer,
// the pure-data AuthRouteMeta is forwarded so Router/TestMux can build the
// AuthMiddleware matchers at finalize time.
//
// Pattern used for mux.Handle follows Go 1.22 ServeMux syntax:
// "METHOD /path". Path is normalised via path.Clean so "/a//b" and "/a/b"
// compare equal.
//
// ref: go-zero rest/engine route metadata; Kratos transport/http server
// Route — registration-time auth context is the single source of truth.
func Declare(mux cell.RouteHandler, d RouteDecl) {
	d.validateOrPanic()

	pattern := d.Method + " " + d.normalisedPath()
	handler := d.Handler
	if d.Policy != nil {
		handler = RequirePolicy(d.Policy)(handler)
	}
	mux.Handle(pattern, handler)

	if declarer, ok := mux.(cell.AuthRouteDeclarer); ok {
		declarer.DeclareAuthMeta(cell.AuthRouteMeta{
			Method:              d.Method,
			Path:                d.normalisedPath(),
			Public:              d.Public,
			PasswordResetExempt: d.PasswordResetExempt,
			Delegated:           d.Delegated,
		})
	}
}

func (d RouteDecl) normalisedPath() string {
	return path.Clean(d.Path)
}

func (d RouteDecl) validateOrPanic() {
	method := strings.ToUpper(strings.TrimSpace(d.Method))
	if method == "" {
		panic("auth.Declare: Method must not be empty")
	}
	if !validRouteMethods[method] {
		panic(fmt.Sprintf(
			"auth.Declare: method %q not recognised (GET/HEAD/POST/PUT/PATCH/DELETE/OPTIONS/CONNECT/TRACE)",
			d.Method))
	}
	if method != d.Method {
		panic(fmt.Sprintf(
			"auth.Declare: Method %q must be upper-case %q", d.Method, method))
	}
	if d.Path == "" || d.Path[0] != '/' {
		panic(fmt.Sprintf("auth.Declare: Path %q must start with '/'", d.Path))
	}
	if d.Handler == nil {
		panic("auth.Declare: Handler must not be nil")
	}
	if d.Public && d.Policy != nil {
		panic(fmt.Sprintf(
			"auth.Declare %s %s: Public=true conflicts with non-nil Policy (public routes have no server-side authorization)",
			d.Method, d.Path))
	}
	if d.Public && d.PasswordResetExempt {
		panic(fmt.Sprintf(
			"auth.Declare %s %s: Public=true conflicts with PasswordResetExempt=true (gate runs only for authenticated tokens)",
			d.Method, d.Path))
	}
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
