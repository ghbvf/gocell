package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func _(lit *ast.CompositeLit) bool {
	done := false
	scanner.EachInChildren[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) {
		if done {
			return
		}
		if kv.Key != nil {
			done = true
		}
	})
	return done
}
