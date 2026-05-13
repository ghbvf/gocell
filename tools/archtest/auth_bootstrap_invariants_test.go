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
//     paths are mutually exclusive; this archtest is the static (Hard) gate
//     that fails CI before the misconfigured code can merge. The runtime
//     fail-fast in runtime/auth/route.go validateBypassCompatibility is a
//     depth-in-defense second layer, not a primary line of detection.
//
//     AI-rebust = Hard: detection runs over the full production package set
//     with type-checked Defs/Uses, so a Contract field that resolves to a
//     *types.Var (file-scope var, func-body `:=` local, or cross-package
//     SelectorExpr) is compared against the same canonical key regardless of
//     AST shape. The detector has 0 KNOWN-GAP cases — see the companion
//     fixture under tools/archtest/internal/authroutemutexfixture/ which
//     exercises all four reachable Contract-expression shapes.

package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
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
// already (FMT-28 ∩ FMT-31 = ∅); this archtest is the Go-source-code
// primary guard against the mutex being violated should either YAML rule
// be weakened or bypassed by hand-written Mount calls.
//
// Detection scheme — type-aware: scanAuthRouteBootstrapClientsViolations
// loads the production package set via typeseval.LoadProductionPackages and
// walks every auth.Route composite literal under that universe. The Route
// type and ContractSpec type are matched by canonical *types.Named (package
// path + name), so import aliases cannot bypass detection. The Contract
// field value is resolved via *types.Info.Uses / Defs to a *types.Var key,
// then looked up against a pre-built map of every ContractSpec composite
// literal in the module (both `var X = ...` and `X := ...` forms). This
// closes the cross-package SelectorExpr / func-body-local := KNOWN-GAP that
// the prior AST-only detector relied on the runtime guard to catch.
//
// AI-rebust: Hard. Coverage is locked at the *types.Var key — picking a
// different AST shape (file-scope var / inline literal / `:=` local /
// cross-package SelectorExpr) does not change the resolved key, so an AI
// co-author cannot pick a shape that bypasses the static gate. The runtime
// fail-fast in runtime/auth/route.go validateBypassCompatibility remains as
// depth-in-defense (e.g. for ContractSpec values constructed dynamically
// from non-literal sources, which the static gate by design ignores).
func TestAuthRouteBootstrapClientsMutex(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modulePath := readModulePath(t, root)

	resolver, err := typeseval.LoadProductionPackages(root, modulePath, false, nil)
	require.NoError(t, err)

	violations := scanAuthRouteBootstrapClientsViolations(root, modulePath, resolver.Production())

	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"AUTH-BOOTSTRAP-CLIENTS-MUTEX-01: auth.Route literals must not declare "+
			"BootstrapAuth alongside a Contract whose Clients field is non-empty. "+
			"BootstrapAuth and Contract.Clients are mutually exclusive authentication "+
			"paths (BootstrapAuth = HTTP Basic Auth via env credentials, FMT-28 limits "+
			"paths to /api/v1/*/setup/admin; Contract.Clients = service-token caller-"+
			"cell allowlist, FMT-31 limits paths to /internal/v1/*). See ADR "+
			"docs/architecture/202605061600-adr-bootstrap-admin-boundary.md.")
}

// TestAuthRouteBootstrapClientsMutex_FixturePattern loads the build-tag-gated
// authroutemutexfixture package and asserts the scanner reports every
// reachable Contract-expression shape that binds non-empty Clients alongside
// non-nil BootstrapAuth:
//
//  1. file-scope-var-DETECTED: `var spec = ContractSpec{... Clients}` +
//     `auth.Route{BootstrapAuth: ..., Contract: spec}` (same package).
//  2. inline-literal-DETECTED: inline ContractSpec composite literal embedded
//     directly in the Route literal.
//  3. funcbody-local-DETECTED: `spec := ContractSpec{... Clients}` inside a
//     function body + `auth.Route{... Contract: spec}`.
//  4. cross-package-SelectorExpr-DETECTED: ContractSpec var declared in a
//     sibling package + `auth.Route{... Contract: siblingpkg.WithClients}`.
//
// The fixture also contains two clean shapes (BootstrapAuth-without-Clients
// and Clients-without-BootstrapAuth) that must NOT trigger; the test asserts
// the exact hit count is 4 to prevent false negatives in either direction.
//
// Per ai-collab.md §"Hard 范本": the fixture is a real Go package loaded via
// packages.Load with the archtest_fixture build tag. Bypassing this test
// requires modifying real source code; the detector cannot drift back to
// AST-only without breaking the fixture assertions.
func TestAuthRouteBootstrapClientsMutex_FixturePattern(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	modulePath := readModulePath(t, root)

	resolver, err := typeseval.SharedResolver(root, false, []string{"archtest_fixture"},
		"./tools/archtest/internal/authroutemutexfixture/...")
	require.NoError(t, err)

	violations := scanAuthRouteBootstrapClientsViolations(root, modulePath, resolver.Packages())

	for _, v := range violations {
		t.Log(v)
	}
	require.Len(t, violations, 4,
		"fixture must yield exactly 4 violations (file-scope-var / inline-literal / "+
			"funcbody-local / cross-package-SelectorExpr); clean cases must not trigger")

	wantSubstr := []string{
		"Contract: specFileScope",
		"Contract: <inline ContractSpec literal>",
		"Contract: specLocal",
		"Contract: spec.WithClients",
	}
	joined := ""
	for _, v := range violations {
		joined += v + "\n"
	}
	for _, want := range wantSubstr {
		assert.Contains(t, joined, want, "fixture violations must include %q", want)
	}
}

// scanAuthRouteBootstrapClientsViolations walks every auth.Route composite
// literal in pkgs and reports those whose BootstrapAuth field is non-nil
// while their Contract field resolves to a ContractSpec with non-empty
// Clients. Resolution is type-aware:
//
//   - Route and ContractSpec are matched by *types.Named (package path +
//     name), so import aliases do not bypass detection.
//   - Contract field values that are *ast.Ident or *ast.SelectorExpr are
//     resolved via *types.Info.Uses to a *types.Var; the var is looked up
//     against a map built from a first-pass scan of every ContractSpec
//     composite literal in pkgs (both ValueSpec and AssignStmt forms).
//   - Inline *ast.CompositeLit Contract values are evaluated directly via
//     compositeLitHasNonEmptyClients.
//
// Non-CompositeLit, non-Ident, non-SelectorExpr Contract expressions (e.g.
// function call returning ContractSpec) are not statically reachable and
// fall through to the runtime guard at validateBypassCompatibility.
func scanAuthRouteBootstrapClientsViolations(root, modulePath string, pkgs []*packages.Package) []string {
	routeTypePath := modulePath + "/runtime/auth"
	specTypePath := modulePath + "/kernel/contractspec"

	specVars := collectContractSpecVarsTyped(pkgs, specTypePath)

	var violations []string
	for _, pkg := range pkgs {
		if pkg == nil || pkg.TypesInfo == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			absPath := pkg.Fset.Position(file.Pos()).Filename
			rel, err := filepath.Rel(root, absPath)
			if err != nil {
				continue
			}
			relSlash := filepath.ToSlash(rel)
			scanner.EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
				if !isNamedCompositeLit(pkg.TypesInfo, cl, routeTypePath, "Route") {
					return
				}
				hasBootstrapAuth, contractExpr := authRouteBootstrapAndContract(cl)
				if !hasBootstrapAuth || contractExpr == nil {
					return
				}
				hasClients, desc := resolveContractClientsTyped(pkg.TypesInfo, contractExpr, specVars, specTypePath)
				if !hasClients {
					return
				}
				violations = append(violations, fmt.Sprintf(
					"%s:%d — auth.Route{BootstrapAuth: <non-nil>, Contract: %s} binds non-empty Clients (mutex violation)",
					relSlash, pkg.Fset.Position(cl.Pos()).Line, desc))
			})
		}
	}
	return violations
}

// collectContractSpecVarsTyped scans pkgs for every contractspec.ContractSpec
// composite literal that is bound to a variable (either `var X = ...` or
// `X := ...`) and returns a map keyed by the canonical *types.Var, value
// true iff the literal has a non-empty Clients field.
//
// Both ValueSpec and AssignStmt forms produce *types.Var entries via
// pkg.TypesInfo.Defs, so the same key shape is used regardless of AST
// container. Cross-package references resolve to the same *types.Var via
// pkg.TypesInfo.Uses on the consumer side.
func collectContractSpecVarsTyped(pkgs []*packages.Package, specTypePath string) map[*types.Var]bool {
	out := map[*types.Var]bool{}
	for _, pkg := range pkgs {
		if pkg == nil || pkg.TypesInfo == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			scanner.EachInSubtree[ast.ValueSpec](file, func(vs *ast.ValueSpec) {
				recordValueSpecContractVars(pkg.TypesInfo, vs, specTypePath, out)
			})
			scanner.EachInSubtree[ast.AssignStmt](file, func(as *ast.AssignStmt) {
				if as.Tok != token.DEFINE {
					return
				}
				recordAssignStmtContractVars(pkg.TypesInfo, as, specTypePath, out)
			})
		}
	}
	return out
}

func recordValueSpecContractVars(info *types.Info, vs *ast.ValueSpec, specTypePath string, out map[*types.Var]bool) {
	for i, name := range vs.Names {
		if i >= len(vs.Values) {
			continue
		}
		cl, ok := vs.Values[i].(*ast.CompositeLit)
		if !ok {
			continue
		}
		if !isNamedCompositeLit(info, cl, specTypePath, "ContractSpec") {
			continue
		}
		obj := info.Defs[name]
		v, ok := obj.(*types.Var)
		if !ok || v == nil {
			continue
		}
		out[v] = compositeLitHasNonEmptyClients(cl)
	}
}

func recordAssignStmtContractVars(info *types.Info, as *ast.AssignStmt, specTypePath string, out map[*types.Var]bool) {
	// Paired-index form with companion index access on a different slice
	// (`as.Lhs[i]` paired with `as.Rhs[i]`) — SCANNER-FRAMEWORK-USAGE-01 form
	// (c) exempts this shape because the LHS/RHS pairing semantics cannot be
	// expressed by scanner.EachInChildren. Type extraction is funneled
	// through exprToIdent / exprToCompositeLit helpers so no TypeAssertExpr
	// appears in the loop body (precedent: production_loader_funnel_test.go
	// assignLhsHasIdentNamed).
	for i := range as.Lhs {
		if i >= len(as.Rhs) {
			continue
		}
		cl := exprToCompositeLit(as.Rhs[i])
		if cl == nil {
			continue
		}
		if !isNamedCompositeLit(info, cl, specTypePath, "ContractSpec") {
			continue
		}
		ident := exprToIdent(as.Lhs[i])
		if ident == nil {
			continue
		}
		obj := info.Defs[ident]
		v, ok := obj.(*types.Var)
		if !ok || v == nil {
			continue
		}
		out[v] = compositeLitHasNonEmptyClients(cl)
	}
}

// exprToCompositeLit casts e to *ast.CompositeLit, returning nil if not a
// composite literal. Mirrors exprToIdent (security_defaults_test.go) so the
// type assertion is funneled outside the loop body and the call site stays
// compliant with SCANNER-FRAMEWORK-USAGE-01.
func exprToCompositeLit(e ast.Expr) *ast.CompositeLit {
	cl, _ := e.(*ast.CompositeLit)
	return cl
}

// authRouteBootstrapAndContract extracts the BootstrapAuth presence flag and
// the Contract field expression from an auth.Route composite literal.
// Returns hasBootstrapAuth=false when the field is absent or set to literal
// nil; returns contractExpr=nil when the field is absent.
func authRouteBootstrapAndContract(cl *ast.CompositeLit) (hasBootstrapAuth bool, contractExpr ast.Expr) {
	scanner.EachInChildren[ast.KeyValueExpr](cl, func(kv *ast.KeyValueExpr) {
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
			contractExpr = kv.Value
		}
	})
	return hasBootstrapAuth, contractExpr
}

// resolveContractClientsTyped reports whether expr resolves to a
// ContractSpec value (or composite literal) with non-empty Clients, and
// returns a short human-readable description for the violation message.
//
// Handled shapes:
//
//   - *ast.CompositeLit of contractspec.ContractSpec: inspect directly.
//   - *ast.Ident: lookup info.Uses → *types.Var in specVars map.
//   - *ast.SelectorExpr: lookup info.Uses[sel.Sel] → *types.Var in specVars
//     map (resolves cross-package package-level vars to their canonical
//     *types.Var, so import aliases do not affect identity).
//
// Other shapes (function-call result, conversion expression, …) return
// false — they fall through to the runtime guard at registration time.
func resolveContractClientsTyped(info *types.Info, expr ast.Expr, specVars map[*types.Var]bool, specTypePath string) (bool, string) {
	switch v := expr.(type) {
	case *ast.CompositeLit:
		if !isNamedCompositeLit(info, v, specTypePath, "ContractSpec") {
			return false, "<non-ContractSpec literal>"
		}
		return compositeLitHasNonEmptyClients(v), "<inline ContractSpec literal>"
	case *ast.Ident:
		obj := info.Uses[v]
		if obj == nil {
			obj = info.Defs[v]
		}
		tv, ok := obj.(*types.Var)
		if !ok || tv == nil {
			return false, v.Name
		}
		return specVars[tv], v.Name
	case *ast.SelectorExpr:
		if v.Sel == nil {
			return false, "<malformed SelectorExpr>"
		}
		obj := info.Uses[v.Sel]
		if obj == nil {
			obj = info.Defs[v.Sel]
		}
		tv, ok := obj.(*types.Var)
		if !ok || tv == nil {
			return false, v.Sel.Name
		}
		desc := v.Sel.Name
		if x, ok := v.X.(*ast.Ident); ok {
			desc = x.Name + "." + v.Sel.Name
		}
		return specVars[tv], desc
	}
	return false, "<unrecognized Contract expression>"
}

// isNamedCompositeLit reports whether cl's declared type resolves (via
// pkg.TypesInfo) to a *types.Named whose owning package path equals
// wantPkgPath and whose type name equals wantName. Matching by canonical
// types path defeats import aliases (e.g. `import altauth "<module>/runtime/auth";
// altauth.Route{...}` still resolves to the same *types.Named).
func isNamedCompositeLit(info *types.Info, cl *ast.CompositeLit, wantPkgPath, wantName string) bool {
	if info == nil || cl == nil || cl.Type == nil {
		return false
	}
	t := info.TypeOf(cl.Type)
	if t == nil {
		return false
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	return obj.Pkg().Path() == wantPkgPath && obj.Name() == wantName
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
