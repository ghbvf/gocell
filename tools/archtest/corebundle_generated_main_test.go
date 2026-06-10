//go:build archtest

// INVARIANT: COREBUNDLE-GENERATED-MAIN-NO-INLINE-01
//
// corebundle_generated_main_test.go — the generated cmd/corebundle/main.go must
// delegate to the handwritten runCorebundle(ctx, assemblyID, cells) helper and
// must NOT inline BuildApp module literals; the assembly id + cell list passed to
// runCorebundle must match assemblies/corebundle/assembly.yaml.
//
// Formerly module_order_test.go. The MODULE-ORDER-AUDITCORE-BEFORE-ACCESSCORE-01
// and MODULE-ORDER-CONFIGCORE-FIRST-01 invariants were retired — Wave-1 #1423
// deleted the ModuleExports cross-module value handoff (auditcore now wires the
// bootstrap store internally via WithBootstrapStore; accesscore publishes
// event.auth.bootstrap-failed.v1 and auditcore subscribes), and configcore's
// postgres pool moved to assembly-level provisionCapabilities — so only this
// generated-main check remains.

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

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

type assemblyOrderFixture struct {
	ID    string   `yaml:"id"`
	Cells []string `yaml:"cells"`
}

func TestCorebundleGeneratedMainDoesNotInlineModules(t *testing.T) {
	root := findModuleRoot(t)
	mainPath := filepath.Join(root, "cmd", "corebundle", "main.go")
	body, err := os.ReadFile(filepath.Clean(filepath.Join(root, "assemblies", "corebundle", "assembly.yaml")))
	require.NoError(t, err)

	var asm assemblyOrderFixture
	require.NoError(t, yaml.Unmarshal(body, &asm))
	require.NotEmpty(t, asm.ID)

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, mainPath, nil, parser.ParseComments)
	require.NoError(t, err, "parse cmd/corebundle/main.go")

	var buildAppCalls []token.Position
	var runCorebundleCall *ast.CallExpr
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return
		}
		switch ident.Name {
		case "BuildApp":
			buildAppCalls = append(buildAppCalls, fset.Position(call.Pos()))
		case "runCorebundle":
			runCorebundleCall = call
		}
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
