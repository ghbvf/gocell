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
// docs/reviews/**, docs/archive/**, and CHANGELOG.md.

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
		files, err := listenerDXActiveDocs(root)
		require.NoError(t, err)
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
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata", "generated", "worktrees":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files, err
}

func listenerDXActiveDocs(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "vendor", "testdata", "worktrees":
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if listenerDXDocExcluded(rel) {
			return nil
		}
		if strings.HasSuffix(rel, ".md") || strings.HasSuffix(rel, "/doc.go") {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func listenerDXDocExcluded(rel string) bool {
	if rel == "CHANGELOG.md" || rel == "docs/backlog.md" {
		return true
	}
	for _, prefix := range []string{
		"docs/plans/",
		"docs/reviews/",
		"docs/archive/",
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
	ast.Inspect(file, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if _, forbidden := oldListenerAPIIdents[id.Name]; !forbidden {
			return true
		}
		violations = append(violations, listenerDXViolation(root, path, fset.Position(id.Pos()).Line,
			fmt.Sprintf("deleted listener option identifier %q", id.Name)))
		return true
	})
	return violations
}

func delegatedRouteFieldViolations(t *testing.T, root, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Delegated" {
			return true
		}
		violations = append(violations, listenerDXViolation(root, path, fset.Position(key.Pos()).Line,
			"Delegated key in composite literal"))
		return true
	})
	return violations
}

func forbiddenProductionSurfaceViolations(t *testing.T, root, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
	require.NoError(t, err)

	var violations []string
	ast.Inspect(file, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		for _, term := range productionForbiddenSurfaceTerms {
			if strings.Contains(id.Name, term) {
				violations = append(violations, listenerDXViolation(root, path, fset.Position(id.Pos()).Line,
					fmt.Sprintf("production identifier contains deleted surface %q", term)))
			}
		}
		return true
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

	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "RouteGroup" {
				continue
			}
			st, ok := ts.Type.(*ast.StructType)
			if !ok {
				return []string{listenerDXViolation(root, path, fset.Position(ts.Pos()).Line, "RouteGroup is no longer a struct")}
			}
			for _, field := range st.Fields.List {
				if len(field.Names) != 1 || field.Names[0].Name != "Register" {
					continue
				}
				fn, ok := field.Type.(*ast.FuncType)
				if !ok {
					return []string{listenerDXViolation(root, path, fset.Position(field.Pos()).Line, "RouteGroup.Register is not a func")}
				}
				if !listenerDXFuncHasOneParam(fn, "RouteMux") || !listenerDXFuncReturnsOnlyError(fn) {
					return []string{listenerDXViolation(root, path, fset.Position(field.Pos()).Line,
						"RouteGroup.Register must be func(mux RouteMux) error")}
				}
				return nil
			}
			return []string{listenerDXViolation(root, path, fset.Position(ts.Pos()).Line, "RouteGroup.Register field missing")}
		}
	}
	return []string{listenerDXViolation(root, path, 1, "RouteGroup type missing")}
}

func authMountSignatureViolations(t *testing.T, root string) []string {
	t.Helper()
	path := filepath.Join(root, "runtime", "auth", "route.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	require.NoError(t, err)

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Mount" {
			continue
		}
		if !listenerDXFuncHasParams(fn.Type, "cell.RouteHandler", "Route") {
			return []string{listenerDXViolation(root, path, fset.Position(fn.Pos()).Line,
				"auth.Mount must be func(mux cell.RouteHandler, r Route) error")}
		}
		if !listenerDXFuncReturnsOnlyError(fn.Type) {
			return []string{listenerDXViolation(root, path, fset.Position(fn.Pos()).Line, "auth.Mount must return error")}
		}
		return nil
	}
	return []string{listenerDXViolation(root, path, 1, "auth.Mount function missing")}
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
