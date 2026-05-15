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
	"os"
	"path/filepath"
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
		diags := Run(t, ModuleScope(root), func(p *Pass) []Diagnostic {
			var ds []Diagnostic
			for _, file := range p.Files {
				ds = append(ds, oldListenerAPIIdentViolationsPass(p, file)...)
			}
			return ds
		})
		var violations []string
		for _, d := range diags {
			violations = append(violations, fmt.Sprintf("%s:%d: %s", d.Rel, d.Line, d.Message))
		}
		assert.Empty(t, violations, "%s: deleted listener option APIs must not reappear:\n%s",
			ruleListenerDXA52, strings.Join(violations, "\n"))
	})

	t.Run("delegated_route_surface_not_reintroduced", func(t *testing.T) {
		diags := Run(t, ModuleScope(root), func(p *Pass) []Diagnostic {
			var ds []Diagnostic
			for _, file := range p.Files {
				ds = append(ds, delegatedRouteFieldViolationsPass(p, file)...)
				ds = append(ds, forbiddenProductionSurfaceViolationsPass(p, file)...)
			}
			return ds
		})
		var violations []string
		for _, d := range diags {
			violations = append(violations, fmt.Sprintf("%s:%d: %s", d.Rel, d.Line, d.Message))
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
		var violations []string

		// .md files via EachContentFile (non-Go content axis).
		mdScope := ModuleScope(root)
		EachContentFile(t, mdScope, []string{".md"}, func(_ *testing.T, fc ContentContext) {
			if listenerDXDocExcluded(fc.Rel) {
				return
			}
			violations = append(violations, activeDocTermViolations(t, root, fc.AbsPath)...)
		})

		// doc.go files and all production Go files via Run — comments need ParseComments.
		goScope := ModuleScope(root)
		Run(t, goScope, func(p *Pass) []Diagnostic {
			for _, file := range p.Files {
				rel := p.Rel(file)
				isDocGo := filepath.Base(rel) == "doc.go"
				if isDocGo {
					if listenerDXDocExcluded(rel) {
						continue
					}
					violations = append(violations, activeDocTermViolationsFromFile(p, file)...)
				}
				violations = append(violations, activeGoCommentTermViolationsPass(p, file)...)
			}
			return nil
		})

		assert.Empty(t, violations, "%s: active docs/godoc must not show deleted listener APIs or Delegated examples:\n%s",
			ruleListenerDXA52, strings.Join(violations, "\n"))
	})
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

// oldListenerAPIIdentViolationsPass scans one *ast.File for forbidden idents.
func oldListenerAPIIdentViolationsPass(p *Pass, file *ast.File) []Diagnostic {
	var ds []Diagnostic
	EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
		if _, forbidden := oldListenerAPIIdents[id.Name]; !forbidden {
			return
		}
		pos := p.Fset.Position(id.Pos())
		ds = append(ds, Diagnostic{
			Rel:     p.Rel(file),
			Line:    pos.Line,
			Message: fmt.Sprintf("deleted listener option identifier %q", id.Name),
		})
	})
	return ds
}

// delegatedRouteFieldViolationsPass scans one *ast.File for Delegated composite-literal keys.
func delegatedRouteFieldViolationsPass(p *Pass, file *ast.File) []Diagnostic {
	var ds []Diagnostic
	EachInSubtree[ast.KeyValueExpr](file, func(kv *ast.KeyValueExpr) {
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Delegated" {
			return
		}
		pos := p.Fset.Position(key.Pos())
		ds = append(ds, Diagnostic{
			Rel:     p.Rel(file),
			Line:    pos.Line,
			Message: "Delegated key in composite literal",
		})
	})
	return ds
}

// forbiddenProductionSurfaceViolationsPass scans one *ast.File for forbidden
// production surface identifiers and comments. Run always parses with
// ParseComments so comment data is available in file.Comments.
func forbiddenProductionSurfaceViolationsPass(p *Pass, file *ast.File) []Diagnostic {
	var ds []Diagnostic
	rel := p.Rel(file)
	EachInSubtree[ast.Ident](file, func(id *ast.Ident) {
		for _, term := range productionForbiddenSurfaceTerms {
			if strings.Contains(id.Name, term) {
				ds = append(ds, Diagnostic{
					Rel:     rel,
					Line:    p.Fset.Position(id.Pos()).Line,
					Message: fmt.Sprintf("production identifier contains deleted surface %q", term),
				})
			}
		}
	})
	for _, group := range file.Comments {
		for _, comment := range group.List {
			for _, term := range productionForbiddenSurfaceTerms {
				if strings.Contains(comment.Text, term) {
					ds = append(ds, Diagnostic{
						Rel:     rel,
						Line:    p.Fset.Position(comment.Pos()).Line,
						Message: fmt.Sprintf("production comment contains deleted surface %q", term),
					})
				}
			}
		}
	}
	return ds
}

func routeGroupRegisterSignatureViolations(t *testing.T, root string) []string {
	t.Helper()
	// RouteGroup struct is defined in registry.go (merged in batch 1/4).
	const rel = "kernel/cell/registry.go"
	path := filepath.Join(root, filepath.FromSlash(rel))

	var result []string
	found := false

	scope := DirsScope(root, []string{filepath.Dir(rel)},
		MatchRels(func(r string) bool { return r == rel }),
	)
	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			if p.Rel(file) != rel {
				continue
			}
			EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
				if found || ts.Name.Name != "RouteGroup" {
					return
				}
				found = true
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					result = []string{listenerDXViolation(root, path, p.Fset.Position(ts.Pos()).Line, "RouteGroup is no longer a struct")}
					return
				}
				for _, field := range st.Fields.List {
					if len(field.Names) != 1 || field.Names[0].Name != "Register" {
						continue
					}
					fn, ok := field.Type.(*ast.FuncType)
					if !ok {
						result = []string{listenerDXViolation(root, path, p.Fset.Position(field.Pos()).Line, "RouteGroup.Register is not a func")}
						return
					}
					if !listenerDXFuncHasOneParam(fn, "RouteMux") || !listenerDXFuncReturnsOnlyError(fn) {
						result = []string{listenerDXViolation(root, path, p.Fset.Position(field.Pos()).Line,
							"RouteGroup.Register must be func(mux RouteMux) error")}
						return
					}
					return
				}
				result = []string{listenerDXViolation(root, path, p.Fset.Position(ts.Pos()).Line, "RouteGroup.Register field missing")}
			})
		}
		return nil
	})
	if !found {
		return []string{listenerDXViolation(root, path, 1, "RouteGroup type missing")}
	}
	return result
}

func authMountSignatureViolations(t *testing.T, root string) []string {
	t.Helper()
	const rel = "runtime/auth/route.go"
	path := filepath.Join(root, filepath.FromSlash(rel))

	var result []string
	found := false

	scope := DirsScope(root, []string{filepath.Dir(rel)},
		MatchRels(func(r string) bool { return r == rel }),
	)
	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			if p.Rel(file) != rel {
				continue
			}
			EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
				if found || fn.Name.Name != "Mount" {
					return
				}
				found = true
				if !listenerDXFuncHasParams(fn.Type, "cell.RouteHandler", "Route") {
					result = []string{listenerDXViolation(root, path, p.Fset.Position(fn.Pos()).Line,
						"auth.Mount must be func(mux cell.RouteHandler, r Route) error")}
					return
				}
				if !listenerDXFuncReturnsOnlyError(fn.Type) {
					result = []string{listenerDXViolation(root, path, p.Fset.Position(fn.Pos()).Line, "auth.Mount must return error")}
				}
			})
		}
		return nil
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

// activeDocTermViolationsFromFile reports forbidden terms in the doc.go text
// by reading the file bytes directly (same logic as activeDocTermViolations but
// driven from an already-parsed *ast.File whose abs path we have from Pass.Abs).
func activeDocTermViolationsFromFile(p *Pass, file *ast.File) []string {
	path := p.Abs(file)
	rel := p.Rel(file)
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return []string{fmt.Sprintf("%s:0: cannot read file: %v", rel, err)}
	}
	lines := strings.Split(string(data), "\n")
	var violations []string
	terms := listenerDXForbiddenDocTerms()
	for i, line := range lines {
		for _, term := range terms {
			if strings.Contains(line, term) {
				violations = append(violations, fmt.Sprintf("%s:%d: active docs/godoc contains %q", rel, i+1, term))
			}
		}
	}
	return violations
}

// activeGoCommentTermViolationsPass scans one parsed *ast.File's comment groups
// for forbidden terms. Run parses with ParseComments so file.Comments is populated.
func activeGoCommentTermViolationsPass(p *Pass, file *ast.File) []string {
	var violations []string
	terms := listenerDXForbiddenDocTerms()
	for _, group := range file.Comments {
		for _, comment := range group.List {
			for _, term := range terms {
				if strings.Contains(comment.Text, term) {
					violations = append(violations, fmt.Sprintf("%s:%d: active godoc/comment contains %q",
						p.Rel(file), p.Fset.Position(comment.Pos()).Line, term))
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
