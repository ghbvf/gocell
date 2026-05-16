package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest"
)

func _(lit *ast.CompositeLit) bool {
	done := false
	archtest.EachInChildren[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) {
		if done {
			return
		}
		if kv.Key != nil {
			done = true
		}
	})
	return done
}
