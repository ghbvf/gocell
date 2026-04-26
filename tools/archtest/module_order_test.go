package archtest

// module_order_test.go enforces the cmd/corebundle composition root's
// load-bearing module registration order:
//
//   - MODULE-ORDER-CONFIGCORE-FIRST-01: cmd/corebundle/main.go::BuildApp(...)
//     must invoke ConfigCoreModule{} as its FIRST module argument (after the
//     ctx + shared positional args). ConfigCoreModule creates the shared
//     *adapterpg.Pool and writes it to shared.SharedPGPool; AccessCoreModule
//     and AuditCoreModule read that pool to wire their outbox writers. Any
//     reordering that places Access/AuditCore before ConfigCore would observe
//     a nil SharedPGPool in postgres mode and fail at startup with
//     ERR_CELL_MISSING_OUTBOX.
//
// This is the static guard for the implicit constraint introduced by
// PR-CFG-G1 commit 3 (SharedPGPool sharing between modules). Without this
// archtest, a future contributor reordering the module list would only
// discover the breakage at runtime in postgres-topology deployments.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	ruleModuleOrderConfigCoreFirst01 = "MODULE-ORDER-CONFIGCORE-FIRST-01"
	configCoreModuleTypeName         = "ConfigCoreModule"
	buildAppFuncName                 = "BuildApp"
)

// TestModuleOrderConfigCoreFirst01 parses cmd/corebundle/main.go and asserts
// that BuildApp's first variadic module argument is a ConfigCoreModule{}
// composite literal. Other module-positional arguments (AccessCoreModule,
// AuditCoreModule) are not checked — they may appear in any order after
// ConfigCoreModule. The first slot is the load-bearing one because pool
// creation must happen before consumers read it.
func TestModuleOrderConfigCoreFirst01(t *testing.T) {
	root := findModuleRoot(t)
	mainPath := filepath.Join(root, "cmd", "corebundle", "main.go")

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, mainPath, nil, parser.ParseComments)
	require.NoError(t, err, "parse cmd/corebundle/main.go")

	var calls []*ast.CallExpr
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		if ident.Name == buildAppFuncName {
			calls = append(calls, call)
		}
		return true
	})

	require.NotEmpty(t, calls,
		"%s: expected at least one BuildApp(...) call in cmd/corebundle/main.go; "+
			"if BuildApp was renamed update this archtest",
		ruleModuleOrderConfigCoreFirst01)

	for _, call := range calls {
		// BuildApp(ctx, shared, mod0, mod1, ...) — first module is args[2].
		require.GreaterOrEqual(t, len(call.Args), 3,
			"%s: BuildApp(...) call at %s must have at least 3 args (ctx, shared, mod0); got %d",
			ruleModuleOrderConfigCoreFirst01, fset.Position(call.Pos()), len(call.Args))

		firstModule := call.Args[2]
		assert.True(t, isConfigCoreModuleLiteral(firstModule),
			"%s: cmd/corebundle/main.go::BuildApp first module argument (position 2) at %s "+
				"must be ConfigCoreModule{} — pool sharing in postgres mode requires "+
				"ConfigCoreModule.Provide to write SharedPGPool before AccessCore/AuditCore "+
				"read it. Reordering will trigger ERR_CELL_MISSING_OUTBOX at startup.",
			ruleModuleOrderConfigCoreFirst01, fset.Position(firstModule.Pos()))
	}
}

// isConfigCoreModuleLiteral returns true if expr is `ConfigCoreModule{}` (a
// CompositeLit whose Type is the bare ident ConfigCoreModule). Pointer / aliased /
// struct-with-fields forms are accepted as long as the type name matches.
func isConfigCoreModuleLiteral(expr ast.Expr) bool {
	cl, ok := expr.(*ast.CompositeLit)
	if !ok {
		return false
	}
	switch t := cl.Type.(type) {
	case *ast.Ident:
		return t.Name == configCoreModuleTypeName
	case *ast.SelectorExpr:
		return t.Sel.Name == configCoreModuleTypeName
	}
	return false
}
