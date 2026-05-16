package usage02fixtures

// The dot-import below is intentional and is the entire point of this
// fixture: it models the AST + typeseval resolution of a same-package bare
// call to archtest.EachInChildren from within package archtest itself.
// Outside this single line dot-imports remain forbidden by the global lint
// rule.
import (
	"go/ast"

	. "github.com/ghbvf/gocell/tools/archtest" //nolint:revive,staticcheck // intentional: model package-internal bare callee resolution
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
