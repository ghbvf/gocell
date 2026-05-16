package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest"
)

func _(lit *ast.CompositeLit) bool {
	_, found := archtest.FindFirstChild[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) bool {
		return kv.Key != nil
	})
	return found
}
