// invariants:
//   - INVARIANT: MODULE-ORDER-AUDITCORE-BEFORE-ACCESSCORE-01
//
// MODULE-ORDER-CONFIGCORE-FIRST-01 was removed when the postgres pool moved out
// of ConfigCoreModule.Provide into the assembly-level provisionCapabilities
// (cap_wiring.go): the pool is provisioned before BuildApp and registered as the
// first ManagedResource by runtimeBaseOptions, so cell module order no longer
// carries the pool happens-before contract. See ADR
// docs/architecture/202605251500-adr-capability-provider-interface.md §Decision.

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

const (
	ruleModuleOrderAuditcoreBeforeAccesscore01 = "MODULE-ORDER-AUDITCORE-BEFORE-ACCESSCORE-01"
)

type assemblyOrderFixture struct {
	ID    string   `yaml:"id"`
	Cells []string `yaml:"cells"`
}

// TestModuleOrderAuditcoreBeforeAccesscore01 enforces that auditcore's
// CellModule.Provide runs before accesscore's so that
// SharedDeps.BootstrapLedgerStore is populated by the time AccessCoreModule
// builds the bootstrap auth-fail observer via audit.NewBootstrapAuthFailObserver
// (BOOTSTRAP-AUDIT-CHAIN-WIRING-01, plan 039 W1-2).
//
// This is now the only cell-module-order invariant: the postgres pool no longer
// rides on slot-zero (configcore) — it is provisioned assembly-level before
// BuildApp (provisionCapabilities), so the former
// MODULE-ORDER-CONFIGCORE-FIRST-01 was removed.
func TestModuleOrderAuditcoreBeforeAccesscore01(t *testing.T) {
	root := findModuleRoot(t)
	body, err := os.ReadFile(filepath.Clean(filepath.Join(root, "assemblies", "corebundle", "assembly.yaml")))
	require.NoError(t, err)

	var asm assemblyOrderFixture
	require.NoError(t, yaml.Unmarshal(body, &asm))

	auditIdx, accessIdx := -1, -1
	for i, c := range asm.Cells {
		switch c {
		case "auditcore":
			auditIdx = i
		case "accesscore":
			accessIdx = i
		}
	}
	require.NotEqual(t, -1, auditIdx,
		"%s: corebundle assembly must include auditcore", ruleModuleOrderAuditcoreBeforeAccesscore01)
	require.NotEqual(t, -1, accessIdx,
		"%s: corebundle assembly must include accesscore", ruleModuleOrderAuditcoreBeforeAccesscore01)
	assert.Less(t, auditIdx, accessIdx,
		"%s: auditcore must Provide before accesscore so SharedDeps.BootstrapLedgerStore "+
			"is wired before AccessCoreModule reads it (audit.NewBootstrapAuthFailObserver)",
		ruleModuleOrderAuditcoreBeforeAccesscore01)
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
