package auth

import (
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// Route binds a handler to a contract. Contract is the single source of
// truth for HTTP method + fully-qualified path + observability metadata —
// runtime-side Method/Path fields have been eliminated in round-4 (the
// pre-round-4 dual-maintenance of Route.Method/Path vs Contract.Method/Path
// was the drift surface reviewers called out; the collapse pins the two to
// the same literal).
//
// Mount derives the chi sub-route-relative registration path by stripping
// the receiving mux's Prefix() (when the mux implements cell.Prefixer;
// runtime/http/router.Router's nested chiRouterAdapter does). Stdlib
// *http.ServeMux and test stubs without a prefix use Contract.Path
// unchanged.
type Route struct {
	// Contract is the ContractSpec to bind to this route. REQUIRED. Its
	// Method + Path drive both the chi registration pattern and the span
	// attributes (gocell.contract.id / kind / transport + http.method /
	// route). Contract.Kind MUST be "http".
	Contract wrapper.ContractSpec

	// Handler is the inner http.Handler. REQUIRED.
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
}

// Mount registers the Route on mux. It:
//
//  1. Validates the Route shape (Contract required, Contract.Kind=="http",
//     bypass-flag mutual exclusivity).
//  2. Derives the chi-relative registration path by stripping mux.Prefix()
//     from Contract.Path (when the mux implements cell.Prefixer) so
//     chi's own prefix composition produces the correct external URL.
//  3. Wraps Handler with RequirePolicy(Policy) (inner) and
//     wrapper.HTTPHandler(Contract) (outer) — the wrapper writes
//     ctxkeys.ContractID + contributes span attrs to the outer middleware
//     Tracing's single span, so policy denials (403) still emit
//     gocell.contract.id.
//  4. Registers the handler on mux via Handle("{METHOD} {relPath}").
//  5. Forwards AuthRouteMeta to cell.AuthRouteDeclarer if mux implements
//     it (chiRouterAdapter composes the prefix back to fully-qualified
//     path before handing off to the top-level Router's declaration
//     table).
//
// Mount panics on invalid configurations (fail-fast at startup is
// preferred over silent runtime drift).
func Mount(mux cell.RouteHandler, r Route) {
	r.validateOrPanic()

	prefix := ""
	if p, ok := mux.(cell.Prefixer); ok {
		prefix = p.Prefix()
	}
	// Root prefix "/" is semantically identical to no prefix — chi mounted
	// at root owns the whole tree, so contract paths are registered at
	// their absolute form. Normalising here keeps isPathSegmentPrefix /
	// stripMountPrefix free of a special case for "/".
	if prefix == "/" {
		prefix = ""
	}
	if prefix != "" && !isPathSegmentPrefix(r.Contract.Path, prefix) {
		panic(fmt.Sprintf(
			"auth.Mount %s %s: Contract.Path does not extend mux mount prefix %q — "+
				"sub-routers must declare a Contract.Path that begins with the prefix "+
				"on a path-segment boundary. Fix the Contract.Path or the Route()/Mount() "+
				"the caller used to scope the sub-router.",
			r.Contract.Method, r.Contract.Path, prefix))
	}
	relPath := stripMountPrefix(r.Contract.Path, prefix)

	handler := r.Handler
	if r.Policy != nil {
		handler = RequirePolicy(r.Policy)(handler)
	}
	// wrapper.HTTPHandler is a pure ctx contributor (round-4) — it writes
	// ContractID + contract attrs into ctx so the outer middleware.Tracing
	// span late-binds them. No inner span is created.
	handler = wrapper.MustHTTPHandler(r.Contract, handler)

	cleanedRel := path.Clean(relPath)
	mux.Handle(r.Contract.Method+" "+cleanedRel, handler)

	if declarer, ok := mux.(cell.AuthRouteDeclarer); ok {
		// declarer.DeclareAuthMeta's Path is the sub-route-relative path;
		// chiRouterAdapter recomposes it with its prefix on its way up to
		// the top-level Router.
		declarer.DeclareAuthMeta(cell.AuthRouteMeta{
			Method:              r.Contract.Method,
			Path:                cleanedRel,
			Public:              r.Public,
			PasswordResetExempt: r.PasswordResetExempt,
		})
	}
	if declarer, ok := mux.(cell.HTTPContractDeclarer); ok {
		declarer.DeclareHTTPContract(r.Contract)
	}
}

// isPathSegmentPrefix reports whether prefix is a path-segment prefix of
// fullPath. Returns true only when fullPath == prefix, or when fullPath
// starts with prefix AND the character immediately after prefix is '/'.
// Empty prefix returns false (use the fast-path in the caller for empty
// prefix).
//
// Examples:
//
//	isPathSegmentPrefix("/api/v1/access/x", "/api/v1/access") → true
//	isPathSegmentPrefix("/api/v1/auth/x",   "/api/v1/a")      → false
func isPathSegmentPrefix(fullPath, prefix string) bool {
	if prefix == "" {
		return false
	}
	if fullPath == prefix {
		return true
	}
	if len(fullPath) <= len(prefix) {
		return false
	}
	if !strings.HasPrefix(fullPath, prefix) {
		return false
	}
	return fullPath[len(prefix)] == '/'
}

// stripMountPrefix returns fullPath with prefix removed. When prefix is
// empty (or fullPath is not a path-segment extension of prefix), fullPath
// is returned unchanged — the caller will still receive a valid chi pattern
// because the mux has no prefix to compose.
//
// Invariant: when isPathSegmentPrefix(fullPath, prefix) is true, either
// fullPath == prefix (stripped == "", returns "/") or
// fullPath[len(prefix)] == '/' (stripped starts with '/'). There is no
// case where stripped is non-empty without a leading slash.
func stripMountPrefix(fullPath, prefix string) string {
	if prefix == "" || !isPathSegmentPrefix(fullPath, prefix) {
		return fullPath
	}
	stripped := strings.TrimPrefix(fullPath, prefix)
	if stripped == "" {
		return "/"
	}
	return stripped
}

func (r Route) validateOrPanic() {
	if r.Handler == nil {
		panic("auth.Mount: Handler must not be nil")
	}
	if r.Contract.ID == "" {
		panic("auth.Mount: Route.Contract.ID must be set — round-4 dropped the " +
			"untraced legacy registration shape; every Mount call must bind a " +
			"wrapper.ContractSpec literal. If the contract has no YAML yet, " +
			"author one in contracts/ first.")
	}
	r.validateContractShape()
	r.validateBypassCompatibility()
}

// validateContractShape verifies the Contract shape at registration time
// so startup fails fast on structural mistakes.
func (r Route) validateContractShape() {
	if r.Contract.Kind != "http" {
		panic(fmt.Sprintf("auth.Mount: Contract.Kind %q must be \"http\"", r.Contract.Kind))
	}
	if r.Contract.Method == "" {
		panic("auth.Mount: Contract.Method must not be empty")
	}
	if r.Contract.Method != strings.ToUpper(strings.TrimSpace(r.Contract.Method)) {
		panic(fmt.Sprintf("auth.Mount: Contract.Method %q must be upper-case", r.Contract.Method))
	}
	if !validRouteMethods[r.Contract.Method] {
		panic(fmt.Sprintf(
			"auth.Mount: Contract.Method %q not recognised (GET/HEAD/POST/PUT/PATCH/DELETE/OPTIONS/CONNECT/TRACE)",
			r.Contract.Method))
	}
	if r.Contract.Path == "" || r.Contract.Path[0] != '/' {
		panic(fmt.Sprintf("auth.Mount: Contract.Path %q must start with '/'", r.Contract.Path))
	}
}

func (r Route) validateBypassCompatibility() {
	if r.Public && r.Policy != nil {
		panic(fmt.Sprintf(
			"auth.Mount %s %s: Public=true conflicts with non-nil Policy (public routes have no server-side authorization)",
			r.Contract.Method, r.Contract.Path))
	}
	if r.Public && r.PasswordResetExempt {
		panic(fmt.Sprintf(
			"auth.Mount %s %s: Public=true conflicts with PasswordResetExempt=true (gate runs only for authenticated tokens)",
			r.Contract.Method, r.Contract.Path))
	}
}
