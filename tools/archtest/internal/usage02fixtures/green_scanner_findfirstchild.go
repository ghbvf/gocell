package usage02fixtures

import (
	"go/ast"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func _(lit *ast.CompositeLit) bool {
	_, found := scanner.FindFirstChild[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) bool {
		return kv.Key != nil
	})
	return found
}
