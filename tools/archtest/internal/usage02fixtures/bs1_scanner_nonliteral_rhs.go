package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func _(b *ast.BlockStmt, ok bool) bool {
	done := false
	scanner.EachInChildren[ast.IfStmt](b, func(*ast.IfStmt) {
		if done {
			return
		}
		done = ok
	})
	return done
}
