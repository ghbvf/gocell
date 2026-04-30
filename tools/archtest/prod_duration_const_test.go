// PROD-DURATION-CONST-01 — invariant-driven gate.
//
// Invariant: In all production-shippable .go files, any expression whose
// static type is time.Duration and whose subtree contains a BasicLit must
// appear directly in the initializer of a package-level const declaration.
// All other positions (var init, assignment RHS, struct/map literal field,
// function return, CallExpr argument, function-local const, switch case,
// for initializer, TypeAssert/Conversion interior, closure interior) are
// violations.
//
// Exception: a BasicLit whose token value is "0" is not a violation
// (return 0 / var x time.Duration = 0 is idiomatic zero value).
//
// ref: docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md PR-CI-6
package archtest

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
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

// TestProdDurationConst enforces PROD-DURATION-CONST-01 using universal AST
// walk: for every declaration that is not a package-level const block, any
// expression whose static type is time.Duration and whose subtree contains a
// BasicLit is a violation.
//
// ref: docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md PR-CI-6
func TestProdDurationConst(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode " +
			"(loads production packages module-wide, ~5-10s)")
	}

	root := findModuleRoot(t)
	patterns := prodscan.PatternsExtended(root)

	pkgs, errs, err := typeseval.LoadPackagesWithTags(root, []string{"e2e", "integration", "pg"}, patterns...)
	require.NoError(t, err, "packages.Load failed")
	require.Empty(t, errs, "package load errors must fail-closed: %v", errs)

	var violations []string
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

			violations = append(violations, scanProdDurationAST(p.Fset, file, rel, p.TypesInfo)...)
		}
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"PROD-DURATION-CONST-01: extract literal durations to package-level const. "+
			"ref: docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md PR-CI-6")
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
	case strings.HasPrefix(rel, "vendor/"):
		return true
	case strings.HasPrefix(rel, "generated/"):
		return true
	case strings.HasPrefix(rel, "testdata/"):
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
	case strings.Contains(rel, "test") && strings.HasSuffix(rel, "/conformance.go"):
		return true
	}
	return false
}

// scanProdDurationAST walks a single parsed file's AST using a universal walk:
// for each top-level declaration that is not a package-level const block, it
// inspects every sub-expression. An expression that (a) has static type
// time.Duration and (b) whose subtree contains a BasicLit is a violation.
func scanProdDurationAST(
	fset *token.FileSet,
	file *ast.File,
	rel string,
	info *types.Info,
) []string {
	var violations []string

	for _, decl := range file.Decls {
		// Package-level const blocks are the unique compliant position.
		// Skip the entire subtree.
		if gd, ok := decl.(*ast.GenDecl); ok && gd.Tok == token.CONST {
			continue
		}

		ast.Inspect(decl, func(n ast.Node) bool {
			expr, ok := n.(ast.Expr)
			if !ok {
				return true
			}
			if !exprIsTimeDuration(expr, info) {
				return true
			}
			if !isLiteralDurationExpr(expr) {
				return true
			}
			pos := fset.Position(expr.Pos())
			violations = append(violations, fmt.Sprintf("%s:%d: %s", rel, pos.Line, formatDurationExpr(expr)))
			// Do not descend: avoid double-reporting sub-expressions of the
			// outermost matching expression.
			return false
		})
	}

	return violations
}

// exprIsTimeDuration returns true when expr's static type is time.Duration.
func exprIsTimeDuration(expr ast.Expr, info *types.Info) bool {
	if info == nil {
		return false
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return false
	}
	named, ok := tv.Type.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil &&
		obj.Pkg().Path() == "time" && obj.Name() == "Duration"
}

// isLiteralDurationExpr returns true for expressions whose subtree contains a
// numeric BasicLit that contributes a non-zero literal value. It recognizes:
//   - *ast.BasicLit (INT/FLOAT) with Value != "0"  (e.g. var x time.Duration = 5)
//   - <lit> * time.Unit / time.Unit * <lit>  (e.g. 5*time.Second)
//   - chained <lit> * <lit> * time.Unit  (e.g. 7*24*time.Hour)
//   - time.Duration(<lit>) cast
//   - parenthesised / negated forms of the above
//
// NOTE: this predicate does not check the static type; the caller must also
// apply exprIsTimeDuration to avoid false positives on non-duration exprs.
func isLiteralDurationExpr(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.BasicLit:
		// Only numeric literals can be duration values. Strings, chars, and
		// imaginary numbers are never duration magnitudes.
		return (e.Kind == token.INT || e.Kind == token.FLOAT) && e.Value != "0"
	case *ast.BinaryExpr:
		if e.Op != token.MUL {
			return false
		}
		// terminal: lit * time.Unit  or  time.Unit * lit
		// Chained forms like 7*24*time.Hour parse as (7*24)*time.Hour, so
		// allLiteralOrLitProduct handles the chained magnitude side.
		if isTimeUnit(e.X) && allLiteralOrLitProduct(e.Y) {
			return true
		}
		if isTimeUnit(e.Y) && allLiteralOrLitProduct(e.X) {
			return true
		}
		// No recursive fallback: namedVar*2 / namedVar*namedVar must not flag.
		// The type guard (exprIsTimeDuration) ensures the outer expression is
		// time.Duration, so we only report actual literal * unit patterns.
		return false
	case *ast.ParenExpr:
		return isLiteralDurationExpr(e.X)
	case *ast.UnaryExpr:
		return isLiteralDurationExpr(e.X)
	case *ast.CallExpr:
		// time.Duration(<BasicLit>) cast
		if isTimeDurationCast(e) && len(e.Args) == 1 {
			if lit, ok := e.Args[0].(*ast.BasicLit); ok {
				return (lit.Kind == token.INT || lit.Kind == token.FLOAT) && lit.Value != "0"
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
		return (e.Kind == token.INT || e.Kind == token.FLOAT) && e.Value != "0"
	case *ast.ParenExpr:
		return allLiteralOrLitProduct(e.X)
	case *ast.BinaryExpr:
		return allLiteralOrLitProduct(e.X) && allLiteralOrLitProduct(e.Y)
	case *ast.UnaryExpr:
		return allLiteralOrLitProduct(e.X)
	case *ast.CallExpr:
		if isTimeDurationCast(e) && len(e.Args) == 1 {
			if lit, ok := e.Args[0].(*ast.BasicLit); ok {
				return (lit.Kind == token.INT || lit.Kind == token.FLOAT) && lit.Value != "0"
			}
		}
	}
	return false
}

// formatDurationExpr renders an expression back to compact human-readable text
// for violation reports.
func formatDurationExpr(expr ast.Expr) string {
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
		return formatDurationExpr(e.X) + e.Op.String() + formatDurationExpr(e.Y)
	case *ast.ParenExpr:
		return "(" + formatDurationExpr(e.X) + ")"
	case *ast.UnaryExpr:
		return e.Op.String() + formatDurationExpr(e.X)
	case *ast.CallExpr:
		args := make([]string, len(e.Args))
		for i, a := range e.Args {
			args[i] = formatDurationExpr(a)
		}
		return formatDurationExpr(e.Fun) + "(" + strings.Join(args, ", ") + ")"
	}
	return "<expr>"
}
