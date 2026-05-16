package usage02fixtures

import (
	"go/ast"

	. "github.com/ghbvf/gocell/tools/archtest"
)

// dot-import models a call site that resolves identically to a same-package
// bare reference to archtest.EachInChildren — the form that appears when
// authors of tools/archtest/*_test.go (which are in package archtest itself)
// invoke the façade without a qualifier. AST: *ast.Ident{Name:"EachInChildren"}
// after stripping IndexExpr generic instantiation; typeseval resolves the
// ident through the dot-import path to (archtestPkgPath, "EachInChildren").
// USAGE-02 must flag the closure+done sentinel idiom here just as it does for
// qualified scanner.EachInChildren / archtest.EachInChildren calls.
func _(lit *ast.CompositeLit) bool {
	done := false
	EachInChildren[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) {
		if done {
			return
		}
		if kv.Key != nil {
			done = true
		}
	})
	return done
}
