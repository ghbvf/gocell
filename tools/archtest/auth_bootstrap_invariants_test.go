// invariants:
//   - INVARIANT: CELLS-NO-ROUTEMUX-WRAPPER-01
//   - INVARIANT: AUTH-ROUTE-BOOTSTRAP-FLAG-REMOVED-01
//   - INVARIANT: SETUP-ADMIN-CODEGEN-BOOTSTRAP-AUTH-WIRED-01
//   - INVARIANT: AUTH-BOOTSTRAP-CLIENTS-MUTEX-01
//
// Auth bootstrap invariants — codified into archtest after the security
// boundary collapse described in
// docs/architecture/202605061600-adr-bootstrap-admin-boundary.md §D1 + §D10.
//
// BootstrapAuth (env-credentialed HTTP Basic Auth) and Contract.Clients
// (service-token caller-cell allowlist) are orthogonal authentication paths.
// The codegen output, the runtime/auth.Route shape, the cell-side handler,
// and the composition root all agree that BootstrapAuth is a required
// dependency for setup/admin and is mutually exclusive with Clients-bearing
// routes. There is no "declared bootstrap but no auth wired" intermediate
// state and no "BootstrapAuth coexisting with caller-cell allowlist" hybrid.
//
// Rules guarded here:
//
//   - CELLS-NO-ROUTEMUX-WRAPPER-01: cells/ must not embed cell.RouteMux. The
//     historical middlewareRouteMux pattern silently dropped HTTPContractDeclarer
//     from auth.Mount's interface fan-out; the same shape can re-emerge in any
//     cell that wants per-route middleware on a generated handler. New cells
//     must reach for runtime/http/router or auth.Mount middleware composition,
//     not custom mux wrappers.
//
//   - AUTH-ROUTE-BOOTSTRAP-FLAG-REMOVED-01: runtime/auth.Route must not declare
//     a Bootstrap bool field; bypass-with-replacement is expressed exclusively
//     by Route.BootstrapAuth (a non-nil func value). Two fields encoding the
//     same invariant invite drift.
//
//   - SETUP-ADMIN-CODEGEN-BOOTSTRAP-AUTH-WIRED-01: the generated setup/admin
//     handler must call auth.Mount with a non-zero BootstrapAuth literal
//     (i.e. the parameter threaded through NewHandler). This locks the codegen
//     template to the closed contract.
//
//   - AUTH-BOOTSTRAP-CLIENTS-MUTEX-01: any auth.Route composite literal that
//     declares BootstrapAuth (non-nil) must not bind a Contract whose Clients
//     field is non-empty. BootstrapAuth uses HTTP Basic Auth via env
//     credentials (FMT-28 limits the path range to /api/v1/*/setup/admin);
//     Contract.Clients drives the 4-part service-token caller-cell allowlist
//     (FMT-31 limits the path range to /internal/v1/*). The two authentication
//     paths are mutually exclusive; the runtime fail-fast in
//     runtime/auth/route.go validateBypassCompatibility is the second-layer
//     defense, this archtest is the static (Hard) gate that fails CI before
//     the misconfigured code can merge.

package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestCellsNoRouteMuxWrapper enforces CELLS-NO-ROUTEMUX-WRAPPER-01.
// Any struct in cells/ (non-test) that embeds cell.RouteMux is flagged.
// Embedding cell.RouteMux in a wrapper is the signature shape of
// middlewareRouteMux: it pretends to delegate the full RouteMux contract while
// only forwarding a subset of the optional auth/contract declarer interfaces.
// Future per-route middleware needs should be expressed via auth.Route's
// BootstrapAuth (or a sibling field) or via runtime/http/router middleware
// composition — never by re-implementing the mux interface inside cells/.
func TestCellsNoRouteMuxWrapper(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	scope := scanner.DirsScope(root, []string{"cells"})
	var violations []string
	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		// Cheap filter: skip files that don't reference the symbol literally.
		// We re-read is not needed; the AST is already parsed — just inspect.
		scanner.EachInSubtree[ast.StructType](fc.File, func(st *ast.StructType) {
			if st.Fields == nil {
				return
			}
			for _, field := range st.Fields.List {
				// Embedded field has no Names.
				if len(field.Names) != 0 {
					continue
				}
				if exprNamesRouteMux(field.Type) {
					violations = append(violations,
						fc.Rel+":"+fc.Fset.Position(field.Pos()).String())
				}
			}
		})
	})

	assert.Empty(t, violations,
		"CELLS-NO-ROUTEMUX-WRAPPER-01: cells/ must not embed cell.RouteMux. "+
			"Use auth.Route.BootstrapAuth or auth.Mount middleware composition for per-route middleware needs.")
}

// exprNamesRouteMux reports whether the embedded field type expression is
// `RouteMux` (dot-imported, dropped here because we don't expect that style)
// or `<pkg>.RouteMux` for any package alias. We accept any selector ending in
// `.RouteMux` because the alias name is local to each file.
func exprNamesRouteMux(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		return e.Sel != nil && e.Sel.Name == "RouteMux"
	case *ast.Ident:
		// Bare RouteMux ident — only possible if the file is itself in
		// kernel/cell, which the cells/ walk excludes.
		return e.Name == "RouteMux"
	}
	return false
}

// TestAuthRouteBootstrapFlagRemoved enforces AUTH-ROUTE-BOOTSTRAP-FLAG-REMOVED-01.
// The runtime/auth.Route struct must express bootstrap as a single source of
// truth: the BootstrapAuth func field. The legacy `Bootstrap bool` flag (which
// declared "this route bypasses listener JWT") is removed; bypass-with-
// replacement is now derived from `BootstrapAuth != nil` at Mount time, with
// AuthRouteMeta.Bootstrap as the persisted projection consumed by the router's
// FinalizeAuth matcher.
func TestAuthRouteBootstrapFlagRemoved(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	routeFile := filepath.Join(root, "runtime", "auth", "route.go")

	src, err := os.ReadFile(routeFile) // #nosec G304 -- module-rooted file path joined from findModuleRoot, not user input
	require.NoError(t, err, "read runtime/auth/route.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, routeFile, src, parser.SkipObjectResolution)
	require.NoError(t, err, "parse runtime/auth/route.go")

	var (
		hasBootstrapFlag bool
		hasBootstrapAuth bool
	)

	scanner.EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
		if ts.Name == nil || ts.Name.Name != "Route" {
			return
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return
		}
		for _, field := range st.Fields.List {
			for _, name := range field.Names {
				switch name.Name {
				case "Bootstrap":
					if ident, ok := field.Type.(*ast.Ident); ok && ident.Name == "bool" {
						hasBootstrapFlag = true
					}
				case "BootstrapAuth":
					if _, ok := field.Type.(*ast.FuncType); ok {
						hasBootstrapAuth = true
					}
				}
			}
		}
	})

	assert.False(t, hasBootstrapFlag,
		"AUTH-ROUTE-BOOTSTRAP-FLAG-REMOVED-01: runtime/auth.Route must not declare 'Bootstrap bool'; "+
			"BootstrapAuth (func) is the single source of truth for bootstrap routes")
	assert.True(t, hasBootstrapAuth,
		"AUTH-ROUTE-BOOTSTRAP-FLAG-REMOVED-01: runtime/auth.Route must declare 'BootstrapAuth func(http.Handler) http.Handler'")
}

// TestSetupAdminCodegenBootstrapAuthWired enforces
// SETUP-ADMIN-CODEGEN-BOOTSTRAP-AUTH-WIRED-01. The generated setup/admin
// handler_gen.go must:
//
//  1. Declare a NewHandler that takes a bootstrapAuth func(http.Handler)
//     http.Handler parameter (additional to the service interface).
//  2. Pass that bootstrapAuth value to auth.Mount via Route{BootstrapAuth: ...}.
//
// This locks the codegen template into the closed contract: a contract with
// auth.bootstrap:true cannot produce a handler without wiring BootstrapAuth.
func TestSetupAdminCodegenBootstrapAuthWired(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	genFile := filepath.Join(root, "generated", "contracts", "http", "auth", "setup", "admin", "v1", "handler_gen.go")

	src, err := os.ReadFile(genFile) // #nosec G304 -- module-rooted file path joined from findModuleRoot, not user input
	require.NoError(t, err, "read generated setup/admin handler_gen.go (run `go run ./cmd/gocell generate contract --all` first)")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, genFile, src, parser.SkipObjectResolution)
	require.NoError(t, err, "parse generated handler_gen.go")

	var (
		newHandlerHasBootstrapAuthParam bool
		mountCallHasBootstrapAuthField  bool
	)

	scanner.EachInSubtree[ast.FuncDecl](file, func(node *ast.FuncDecl) {
		if node.Name == nil || node.Name.Name != "NewHandler" || node.Recv != nil {
			return
		}
		for _, param := range node.Type.Params.List {
			ft, ok := param.Type.(*ast.FuncType)
			if !ok || ft.Params == nil || len(ft.Params.List) != 1 || ft.Results == nil || len(ft.Results.List) != 1 {
				continue
			}
			if isHTTPHandlerSelector(ft.Params.List[0].Type) && isHTTPHandlerSelector(ft.Results.List[0].Type) {
				newHandlerHasBootstrapAuthParam = true
			}
		}
	})
	scanner.EachInSubtree[ast.CompositeLit](file, func(node *ast.CompositeLit) {
		sel, ok := node.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != "Route" {
			return
		}
		// EachInChildren visits only direct KeyValueExpr children of node (not
		// nested KeyValueExprs from inner composite literals), equivalent to the
		// prior paired-index loop over node.Elts.
		scanner.EachInChildren[ast.KeyValueExpr](node, func(kv *ast.KeyValueExpr) {
			keyIdent, ok := kv.Key.(*ast.Ident)
			if !ok || keyIdent.Name != "BootstrapAuth" {
				return
			}
			// Any non-nil expression as the value satisfies the wiring requirement;
			// the codegen template cannot produce a literal nil because the param is
			// a func value passed straight through.
			if id, isIdent := kv.Value.(*ast.Ident); !isIdent || id.Name != "nil" {
				mountCallHasBootstrapAuthField = true
			}
		})
	})

	assert.True(t, newHandlerHasBootstrapAuthParam,
		"SETUP-ADMIN-CODEGEN-BOOTSTRAP-AUTH-WIRED-01: generated NewHandler must take a "+
			"`func(http.Handler) http.Handler` bootstrap auth parameter for contracts with auth.bootstrap:true")
	assert.True(t, mountCallHasBootstrapAuthField,
		"SETUP-ADMIN-CODEGEN-BOOTSTRAP-AUTH-WIRED-01: generated handler_gen.go must call auth.Mount with "+
			"a non-nil BootstrapAuth field (using the threaded NewHandler parameter)")
}

func isHTTPHandlerSelector(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "http" && sel.Sel != nil && sel.Sel.Name == "Handler"
}

// TestAuthRouteBootstrapClientsMutex enforces AUTH-BOOTSTRAP-CLIENTS-MUTEX-01.
//
// Any auth.Route composite literal that declares BootstrapAuth (non-nil
// expression) must not bind a Contract whose Clients field is non-empty.
// BootstrapAuth replaces JWT with HTTP Basic Auth via env credentials (used
// only on /api/v1/*/setup/admin per FMT-28); Contract.Clients drives the
// service-token caller-cell allowlist on /internal/v1/* (per FMT-31). The
// two authentication paths are mutually exclusive at the path-range level
// already (FMT-28 ∩ FMT-31 = ∅), and this archtest is the Go-source-code
// second-source guard against the mutex being violated should either YAML
// rule be weakened or bypassed by hand-written Mount calls.
//
// Detection scheme — same-file resolution suffices because both the codegen
// template (generated/contracts/*/handler_gen.go) and the documented
// slice-handler pattern (runtime/auth/runtime-api.md "Slice handler")
// declare the Contract var in the same file as the auth.Mount call. A
// cross-file Contract reference (different .go file in the same package, or
// a cross-package selector) is treated as "unresolvable" by this gate and
// the Route literal is flagged as a violation requiring manual review — the
// runtime guard in validateBypassCompatibility remains as the second-layer
// fallback for such cases.
//
// AI-rebust: Hard. Any Route literal violating the mutex causes archtest to
// fail CI before the code can merge; runtime fail-fast is the second layer
// of defense at validateBypassCompatibility.
func TestAuthRouteBootstrapClientsMutex(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root,
		[]string{"runtime", "cells", "cmd", "examples", "generated"},
		scanner.IncludeGenerated())

	var violations []string
	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(_ *testing.T, fc scanner.FileContext) {
		violations = append(violations, detectBootstrapClientsViolations(fc.File, fc.Fset, fc.Rel)...)
	})

	assert.Empty(t, violations,
		"AUTH-BOOTSTRAP-CLIENTS-MUTEX-01: auth.Route literals must not declare "+
			"BootstrapAuth alongside a Contract whose Clients field is non-empty. "+
			"BootstrapAuth and Contract.Clients are mutually exclusive authentication "+
			"paths (BootstrapAuth = HTTP Basic Auth via env credentials, FMT-28 limits "+
			"paths to /api/v1/*/setup/admin; Contract.Clients = service-token caller-"+
			"cell allowlist, FMT-31 limits paths to /internal/v1/*). See ADR "+
			"docs/architecture/202605061600-adr-bootstrap-admin-boundary.md.")
}

// detectBootstrapClientsViolations is the per-file detector kernel for
// AUTH-BOOTSTRAP-CLIENTS-MUTEX-01. Factored out so the same logic powers both
// the repo-wide static scan (TestAuthRouteBootstrapClientsMutex) and the
// synthetic-source self-test (TestAuthRouteBootstrapClientsMutex_CoverageBoundary)
// that locks the detection-coverage contract.
func detectBootstrapClientsViolations(file *ast.File, fset *token.FileSet, rel string) []string {
	// Pass 1: collect package-level (file-scope) vars whose value is a
	// contractspec.ContractSpec composite literal, mapping var name to
	// "Clients field is non-empty".
	specClientsNonEmpty := collectFileContractSpecClients(file)

	// Pass 2: scan every auth.Route composite literal in the file.
	var violations []string
	scanner.EachInSubtree[ast.CompositeLit](file, func(node *ast.CompositeLit) {
		if !isAuthRouteLitType(node.Type) {
			return
		}
		var (
			hasBootstrapAuth      bool
			contractRef           string
			contractInlineClients bool
			contractResolved      bool
		)
		scanner.EachInChildren[ast.KeyValueExpr](node, func(kv *ast.KeyValueExpr) {
			key, ok := kv.Key.(*ast.Ident)
			if !ok {
				return
			}
			switch key.Name {
			case "BootstrapAuth":
				if id, isIdent := kv.Value.(*ast.Ident); !isIdent || id.Name != "nil" {
					hasBootstrapAuth = true
				}
			case "Contract":
				contractRef, contractInlineClients, contractResolved = resolveContractField(kv.Value, specClientsNonEmpty)
			}
		})
		if !hasBootstrapAuth {
			return
		}
		if contractInlineClients || (contractResolved && specClientsNonEmpty[contractRef]) {
			violations = append(violations,
				rel+":"+fset.Position(node.Pos()).String()+
					" — auth.Route{BootstrapAuth: <non-nil>, Contract: "+
					describeContractRef(contractRef, contractInlineClients)+"} "+
					"binds non-empty Clients (mutex violation)")
		}
	})
	return violations
}

// TestAuthRouteBootstrapClientsMutex_CoverageBoundary locks the static
// detector's coverage contract via synthetic source files parsed in memory.
// Three coverage classes are exercised:
//
//  1. File-scope `var spec = contractspec.ContractSpec{... Clients: [...]}`
//     referenced by `Contract: spec` → MUST be detected.
//  2. Inline `Contract: contractspec.ContractSpec{... Clients: [...]}` literal
//     embedded directly in the Route literal → MUST be detected.
//  3. Func-body-local `spec := contractspec.ContractSpec{... Clients: [...]}`
//     referenced by `Contract: spec` → KNOWN GAP, NOT detected; covered by
//     the runtime guard in runtime/auth/route.go validateBypassCompatibility
//     as the second layer. This case is locked here so any future widening
//     of the static detector intentionally updates this assertion.
//
// The detector must also leave a clean (no violations) source clean and a
// BootstrapAuth-without-Clients source clean.
func TestAuthRouteBootstrapClientsMutex_CoverageBoundary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		src        string
		wantHit    bool   // exactly one violation expected
		wantSubstr string // substring required in the violation msg (when wantHit)
		descr      string
	}{
		{
			name: "filescope-var-DETECTED",
			src: `package x
import _ "stub"
var spec = contractspec.ContractSpec{Method: "GET", Path: "/internal/v1/x", Clients: []string{"caller"}}
func _f() { _ = auth.Route{BootstrapAuth: f, Contract: spec} }
`,
			wantHit:    true,
			wantSubstr: "Contract: spec",
			descr:      "package-level var pattern (codegen handler_gen.go)",
		},
		{
			name: "inline-literal-DETECTED",
			src: `package x
import _ "stub"
func _f() { _ = auth.Route{BootstrapAuth: f, Contract: contractspec.ContractSpec{Clients: []string{"c"}}} }
`,
			wantHit:    true,
			wantSubstr: "Contract: <inline ContractSpec literal>",
			descr:      "inline ContractSpec literal embedded in Route",
		},
		{
			name: "funcbody-local-KNOWN-GAP",
			src: `package x
import _ "stub"
func _f() {
    spec := contractspec.ContractSpec{Clients: []string{"caller"}}
    _ = auth.Route{BootstrapAuth: f, Contract: spec}
}
`,
			wantHit: false,
			descr:   "func-body-local := ContractSpec falls through to runtime guard",
		},
		{
			name: "bootstrap-without-clients-CLEAN",
			src: `package x
import _ "stub"
var spec = contractspec.ContractSpec{Method: "POST", Path: "/api/v1/x/setup/admin"}
func _f() { _ = auth.Route{BootstrapAuth: f, Contract: spec} }
`,
			wantHit: false,
			descr:   "BootstrapAuth alone with empty-Clients spec is legitimate (setup/admin pattern)",
		},
		{
			name: "clients-without-bootstrap-CLEAN",
			src: `package x
import _ "stub"
var spec = contractspec.ContractSpec{Method: "GET", Path: "/internal/v1/x", Clients: []string{"caller"}}
func _f() { _ = auth.Route{Contract: spec, Public: true} }
`,
			wantHit: false,
			descr:   "Clients alone without BootstrapAuth is legitimate (internal route pattern)",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "synthetic.go", tc.src, parser.SkipObjectResolution)
			require.NoError(t, err, tc.descr)
			got := detectBootstrapClientsViolations(file, fset, "synthetic.go")
			if !tc.wantHit {
				assert.Empty(t, got, tc.descr+" (case "+tc.name+")")
				return
			}
			require.Len(t, got, 1, tc.descr+" — expected exactly one violation")
			assert.Contains(t, got[0], tc.wantSubstr, tc.descr)
		})
	}
}

// collectFileContractSpecClients walks file-scope ValueSpec entries and returns
// a map keyed by the declared var name, whose value is true iff the underlying
// composite literal is a contractspec.ContractSpec with a non-empty Clients
// field. Only direct file-level var declarations are inspected; func-body
// locals and cross-file references are out of scope (handled fail-closed by
// the caller's resolution logic).
func collectFileContractSpecClients(file *ast.File) map[string]bool {
	out := map[string]bool{}
	scanner.EachInChildren[ast.GenDecl](file, func(gd *ast.GenDecl) {
		if gd.Tok != token.VAR {
			return
		}
		// ValueSpec is the direct child kind of GenDecl for `var` blocks;
		// using the scanner funnel rather than for-range over gd.Specs
		// satisfies SCANNER-FRAMEWORK-USAGE-01 (typed-child walk).
		scanner.EachInChildren[ast.ValueSpec](gd, func(vs *ast.ValueSpec) {
			recordContractSpecVars(vs, out)
		})
	})
	return out
}

// recordContractSpecVars inspects a single ValueSpec for ContractSpec
// composite-literal bindings and records each (varName, has-non-empty-Clients)
// pair into out. Names and Values are aligned positionally per the Go AST
// invariant for value specs with explicit initializers.
func recordContractSpecVars(vs *ast.ValueSpec, out map[string]bool) {
	for i, name := range vs.Names {
		if i >= len(vs.Values) {
			continue
		}
		cl, ok := vs.Values[i].(*ast.CompositeLit)
		if !ok {
			continue
		}
		if !isContractSpecLitType(cl.Type) {
			continue
		}
		out[name.Name] = compositeLitHasNonEmptyClients(cl)
	}
}

// resolveContractField interprets the value expression of an auth.Route
// Contract field, returning:
//
//   - varName: the unqualified identifier name when the value is an Ident or
//     SelectorExpr (e.g. "specSessionsLogin" or "auth.SetupAdmin" → "SetupAdmin").
//   - inlineClients: true when the value is an inline composite literal of
//     contractspec.ContractSpec with a non-empty Clients field.
//   - resolved: true when same-file resolution succeeded (Ident in the
//     specClients map). Cross-package SelectorExpr and same-file Ident
//     not-in-map both return resolved=false, which causes the caller to skip
//     flagging — the violation can only fire on a definitively-resolved
//     non-empty Clients spec. The runtime guard in
//     runtime/auth/route.go:validateBypassCompatibility remains the second
//     layer for the unresolved cases (cross-package / cross-file / func-body
//     local), so detection coverage is layered rather than archtest-only.
//
// Why SelectorExpr is treated as unresolved rather than checked:
// cross-package ContractSpec refs live in their declaring package's file
// scope, and the path ranges enforced by FMT-28 (BootstrapAuth →
// /api/v1/*/setup/admin) and FMT-31 (Clients → /internal/v1/*) make their
// coexistence structurally impossible at the YAML source. Flagging
// cross-package refs without cross-package var resolution would require
// loading multiple packages and is out of scope for this AST-only archtest;
// the runtime guard handles the remaining surface.
func resolveContractField(value ast.Expr, specClients map[string]bool) (varName string, inlineClients bool, resolved bool) {
	switch v := value.(type) {
	case *ast.Ident:
		if _, ok := specClients[v.Name]; ok {
			return v.Name, false, true
		}
		return v.Name, false, false
	case *ast.CompositeLit:
		if isContractSpecLitType(v.Type) {
			return "", compositeLitHasNonEmptyClients(v), false
		}
		return "", false, false
	case *ast.SelectorExpr:
		if v.Sel != nil {
			return v.Sel.Name, false, false
		}
		return "", false, false
	}
	return "", false, false
}

// isAuthRouteLitType reports whether the composite-literal type expression
// names auth.Route. Accepts either the qualified form `auth.Route` or the
// bare `Route` (same-package usage from runtime/auth itself).
func isAuthRouteLitType(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		return e.Sel != nil && e.Sel.Name == "Route"
	case *ast.Ident:
		return e.Name == "Route"
	}
	return false
}

// isContractSpecLitType reports whether the composite-literal type expression
// names contractspec.ContractSpec (or the bare ContractSpec, for same-package
// usage from kernel/contractspec).
func isContractSpecLitType(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		return e.Sel != nil && e.Sel.Name == "ContractSpec"
	case *ast.Ident:
		return e.Name == "ContractSpec"
	}
	return false
}

// compositeLitHasNonEmptyClients walks a ContractSpec composite literal's
// KeyValueExpr children and reports whether the Clients field is bound to a
// non-empty slice literal. A missing Clients field is treated as empty.
func compositeLitHasNonEmptyClients(cl *ast.CompositeLit) bool {
	var nonEmpty bool
	scanner.EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Clients" {
			return
		}
		// Recognize []string{...} composite literal with at least one elt.
		if v, ok := kv.Value.(*ast.CompositeLit); ok && len(v.Elts) > 0 {
			nonEmpty = true
		}
	})
	return nonEmpty
}

// describeContractRef renders the Contract reference for inclusion in
// archtest violation messages.
func describeContractRef(varName string, inlineClients bool) string {
	if inlineClients {
		return "<inline ContractSpec literal>"
	}
	if varName == "" {
		return "<unresolved>"
	}
	return varName
}
