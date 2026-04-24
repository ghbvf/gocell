package auth

import (
	"net/http"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// ===========================================================================
// PR-A11 round-4 legacy-test compat shim.
//
// All production call sites of auth.Declare + auth.RouteDecl are migrated
// to auth.Mount + auth.Route{Contract, ...} — confirmed by grep across
// cells/**/*.go and cmd/**/*.go (excluding *_test.go). This file exists
// solely to keep legacy TEST files compiling during the transition window
// tracked in backlog entry PR-A11-TESTMIGRATE (next roll). Every call to
// Declare / RouteDecl below is a registration that ends up on the
// contract-less fallback path: handler.ServeHTTP is registered with a
// synthetic ContractSpec built from the legacy Method+Path fields and
// kind "http-legacy" to distinguish it from first-class contract routes
// in metrics/logs.
//
// Deprecated-for-new-code: use auth.Mount(mux, auth.Route{Contract, ...}).
// Do NOT call this from new production code — FMT-18 governance will
// eventually refuse spec.Kind == "http-legacy" literals in cells/**.
// ===========================================================================

// RouteDecl is the legacy route declaration still referenced by test
// fixtures. New code uses auth.Route (Contract-driven).
type RouteDecl struct {
	Method              string
	Path                string
	Handler             http.Handler
	Policy              Policy
	Public              bool
	PasswordResetExempt bool
	Delegated           bool
}

// Declare registers a legacy route via auth.Mount. It synthesises a
// minimal ContractSpec with Kind="http-legacy" so wrapper.HTTPHandler's
// validation is bypassed while still running the Mount path for
// policy/meta handling.
//
// Deprecated-for-new-code: use auth.Mount with a real wrapper.ContractSpec
// from contracts/**/contract.yaml.
func Declare(mux cell.RouteHandler, d RouteDecl) {
	declareLegacy(mux, d)
}

// declareLegacy is the implementation. Kept separate so future PRs can
// wrap it with additional deprecation diagnostics without touching the
// public symbol.
func declareLegacy(mux cell.RouteHandler, d RouteDecl) {
	r := Route{
		Handler:             d.Handler,
		Policy:              d.Policy,
		Public:              d.Public,
		PasswordResetExempt: d.PasswordResetExempt,
		Delegated:           d.Delegated,
	}
	// validateOrPanic requires Contract.ID != "" — synthesise one from the
	// method+path so legacy callers continue to fail-fast on invalid
	// method/path but still observe the new Mount pipeline.
	r.Contract = wrapper.ContractSpec{
		ID:        "legacy:" + d.Method + ":" + d.Path,
		Kind:      "http",
		Transport: "http",
		Method:    d.Method,
		Path:      d.Path,
	}
	Mount(mux, r)
}
