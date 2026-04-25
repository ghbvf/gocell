// Package unconditionalskip defines a go/analysis analyzer that flags test
// functions whose first statement is an unconditional t.Skip / t.Skipf /
// t.SkipNow call. Such tests never run and should either be removed or
// guarded by a condition (e.g. testing.Short()).
//
// Rule: there are no exemptions. If a test is permanently skipped, delete it.
// If it should run in some conditions, wrap the skip in an if-clause.
//
// ref: golang.org/x/tools/go/analysis/passes/nilness/nilness.go — analyzer
// registration and AST visitor template.
package unconditionalskip

import (
	"go/ast"
	"go/types"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/passes/inspect"
	"golang.org/x/tools/go/ast/inspector"
)

// Analyzer is the exported analysis.Analyzer that can be embedded into nogo
// rule sets or run via singlechecker.Main.
var Analyzer = &analysis.Analyzer{
	Name:     "unconditionalskip",
	Doc:      "reports test functions whose first statement is an unconditional t.Skip/t.Skipf/t.SkipNow",
	URL:      "https://github.com/ghbvf/gocell/tools/nogo/unconditionalskip",
	Requires: []*analysis.Analyzer{inspect.Analyzer},
	Run:      run,
}

const diagMessage = "unconditional t.Skip — wrap in if-condition or remove the test"

func run(pass *analysis.Pass) (interface{}, error) {
	insp := pass.ResultOf[inspect.Analyzer].(*inspector.Inspector)

	nodeFilter := []ast.Node{(*ast.FuncDecl)(nil)}
	insp.Preorder(nodeFilter, func(n ast.Node) {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return
		}

		// Only analyse Test* and Benchmark* functions whose first parameter is
		// *testing.T or *testing.B.
		if !isTestOrBenchmark(fn, pass) {
			return
		}

		checkBody(pass, fn.Body)
	})

	return nil, nil
}

// isTestOrBenchmark reports whether fn is a top-level test or benchmark
// function: name starts with "Test" or "Benchmark", and the first parameter
// type is *testing.T or *testing.B.
func isTestOrBenchmark(fn *ast.FuncDecl, pass *analysis.Pass) bool {
	name := fn.Name.Name
	if !hasTestPrefix(name) {
		return false
	}
	if fn.Type.Params == nil || len(fn.Type.Params.List) < 1 {
		return false
	}
	firstParam := fn.Type.Params.List[0]
	return isTestingTorB(pass, firstParam.Type)
}

func hasTestPrefix(name string) bool {
	return len(name) > 4 && (name[:4] == "Test" || (len(name) > 9 && name[:9] == "Benchmark"))
}

// isTestingTorB reports whether the expression is *testing.T or *testing.B.
func isTestingTorB(pass *analysis.Pass, expr ast.Expr) bool {
	ptr, ok := expr.(*ast.StarExpr)
	if !ok {
		return false
	}
	tv, ok := pass.TypesInfo.Types[ptr.X]
	if !ok {
		return false
	}
	named, ok := tv.Type.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj.Pkg() == nil || obj.Pkg().Path() != "testing" {
		return false
	}
	return obj.Name() == "T" || obj.Name() == "B"
}

// checkBody inspects the first statement of body. If it is an unconditional
// call to t.Skip, t.Skipf, or t.SkipNow, a diagnostic is reported.
// Sub-functions passed to t.Run are also inspected recursively.
func checkBody(pass *analysis.Pass, body *ast.BlockStmt) {
	if body == nil || len(body.List) == 0 {
		return
	}

	first := body.List[0]

	// Direct: t.Skip(...), t.Skipf(...), t.SkipNow()
	if isSkipCall(pass, first) {
		pass.Reportf(first.Pos(), diagMessage)
		return
	}

	// t.Run(name, func(t *testing.T) { ... }) — recurse into the literal body.
	if sub, ok := extractRunLitBody(pass, first); ok {
		checkBody(pass, sub)
	}
}

// isSkipCall reports whether stmt is an expression statement of the form
// receiver.Skip(...) / receiver.Skipf(...) / receiver.SkipNow() where the
// receiver's type is *testing.T or *testing.B.
func isSkipCall(pass *analysis.Pass, stmt ast.Stmt) bool {
	exprStmt, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := exprStmt.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Skip", "Skipf", "SkipNow":
	default:
		return false
	}
	// Check that the receiver is typed as *testing.T or *testing.B.
	tv, ok := pass.TypesInfo.Types[sel.X]
	if !ok {
		return false
	}
	ptr, ok := tv.Type.(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	if obj.Pkg() == nil || obj.Pkg().Path() != "testing" {
		return false
	}
	return obj.Name() == "T" || obj.Name() == "B"
}

// extractRunLitBody extracts the *ast.BlockStmt from a t.Run(name, func(...){...})
// call that appears as the first statement. Returns nil, false if the
// statement is not a t.Run call with a function literal body.
func extractRunLitBody(pass *analysis.Pass, stmt ast.Stmt) (*ast.BlockStmt, bool) {
	exprStmt, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return nil, false
	}
	call, ok := exprStmt.X.(*ast.CallExpr)
	if !ok {
		return nil, false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return nil, false
	}
	if sel.Sel.Name != "Run" {
		return nil, false
	}
	// Verify receiver is *testing.T.
	tv, ok := pass.TypesInfo.Types[sel.X]
	if !ok {
		return nil, false
	}
	ptr, ok := tv.Type.(*types.Pointer)
	if !ok {
		return nil, false
	}
	named, ok := ptr.Elem().(*types.Named)
	if !ok {
		return nil, false
	}
	obj := named.Obj()
	if obj.Pkg() == nil || obj.Pkg().Path() != "testing" || obj.Name() != "T" {
		return nil, false
	}
	// Second argument must be a function literal.
	if len(call.Args) < 2 {
		return nil, false
	}
	lit, ok := call.Args[1].(*ast.FuncLit)
	if !ok {
		return nil, false
	}
	return lit.Body, true
}
