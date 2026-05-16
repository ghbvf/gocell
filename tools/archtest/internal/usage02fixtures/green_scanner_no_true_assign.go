package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func _(b *ast.BlockStmt) int {
	n := 0
	scanner.EachInChildren[ast.IfStmt](b, func(ifStmt *ast.IfStmt) {
		if ifStmt.Else == nil {
			return
		}
		n++
	})
	return n
}
