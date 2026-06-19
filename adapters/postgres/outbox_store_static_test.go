package postgres

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
)

func TestPGOutboxStore_OldestEligibleAt_DoesNotBuildSQLWithFmtSprintf(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "outbox_store.go", nil, 0)
	if err != nil {
		t.Fatalf("parse outbox_store.go: %v", err)
	}

	fn := findFuncDecl(file, "OldestEligibleAt")
	if fn == nil {
		t.Fatal("OldestEligibleAt method not found")
	}

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Sprintf" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "fmt" && sprintfBuildsSQL(call) {
			t.Fatalf("OldestEligibleAt must select from named const SQL queries, not build SQL with fmt.Sprintf at %s",
				fset.Position(call.Pos()))
		}
		return true
	})
}

func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func sprintfBuildsSQL(call *ast.CallExpr) bool {
	if len(call.Args) == 0 {
		return false
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	format, err := strconv.Unquote(lit.Value)
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToUpper(format), "SELECT")
}
