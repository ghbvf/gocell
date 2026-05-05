// Setup-admin bootstrap closure invariants — codified into archtest after the
// security boundary collapse described in
// docs/architecture/202605061600-adr-bootstrap-admin-boundary.md §D1 + §D10.
//
// The setup/admin endpoint must be a closed contract: the codegen output, the
// runtime/auth.Route shape, the cell-side handler, and the composition root
// all agree that BootstrapAuth is a required dependency. There is no
// "declared bootstrap but no auth wired" intermediate state.
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

package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	cellsDir := filepath.Join(root, "cells")

	fset := token.NewFileSet()
	var violations []string

	walkErr := filepath.Walk(cellsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// #nosec G304 -- path comes from filepath.Walk under findModuleRoot, not user input.
		src, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		// Cheap filter: skip files that don't reference the symbol literally.
		if !strings.Contains(string(src), "RouteMux") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			st, ok := n.(*ast.StructType)
			if !ok || st.Fields == nil {
				return true
			}
			for _, field := range st.Fields.List {
				// Embedded field has no Names.
				if len(field.Names) != 0 {
					continue
				}
				if exprNamesRouteMux(field.Type) {
					rel, _ := filepath.Rel(root, path)
					violations = append(violations, rel+":"+fset.Position(field.Pos()).String())
				}
			}
			return true
		})
		return nil
	})
	require.NoError(t, walkErr, "walk cells/")

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

	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok || ts.Name == nil || ts.Name.Name != "Route" {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			return false
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
		return false
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

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.FuncDecl:
			if node.Name == nil || node.Name.Name != "NewHandler" || node.Recv != nil {
				return true
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
		case *ast.CompositeLit:
			sel, ok := node.Type.(*ast.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Name != "Route" {
				return true
			}
			for _, elt := range node.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				keyIdent, ok := kv.Key.(*ast.Ident)
				if !ok || keyIdent.Name != "BootstrapAuth" {
					continue
				}
				// Any non-nil expression as the value satisfies the wiring requirement;
				// the codegen template cannot produce a literal nil because the param is
				// a func value passed straight through.
				if _, isNil := kv.Value.(*ast.Ident); !isNil || kv.Value.(*ast.Ident).Name != "nil" {
					mountCallHasBootstrapAuthField = true
				}
			}
		}
		return true
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
