package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func _(b *ast.BlockStmt) bool {
	found := false
	scanner.EachInChildren[ast.ReturnStmt](b, func(ret *ast.ReturnStmt) {
		if found || ret == nil {
			return
		}
		found = true
	})
	return found
}
