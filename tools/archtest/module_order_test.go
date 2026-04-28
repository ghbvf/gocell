package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

const ruleModuleOrderConfigCoreFirst01 = "MODULE-ORDER-CONFIGCORE-FIRST-01"

type assemblyOrderFixture struct {
	ID    string   `yaml:"id"`
	Cells []string `yaml:"cells"`
}

func TestModuleOrderConfigCoreFirst01(t *testing.T) {
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "assemblies", "corebundle", "assembly.yaml"))
	require.NoError(t, err)

	var asm assemblyOrderFixture
	require.NoError(t, yaml.Unmarshal(body, &asm))
	require.NotEmpty(t, asm.Cells)
	assert.Equal(t, "configcore", asm.Cells[0],
		"%s: assemblies/corebundle/assembly.yaml cells order is the runtime module order; configcore must stay first",
		ruleModuleOrderConfigCoreFirst01)
}

func TestCorebundleGeneratedMainDoesNotInlineModules(t *testing.T) {
	root := findModuleRoot(t)
	mainPath := filepath.Join(root, "cmd", "corebundle", "main.go")
	body, err := os.ReadFile(filepath.Join(root, "assemblies", "corebundle", "assembly.yaml"))
	require.NoError(t, err)

	var asm assemblyOrderFixture
	require.NoError(t, yaml.Unmarshal(body, &asm))
	require.NotEmpty(t, asm.ID)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, mainPath, nil, parser.ParseComments)
	require.NoError(t, err, "parse cmd/corebundle/main.go")

	var buildAppCalls []token.Position
	var runCorebundleCall *ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok {
			switch ident.Name {
			case "BuildApp":
				buildAppCalls = append(buildAppCalls, fset.Position(call.Pos()))
			case "runCorebundle":
				runCorebundleCall = call
			}
		}
		return true
	})

	assert.Empty(t, buildAppCalls, "generated main.go must not inline BuildApp module literals")
	require.NotNil(t, runCorebundleCall, "generated main.go must call handwritten runtime helper")
	require.Len(t, runCorebundleCall.Args, 3, "runCorebundle(ctx, assemblyID, cells) signature must stay generated")
	assert.Equal(t, asm.ID, generatedMainStringLiteralValue(t, runCorebundleCall.Args[1]))
	assert.Equal(t, asm.Cells, generatedMainStringSliceLiteralValues(t, runCorebundleCall.Args[2]))
}

func generatedMainStringLiteralValue(t *testing.T, expr ast.Expr) string {
	t.Helper()
	lit, ok := expr.(*ast.BasicLit)
	require.True(t, ok, "expected string literal, got %T", expr)
	got, err := strconv.Unquote(lit.Value)
	require.NoError(t, err)
	return got
}

func generatedMainStringSliceLiteralValues(t *testing.T, expr ast.Expr) []string {
	t.Helper()
	lit, ok := expr.(*ast.CompositeLit)
	require.True(t, ok, "expected []string composite literal, got %T", expr)
	out := make([]string, 0, len(lit.Elts))
	for _, elt := range lit.Elts {
		out = append(out, generatedMainStringLiteralValue(t, elt))
	}
	return out
}
