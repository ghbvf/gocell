// PROD-DURATION-CONST-01 — production code must not contain literal duration
// expressions assigned to variables, struct fields, or passed directly to
// duration-bearing functions.
//
// Every literal of the form `N * time.{Unit}`, `time.Duration(N)`, or
// parenthesised / chained variants must be replaced by a named package-level
// const so operators can locate all timeout / interval knobs at a glance.
//
// Scope: production *.go files only. The following are excluded:
//   - `_test.go` files (covered by PR-CI-4 TEST-NO-SLEEP-01)
//   - `tools/archtest/` (self-referential)
//   - `examples/` (out of scope per PR-CI-6 plan)
//   - `vendor/` `.git/` `generated/` `testdata/` `worktrees/` `node_modules/`
//   - `**/locktest/` `**/outboxtest/` `**/storetest/` `**/healthtest/`
//     `**/conformance.go` (driver test helpers where physical timing is the
//     test semantic itself)
//
// Implementation uses packages.Load + go/types to load full type information
// and packages.Visit to walk the dependency graph, then applies a 5-node AST
// predicate (CallExpr / GenDecl(VAR) / AssignStmt / CompositeLit / ReturnStmt)
// with a refined isLiteralDurationExpr predicate that rejects zero sentinels
// and named-const scalings.
//
// ref: golangci-lint/durationcheck (go/types pattern)
// ref: kubernetes hack/tools/* (go/analysis convention)
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/prodscan"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

const prodDurationConstRule = "PROD-DURATION-CONST-01"

type prodDurationViolation struct {
	File     string
	Line     int
	NodeKind string
	Form     string
}

func (v prodDurationViolation) String() string {
	return fmt.Sprintf("%s: %s:%d [%s] %s — extract to package-level const",
		prodDurationConstRule, v.File, v.Line, v.NodeKind, v.Form)
}

// TestProdDurationConst enforces PROD-DURATION-CONST-01: production code must
// not contain inline literal duration expressions in any of the 5 node forms:
// CallExpr, GenDecl(VAR), AssignStmt, CompositeLit (keyed/positional),
// ReturnStmt. The scanner uses packages.Load with full type info.
//
// See docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md PR-CI-6.
func TestProdDurationConst(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode " +
			"(loads production packages module-wide, ~5-10s)")
	}

	root := findModuleRoot(t)
	patterns := prodscan.Patterns(root)

	pkgs, errs, err := typeseval.LoadPackages(root, patterns...)
	require.NoError(t, err, "packages.Load failed")
	if len(errs) > 0 {
		t.Logf("WARN: %d package errors during load (type-check issues)", len(errs))
	}

	var violations []prodDurationViolation
	visited := map[string]bool{}

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for i, file := range p.Syntax {
			if i >= len(p.GoFiles) {
				continue
			}
			abs := p.GoFiles[i]
			if visited[abs] {
				continue
			}
			visited[abs] = true

			if prodDurationExcludeAbs(root, abs) {
				continue
			}
			rel, _ := filepath.Rel(root, abs)
			rel = filepath.ToSlash(rel)

			violations = append(violations, scanProdDurationAST(p.Fset, file, rel)...)
		}
	})

	sort.Slice(violations, func(i, j int) bool {
		if violations[i].File != violations[j].File {
			return violations[i].File < violations[j].File
		}
		return violations[i].Line < violations[j].Line
	})
	for _, v := range violations {
		t.Log(v.String())
	}
	assert.Empty(t, violations,
		"PROD-DURATION-CONST-01: extract literal durations to package-level const. "+
			"Covers 5 node forms: CallExpr / GenDecl(VAR) / AssignStmt / "+
			"CompositeLit (keyed+positional) / ReturnStmt. "+
			"See docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md PR-CI-6.")
}

// prodDurationExcludeAbs reports whether the absolute path should be skipped.
func prodDurationExcludeAbs(root, abs string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return true
	}
	rel = filepath.ToSlash(rel)
	// Reject paths outside the module root (dependency packages).
	if strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
		return true
	}
	switch {
	case strings.HasSuffix(rel, "_test.go"):
		return true
	case strings.HasPrefix(rel, "tools/archtest/"):
		return true
	case strings.HasPrefix(rel, "examples/"):
		return true
	case strings.Contains(rel, "/locktest/"):
		return true
	case strings.Contains(rel, "/outboxtest/"):
		return true
	case strings.Contains(rel, "/storetest/"):
		return true
	case strings.Contains(rel, "/healthtest/"):
		return true
	case strings.HasSuffix(rel, "/conformance.go"):
		return true
	case strings.HasPrefix(rel, "vendor/"):
		return true
	case strings.HasPrefix(rel, "generated/"):
		return true
	case strings.HasPrefix(rel, "testdata/"):
		return true
	}
	return false
}

// scanProdDurationAST walks a single parsed file's AST and returns every
// literal-duration expression found in the 5 target node forms.
func scanProdDurationAST(fset *token.FileSet, file *ast.File, rel string) []prodDurationViolation {
	var violations []prodDurationViolation

	report := func(node ast.Node, kind, form string) {
		pos := fset.Position(node.Pos())
		violations = append(violations, prodDurationViolation{
			File:     rel,
			Line:     pos.Line,
			NodeKind: kind,
			Form:     form,
		})
	}

	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {

		// Node 1: Call-argument path (time.Sleep, context.WithTimeout, …)
		case *ast.CallExpr:
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			id, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			var argIdx int
			form := ""
			switch id.Name {
			case "time":
				switch sel.Sel.Name {
				case "Sleep", "After", "NewTimer", "NewTicker":
					form = "time." + sel.Sel.Name
					argIdx = 0
				}
			case "context":
				switch sel.Sel.Name {
				case "WithTimeout":
					form = "context.WithTimeout"
					argIdx = 1
				case "WithDeadline":
					form = "context.WithDeadline"
					argIdx = 1
				case "AfterFunc":
					form = "context.AfterFunc"
					argIdx = 0
				}
			}
			if form == "" || argIdx >= len(node.Args) {
				return true
			}
			arg := node.Args[argIdx]
			if form == "context.WithDeadline" {
				if inner, ok := unwrapNowAddCall(arg); ok {
					arg = inner
				} else {
					return true
				}
			}
			if isLiteralDurationExpr(arg) {
				report(arg, "CallArg", form+"("+prodExprText(arg)+")")
			}

		// Node 2: package-level var declarations  var x = 30*time.Second
		case *ast.GenDecl:
			if node.Tok != token.VAR {
				return true
			}
			for _, spec := range node.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, val := range vs.Values {
					if isLiteralDurationExpr(val) {
						report(val, "VarInit", "var = "+prodExprText(val))
					}
				}
			}

		// Node 3: assignment statements  x = 5*time.Second
		case *ast.AssignStmt:
			for _, val := range node.Rhs {
				if isLiteralDurationExpr(val) {
					report(val, "Assign", "= "+prodExprText(val))
				}
			}

		// Node 4: composite literals (struct fields and positional elements)
		case *ast.CompositeLit:
			for _, elt := range node.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					if isLiteralDurationExpr(kv.Value) {
						report(kv.Value, "Field",
							prodExprText(kv.Key)+": "+prodExprText(kv.Value))
					}
				} else if isLiteralDurationExpr(elt) {
					report(elt, "CompositePos", prodExprText(elt))
				}
			}

		// Node 5: return statements  return 5*time.Second
		case *ast.ReturnStmt:
			for _, res := range node.Results {
				if isLiteralDurationExpr(res) {
					report(res, "Return", "return "+prodExprText(res))
				}
			}
		}
		return true
	})
	return violations
}

// isLiteralDurationExpr returns true ONLY for the surface forms developers
// actually write to express a hardcoded duration:
//   - <lit> * time.Unit   /   time.Unit * <lit>
//   - chained <lit> * <lit> * time.Unit  (e.g. 7*24*time.Hour)
//   - time.Duration(<lit>)
//   - parenthesised forms of the above
//
// It explicitly does NOT match:
//   - existingDur * 2 (factor scaling of a named const)
//   - return 0 / = 0 (zero sentinel — allLiteralOrLitProduct rejects "0")
//   - 0 * time.Second (zero sentinel expressed as multiplication)
//   - time.Duration(runtimeExpr) (non-literal cast)
//   - BaseRetryDelay * (1<<shift) (named-const scaling)
func isLiteralDurationExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BinaryExpr:
		if e.Op != token.MUL {
			return false
		}
		// terminal: lit * time.Unit  or  time.Unit * lit
		if isTimeUnit(e.X) && allLiteralOrLitProduct(e.Y) {
			return true
		}
		if isTimeUnit(e.Y) && allLiteralOrLitProduct(e.X) {
			return true
		}
		// recursive: (lit * lit) * time.Unit  e.g. 7*24*time.Hour
		return isLiteralDurationExpr(e.X) || isLiteralDurationExpr(e.Y)
	case *ast.ParenExpr:
		return isLiteralDurationExpr(e.X)
	case *ast.CallExpr:
		// time.Duration(<BasicLit>) cast
		if isTimeDurationCast(e) && len(e.Args) == 1 {
			if _, ok := e.Args[0].(*ast.BasicLit); ok {
				return true
			}
		}
	}
	return false
}

// isTimeUnit returns true for time.{Nanosecond,Microsecond,Millisecond,Second,Minute,Hour}.
func isTimeUnit(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != "time" {
		return false
	}
	switch sel.Sel.Name {
	case "Nanosecond", "Microsecond", "Millisecond",
		"Second", "Minute", "Hour":
		return true
	}
	return false
}

// isTimeDurationCast returns true for time.Duration(<expr>) type-conversion calls.
func isTimeDurationCast(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok || id.Name != "time" {
		return false
	}
	return sel.Sel.Name == "Duration"
}

// allLiteralOrLitProduct reports whether expr is composed entirely of BasicLit
// nodes, parenthesised/arithmetic BasicLit products, or time.Duration(<BasicLit>)
// casts. Used as the "magnitude" side of `<magnitude> * time.Unit`.
//
// Special case: "0" is rejected — `return 0` / `= 0` is idiomatic zero-value
// usage, not a hardcoded duration literal.
func allLiteralOrLitProduct(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Value != "0"
	case *ast.ParenExpr:
		return allLiteralOrLitProduct(e.X)
	case *ast.BinaryExpr:
		return allLiteralOrLitProduct(e.X) && allLiteralOrLitProduct(e.Y)
	case *ast.UnaryExpr:
		return allLiteralOrLitProduct(e.X)
	case *ast.CallExpr:
		if isTimeDurationCast(e) && len(e.Args) == 1 {
			if _, ok := e.Args[0].(*ast.BasicLit); ok {
				return true
			}
		}
	}
	return false
}

// unwrapNowAddCall recognises `time.Now().Add(<expr>)` and returns <expr>, true.
// Used to extract the duration argument from context.WithDeadline callers.
func unwrapNowAddCall(expr ast.Expr) (ast.Expr, bool) {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Add" {
		return nil, false
	}
	inner, ok := sel.X.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	innerSel, ok := inner.Fun.(*ast.SelectorExpr)
	if !ok || innerSel.Sel.Name != "Now" {
		return nil, false
	}
	pkg, ok := innerSel.X.(*ast.Ident)
	if !ok || pkg.Name != "time" {
		return nil, false
	}
	return call.Args[0], true
}

// prodExprText renders an expression back to compact human-readable text for
// violation reports. Mirrors exprText from the dump scanner.
func prodExprText(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.BasicLit:
		return e.Value
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		if id, ok := e.X.(*ast.Ident); ok {
			return id.Name + "." + e.Sel.Name
		}
		return e.Sel.Name
	case *ast.BinaryExpr:
		return prodExprText(e.X) + e.Op.String() + prodExprText(e.Y)
	case *ast.ParenExpr:
		return "(" + prodExprText(e.X) + ")"
	case *ast.UnaryExpr:
		return e.Op.String() + prodExprText(e.X)
	case *ast.CallExpr:
		args := make([]string, len(e.Args))
		for i, a := range e.Args {
			args[i] = prodExprText(a)
		}
		return prodExprText(e.Fun) + "(" + strings.Join(args, ", ") + ")"
	}
	return fmt.Sprintf("<%T>", expr)
}
