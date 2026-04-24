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
// an HTTP handler by calling Mount with a Route whose Contract carries the
// wire-level metadata (contract id, method, path) — wrapper.HTTPHandler is
// automatically composed over Handler so every route emits a span tagged
// with gocell.contract.id / kind / transport + http.method / route /
// status_code. Policy / Public / PasswordResetExempt / Delegated remain on
// Route (they describe auth-middleware bypass semantics, not contract shape).
//
// Legacy callers can still use Declare(RouteDecl{...}); it forwards to
// Mount without populating Contract — those routes remain untraced by
// wrapper and will be migrated one by one in subsequent PRs until Declare
// and RouteDecl are retired.
type Route struct {
	// Contract is the ContractSpec to bind to this route. When non-zero,
	// Method/Path are derived from Contract (Method/Path fields on Route
	// must be left empty). The zero value selects the legacy behaviour:
	// Method/Path fields must be provided instead.
	Contract wrapper.ContractSpec

	// Handler is the inner http.Handler. Mount wraps it via
	// wrapper.HTTPHandler (if Contract is non-zero) and then composes the
	// Policy enforcement guard on top.
	Handler http.Handler

	// Policy is the per-route authorization policy enforced by
	// RequirePolicy. Nil = no guard.
	Policy Policy

	// Public marks a JWT-exempt route. Public routes MUST carry no Policy
	// (server-side authorization has no subject to authorize) and no
	// PasswordResetExempt (the gate runs only for authenticated tokens).
	Public bool

	// PasswordResetExempt allows reset-required tokens through this route.
	// Handlers are responsible for tighter checks (e.g. verifying the
	// session owns the reset).
	PasswordResetExempt bool

	// Delegated marks an /internal/v1/* route. FinalizeAuth validates the
	// Delegated ⇔ path-prefix implication so discrepancies fail at startup.
	Delegated bool

	// Method and Path are used only when Contract is zero-value. For new
	// code prefer setting Contract and leaving these blank.
	Method string
	Path   string
}

// Mount registers the Route on mux. It validates the fields, composes the
// wrapper.HTTPHandler (contract-bound routes only), applies the Policy
// guard, and forwards AuthRouteMeta to the mux if it implements
// AuthRouteDeclarer.
//
// Mount panics on invalid configurations (fail-fast at startup is preferred
// over a silent runtime drift).
func Mount(mux cell.RouteHandler, r Route) {
	method, rawPath := r.resolveMethodAndPath()
	r.validateOrPanic(method, rawPath)

	cleanedPath := path.Clean(rawPath)
	pattern := method + " " + cleanedPath
	handler := r.Handler

	if r.Contract.ID != "" {
		// Validate again with canonical Method/Path so wrapper.ContractSpec
		// is internally consistent even when Route carried overrides.
		spec := r.Contract
		spec.Method = method
		spec.Path = cleanedPath
		handler = wrapper.HTTPHandler(spec, handler)
	}
	if r.Policy != nil {
		handler = RequirePolicy(r.Policy)(handler)
	}
	mux.Handle(pattern, handler)

	if declarer, ok := mux.(cell.AuthRouteDeclarer); ok {
		declarer.DeclareAuthMeta(cell.AuthRouteMeta{
			Method:              method,
			Path:                cleanedPath,
			Public:              r.Public,
			PasswordResetExempt: r.PasswordResetExempt,
			Delegated:           r.Delegated,
		})
	}
}

// resolveMethodAndPath picks the source of the wire-level method/path:
// Contract when populated (contract-first API), otherwise the legacy
// Method/Path fields on Route. It does not validate beyond non-empty.
func (r Route) resolveMethodAndPath() (string, string) {
	if r.Contract.ID != "" && r.Contract.Kind == "http" {
		return r.Contract.Method, r.Contract.Path
	}
	return r.Method, r.Path
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
	if r.Method != "" || r.Path != "" {
		panic("auth.Mount: Route.Method/Path must be empty when Contract is set")
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
