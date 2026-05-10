// INVARIANT: LISTENER-DX-01: deleted listener option APIs and legacy auth.Route Delegated surface must not be reintroduced
package archtest

// listener_dx_test.go enforces A52 LISTENER-DX-UNIFY anti-regression guards.
//
// The rule is intentionally narrow:
//   - production Go must not reintroduce deleted listener option APIs;
//   - production Go must not reintroduce the old auth.Route Delegated surface;
//   - owner API signatures for RouteGroup.Register and auth.Mount stay aligned;
//   - active docs/godoc must not show old listener APIs, Delegated examples, or
//     the legacy single-mux route registration surface.
//
// Historical provenance remains allowed in docs/backlog.md, docs/plans/**,
// docs/reviews/**, docs/archive/**, docs/backlog/archive/**, and CHANGELOG.md.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const ruleListenerDXA52 = "LISTENER-DX-A52"

var oldListenerAPIIdents = map[string]struct{}{
	"WithHTTPAddr":         {},
	"WithHTTPPrimaryAddr":  {},
	"WithHTTPInternalAddr": {},
	"WithPrimaryListener":  {},
	"WithInternalListener": {},
}

var oldRouteSurfaceDocTerms = []string{
	"cell.HTTPRegistrar",
	"HTTPRegistrar",
	"WithInternalMiddleware",
	"Cell.RegisterRoutes",
	"Cell RegisterRoutes",
	"cell.RegisterRoutes",
	"func (c *MyCell) RegisterRoutes",
	"RegisterRoutes(mux cell.RouteMux)",
	"publicMux",
	"internalMux",
}

var activeDocForbiddenTerms = []string{
	"WithHTTPAddr",
	"WithHTTPPrimaryAddr",
	"WithHTTPInternalAddr",
	"WithPrimaryListener",
	"WithInternalListener",
	"Delegated",
}

var productionForbiddenSurfaceTerms = []string{
	"Delegated",
	"WithDelegatedMatcher",
}

func TestListenerDXA52Guard(t *testing.T) {
	root := findModuleRoot(t)

	t.Run("deleted_listener_options_not_reintroduced", func(t *testing.T) {
		files, err := listenerDXProductionGoFiles(root)
		require.NoError(t, err)
		var violations []string
		for _, file := range files {
			violations = append(violations, oldListenerAPIIdentViolations(t, root, file)...)
		}
		assert.Empty(t, violations, "%s: deleted listener option APIs must not reappear:\n%s",
			ruleListenerDXA52, strings.Join(violations, "\n"))
	})

	t.Run("delegated_route_surface_not_reintroduced", func(t *testing.T) {
		files, err := listenerDXProductionGoFiles(root)
		require.NoError(t, err)
		var violations []string
		for _, file := range files {
			violations = append(violations, delegatedRouteFieldViolations(t, root, file)...)
			violations = append(violations, forbiddenProductionSurfaceViolations(t, root, file)...)
		}
		assert.Empty(t, violations, "%s: auth.Route Delegated surface must not reappear:\n%s",
			ruleListenerDXA52, strings.Join(violations, "\n"))
	})

	t.Run("owner_surface_signatures_stay_current", func(t *testing.T) {
		var violations []string
		violations = append(violations, routeGroupRegisterSignatureViolations(t, root)...)
		violations = append(violations, authMountSignatureViolations(t, root)...)
		assert.Empty(t, violations, "%s: listener DX owner API signatures drifted:\n%s",
			ruleListenerDXA52, strings.Join(violations, "\n"))
	})

	t.Run("active_docs_do_not_show_deleted_listener_surface", func(t *testing.T) {
		files := listenerDXActiveDocs(t, root)
		var violations []string
		for _, file := range files {
			violations = append(violations, activeDocTermViolations(t, root, file)...)
		}
		goFiles, err := listenerDXProductionGoFiles(root)
		require.NoError(t, err)
		for _, file := range goFiles {
			violations = append(violations, activeGoCommentTermViolations(t, root, file)...)
		}
		assert.Empty(t, violations, "%s: active docs/godoc must not show deleted listener APIs or Delegated examples:\n%s",
			ruleListenerDXA52, strings.Join(violations, "\n"))
	})
}

func listenerDXProductionGoFiles(root string) ([]string, error) {
	scope := scanner.ModuleScope(root)
	files, err := scope.Files()
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// listenerDXActiveDocSkipDirs are directory base-names skipped when collecting
// active doc files. Matches the scanner framework's defaultSkipDirs plus
// the original walk's exclusions.
// listenerDXActiveDocs collects absolute paths of "active" documentation
// surfaces under root: every .md file plus every doc.go file, excluding
// historical archives via listenerDXDocExcluded. .md is funneled through
// scanner.EachContentFile (non-Go content), doc.go through scanner.EachFile
// (Go AST scope; only the filename is needed but the file is still parsed,
// which is fine — there's at most ~50 doc.go files repo-wide).
//
// Default scanner skipDirs (vendor / testdata / worktrees / generated /
// .git / node_modules) supersede the previous custom set of {.git, vendor,
// testdata, worktrees}; the additional generated/ + node_modules/ exclusions
// are strict improvements (no doc.go or curated .md should live there).
func listenerDXActiveDocs(t *testing.T, root string) []string {
	t.Helper()
	var files []string

	mdScope := scanner.ModuleScope(root)
	scanner.EachContentFile(t, mdScope, []string{".md"}, func(_ *testing.T, fc scanner.ContentContext) {
		if listenerDXDocExcluded(fc.Rel) {
			return
		}
		files = append(files, fc.AbsPath)
	})

	goScope := scanner.ModuleScope(root)
	scanner.EachFile(t, goScope, parser.SkipObjectResolution, func(_ *testing.T, fc scanner.FileContext) {
		if filepath.Base(fc.AbsPath) != "doc.go" {
			return
		}
		if listenerDXDocExcluded(fc.Rel) {
			return
		}
		files = append(files, fc.AbsPath)
	})

	sort.Strings(files)
	return files
}

func listenerDXDocExcluded(rel string) bool {
	if rel == "CHANGELOG.md" || rel == "docs/backlog.md" {
		return true
	}
	for _, prefix := range []string{
		"docs/plans/",
		"docs/reviews/",
		"docs/archive/",
		// docs/backlog/archive/** — historical snapshot of pre-framework
		// backlog files (5 sources @ 18a06ab7); listener API references
		// retained verbatim for traceability.
		"docs/backlog/archive/",
	} {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

func oldListenerAPIIdentViolations(t *testing.T, root, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	var violations []string
	scanner.EachNode[ast.Ident](file, func(id *ast.Ident) {
		if _, forbidden := oldListenerAPIIdents[id.Name]; !forbidden {
			return
		}
		violations = append(violations, listenerDXViolation(root, path, fset.Position(id.Pos()).Line,
			fmt.Sprintf("deleted listener option identifier %q", id.Name)))
	})
	return violations
}

func delegatedRouteFieldViolations(t *testing.T, root, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	var violations []string
	scanner.EachNode[ast.KeyValueExpr](file, func(kv *ast.KeyValueExpr) {
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Delegated" {
			return
		}
		violations = append(violations, listenerDXViolation(root, path, fset.Position(key.Pos()).Line,
			"Delegated key in composite literal"))
	})
	return violations
}

func forbiddenProductionSurfaceViolations(t *testing.T, root, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	require.NoError(t, err)

	var violations []string
	scanner.EachNode[ast.Ident](file, func(id *ast.Ident) {
		for _, term := range productionForbiddenSurfaceTerms {
			if strings.Contains(id.Name, term) {
				violations = append(violations, listenerDXViolation(root, path, fset.Position(id.Pos()).Line,
					fmt.Sprintf("production identifier contains deleted surface %q", term)))
			}
		}
	})
	for _, group := range file.Comments {
		for _, comment := range group.List {
			for _, term := range productionForbiddenSurfaceTerms {
				if strings.Contains(comment.Text, term) {
					violations = append(violations, listenerDXViolation(root, path, fset.Position(comment.Pos()).Line,
						fmt.Sprintf("production comment contains deleted surface %q", term)))
				}
			}
		}
	}
	return violations
}

func routeGroupRegisterSignatureViolations(t *testing.T, root string) []string {
	t.Helper()
	// RouteGroup struct is defined in registry.go (merged in batch 1/4).
	path := filepath.Join(root, "kernel", "cell", "registry.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	var result []string
	found := false
	scanner.EachNode[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
		if found || ts.Name.Name != "RouteGroup" {
			return
		}
		found = true
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			result = []string{listenerDXViolation(root, path, fset.Position(ts.Pos()).Line, "RouteGroup is no longer a struct")}
			return
		}
		for _, field := range st.Fields.List {
			if len(field.Names) != 1 || field.Names[0].Name != "Register" {
				continue
			}
			fn, ok := field.Type.(*ast.FuncType)
			if !ok {
				result = []string{listenerDXViolation(root, path, fset.Position(field.Pos()).Line, "RouteGroup.Register is not a func")}
				return
			}
			if !listenerDXFuncHasOneParam(fn, "RouteMux") || !listenerDXFuncReturnsOnlyError(fn) {
				result = []string{listenerDXViolation(root, path, fset.Position(field.Pos()).Line,
					"RouteGroup.Register must be func(mux RouteMux) error")}
				return
			}
			return
		}
		result = []string{listenerDXViolation(root, path, fset.Position(ts.Pos()).Line, "RouteGroup.Register field missing")}
	})
	if !found {
		return []string{listenerDXViolation(root, path, 1, "RouteGroup type missing")}
	}
	return result
}

func authMountSignatureViolations(t *testing.T, root string) []string {
	t.Helper()
	path := filepath.Join(root, "runtime", "auth", "route.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	var result []string
	found := false
	scanner.EachNode[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if found || fn.Name.Name != "Mount" {
			return
		}
		found = true
		if !listenerDXFuncHasParams(fn.Type, "cell.RouteHandler", "Route") {
			result = []string{listenerDXViolation(root, path, fset.Position(fn.Pos()).Line,
				"auth.Mount must be func(mux cell.RouteHandler, r Route) error")}
			return
		}
		if !listenerDXFuncReturnsOnlyError(fn.Type) {
			result = []string{listenerDXViolation(root, path, fset.Position(fn.Pos()).Line, "auth.Mount must return error")}
		}
	})
	if !found {
		return []string{listenerDXViolation(root, path, 1, "auth.Mount function missing")}
	}
	return result
}

func listenerDXFuncHasOneParam(fn *ast.FuncType, wantType string) bool {
	return listenerDXFuncHasParams(fn, wantType)
}

func listenerDXFuncHasParams(fn *ast.FuncType, wantTypes ...string) bool {
	gotTypes := listenerDXFuncParamTypes(fn)
	if len(gotTypes) != len(wantTypes) {
		return false
	}
	for i, want := range wantTypes {
		if gotTypes[i] != want {
			return false
		}
	}
	return true
}

func listenerDXFuncParamTypes(fn *ast.FuncType) []string {
	if fn.Params == nil {
		return nil
	}
	var gotTypes []string
	for _, field := range fn.Params.List {
		count := len(field.Names)
		if count == 0 {
			count = 1
		}
		for i := 0; i < count; i++ {
			gotTypes = append(gotTypes, listenerDXExprName(field.Type))
		}
	}
	return gotTypes
}

func listenerDXFuncReturnsOnlyError(fn *ast.FuncType) bool {
	return fn.Results != nil && len(fn.Results.List) == 1 && listenerDXExprName(fn.Results.List[0].Type) == "error"
}

func listenerDXExprName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return listenerDXExprName(e.X) + "." + e.Sel.Name
	default:
		return ""
	}
}

func activeDocTermViolations(t *testing.T, root, path string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	require.NoError(t, err)
	lines := strings.Split(string(data), "\n")
	var violations []string
	terms := listenerDXForbiddenDocTerms()
	for i, line := range lines {
		for _, term := range terms {
			if strings.Contains(line, term) {
				violations = append(violations, listenerDXViolation(root, path, i+1,
					fmt.Sprintf("active docs/godoc contains %q", term)))
			}
		}
	}
	return violations
}

func activeGoCommentTermViolations(t *testing.T, root, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	require.NoError(t, err)

	var violations []string
	terms := listenerDXForbiddenDocTerms()
	for _, group := range file.Comments {
		for _, comment := range group.List {
			for _, term := range terms {
				if strings.Contains(comment.Text, term) {
					violations = append(violations, listenerDXViolation(root, path, fset.Position(comment.Pos()).Line,
						fmt.Sprintf("active godoc/comment contains %q", term)))
				}
			}
		}
	}
	return violations
}

func listenerDXForbiddenDocTerms() []string {
	terms := make([]string, 0, len(activeDocForbiddenTerms)+len(oldRouteSurfaceDocTerms))
	terms = append(terms, activeDocForbiddenTerms...)
	terms = append(terms, oldRouteSurfaceDocTerms...)
	return terms
}

func listenerDXViolation(root, path string, line int, msg string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	return fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), line, msg)
}
