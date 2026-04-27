package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVaultIntegrationContainerFailuresFailFast(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Join(root, "adapters", "vault", "integration_test.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err)

	fn := findFuncDecl(file, "startVaultContainer")
	require.NotNil(t, fn, "startVaultContainer helper must exist")

	var hasDockerPrecheck bool
	var skipCalls []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if selectorName(call.Fun) == "RequireDocker" {
			hasDockerPrecheck = true
		}
		switch selectorName(call.Fun) {
		case "Skip", "Skipf", "SkipNow":
			skipCalls = append(skipCalls, fset.Position(call.Pos()).String())
		}
		return true
	})

	assert.True(t, hasDockerPrecheck, "startVaultContainer must explicitly skip only when Docker is unavailable")
	assert.Empty(t, skipCalls, "Vault container startup/address failures must fail-fast, not skip")
}

func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

func selectorName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.SelectorExpr:
		return x.Sel.Name
	case *ast.Ident:
		return x.Name
	default:
		return ""
	}
}
