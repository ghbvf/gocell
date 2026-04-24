package auth

import (
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// Route is the contract-first replacement for RouteDecl. A Cell registers
// an HTTP handler by calling Mount. Method + Path are the mux-relative
// registration pair (honouring chi sub-routes); Contract, when populated,
// carries the fully-qualified wire-level metadata (contract id, full path)
// so wrapper.HTTPHandler annotates every span with gocell.contract.id /
// kind / transport + http.method / route (using Contract.Path, the full
// fully-qualified path template) + http.status_code. Policy / Public /
// PasswordResetExempt / Delegated remain on Route — they describe auth-
// middleware bypass semantics, not contract shape.
//
// Legacy callers can still use Declare(RouteDecl{...}); it forwards to
// Mount without populating Contract. Those routes remain untraced by
// wrapper and will be migrated one by one in subsequent PRs until Declare
// and RouteDecl are retired.
type Route struct {
	// Contract is the ContractSpec to bind to this route. Optional. When
	// set, wrapper.HTTPHandler wraps Handler and span attributes use
	// Contract.Path (fully-qualified) as http.route.
	//
	// Note: Route.Method / Route.Path and Contract.Method / Contract.Path
	// play DIFFERENT roles and are NOT redundant copies of each other:
	//   - Route.Method / Route.Path drive the mux registration pattern
	//     (chi-sub-route-relative).
	//   - Contract.Method / Contract.Path drive the span attribute values
	//     (fully qualified — useful for dashboards even when a cell mounts
	//     under a chi sub-route prefix).
	// Keeping both lets a cell nest under sub.Route("/api/v1/access") and
	// still emit fully-qualified http.route on the span. When Contract.Method
	// is populated Mount asserts Contract.Method == Route.Method so the verb
	// at least cannot drift; Contract.Path is NOT checked against Route.Path
	// (the sub-route prefix is invisible at this layer — FMT-17 will police
	// that drift statically in a follow-up PR).
	Contract wrapper.ContractSpec

	// Handler is the inner http.Handler.
	Handler http.Handler

	// Policy is the per-route authorization policy enforced by
	// RequirePolicy. Nil = no guard.
	Policy Policy

	// Public marks a JWT-exempt route. Public routes MUST carry no Policy
	// (server-side authorization has no subject to authorize) and no
	// PasswordResetExempt (the gate runs only for authenticated tokens).
	Public bool

	// PasswordResetExempt allows reset-required tokens through this route.
	PasswordResetExempt bool

	// Delegated marks an /internal/v1/* route. FinalizeAuth validates the
	// Delegated ⇔ path-prefix implication so discrepancies fail at startup.
	Delegated bool

	// Method is the HTTP verb; required.
	Method string

	// Path is the mux-relative path (respects chi sub-routing); required.
	// When Contract is set, Path is the chi-sub-route-relative registration
	// path; the FULLY-QUALIFIED http.route span attribute is read from
	// Contract.Path instead. The FMT-17 governance rule (future) cross-
	// references the sub-route prefix + Path against Contract.Path at
	// validate time so drift fails fast in CI.
	Path string
}

// Mount registers the Route on mux. It validates the fields, composes the
// wrapper.HTTPHandler (contract-bound routes only), applies the Policy
// guard, and forwards AuthRouteMeta to the mux if it implements
// AuthRouteDeclarer.
//
// Mount panics on invalid configurations (fail-fast at startup is preferred
// over a silent runtime drift).
func Mount(mux cell.RouteHandler, r Route) {
	r.validateOrPanic(r.Method, r.Path)

	cleanedPath := path.Clean(r.Path)
	pattern := r.Method + " " + cleanedPath
	handler := r.Handler

	if r.Contract.ID != "" {
		// Span attributes use the fully-qualified Contract.Path so
		// observability dashboards bucket spans by full route even when
		// cells register under chi sub-routes (the mux-relative path differs
		// from the visible server path).
		handler = wrapper.HTTPHandler(r.Contract, handler)
	}
	// RequirePolicy wraps OUTERMOST so policy denials short-circuit before
	// the wrapper starts a span — a 403 emitted by a failed Policy is
	// untraced by the contract span, and only the generic auth middleware
	// records it. Do NOT swap this order: swapping would emit a span for
	// every pre-auth reject and flood observability backends.
	if r.Policy != nil {
		handler = RequirePolicy(r.Policy)(handler)
	}
	mux.Handle(pattern, handler)

	if declarer, ok := mux.(cell.AuthRouteDeclarer); ok {
		declarer.DeclareAuthMeta(cell.AuthRouteMeta{
			Method:              r.Method,
			Path:                cleanedPath,
			Public:              r.Public,
			PasswordResetExempt: r.PasswordResetExempt,
			Delegated:           r.Delegated,
		})
	}
}

func (r Route) validateOrPanic(method, rawPath string) {
	if r.Handler == nil {
		panic("auth.Mount: Handler must not be nil")
	}
	r.validateContractShape()
	validateMethod(method)
	validatePath(rawPath)
	r.validateBypassCompatibility(method, rawPath)
}

// validateContractShape reports Contract-level misconfigurations first —
// when a caller supplied a Contract with a non-http Kind we surface the root
// cause instead of the downstream empty-Method error.
func (r Route) validateContractShape() {
	if r.Contract.ID == "" {
		return
	}
	if r.Contract.Kind != "http" {
		panic(fmt.Sprintf("auth.Mount: Contract.Kind %q must be \"http\"", r.Contract.Kind))
	}
	if r.Contract.Method != "" && r.Contract.Method != r.Method {
		panic(fmt.Sprintf(
			"auth.Mount: Route.Method %q does not match Contract.Method %q",
			r.Method, r.Contract.Method))
	}
}

func validateMethod(method string) {
	if method == "" {
		panic("auth.Mount: Method must not be empty")
	}
	if method != strings.ToUpper(strings.TrimSpace(method)) {
		panic(fmt.Sprintf("auth.Mount: Method %q must be upper-case", method))
	}
	if !validRouteMethods[method] {
		panic(fmt.Sprintf(
			"auth.Mount: method %q not recognised (GET/HEAD/POST/PUT/PATCH/DELETE/OPTIONS/CONNECT/TRACE)",
			method))
	}
}

func validatePath(rawPath string) {
	if rawPath == "" || rawPath[0] != '/' {
		panic(fmt.Sprintf("auth.Mount: Path %q must start with '/'", rawPath))
	}
}

func (r Route) validateBypassCompatibility(method, rawPath string) {
	if r.Public && r.Policy != nil {
		panic(fmt.Sprintf(
			"auth.Mount %s %s: Public=true conflicts with non-nil Policy (public routes have no server-side authorization)",
			method, rawPath))
	}
	if r.Public && r.PasswordResetExempt {
		panic(fmt.Sprintf(
			"auth.Mount %s %s: Public=true conflicts with PasswordResetExempt=true (gate runs only for authenticated tokens)",
			method, rawPath))
	}
}
