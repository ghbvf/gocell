// Package red_dot_import_call is a RED fixture for SCAFFOLD-DERIVED-FORCEOVERWRITE-01:
// a dot-imported direct call to DerivedOverwrite (a bare *ast.Ident call, not a
// pkg.Sel SelectorExpr) must be flagged by the forward scan's Ident branch.
package red_dot_import_call

import . "github.com/ghbvf/gocell/framework/pkg/pathsafe"

// call invokes the dot-imported DerivedOverwrite directly (bare-identifier form).
func call() PlannedFile {
	return DerivedOverwrite("/tmp/x", nil)
}
