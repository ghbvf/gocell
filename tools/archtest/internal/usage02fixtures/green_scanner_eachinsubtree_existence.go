package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func _(f *ast.File) bool {
	found := false
	scanner.EachInSubtree[ast.BasicLit](f, func(bl *ast.BasicLit) {
		if bl.Value == "x" {
			found = true
		}
	})
	return found
}
