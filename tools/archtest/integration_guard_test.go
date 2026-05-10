// INVARIANT: INTEGRATION-GUARD-01: vault integration container failures must fail-fast without hanging
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
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
	scanner.EachNode[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
		if selectorName(call.Fun) == "RequireDocker" {
			hasDockerPrecheck = true
		}
		switch selectorName(call.Fun) {
		case "Skip", "Skipf", "SkipNow":
			skipCalls = append(skipCalls, fset.Position(call.Pos()).String())
		}
	})

	assert.True(t, hasDockerPrecheck, "startVaultContainer must explicitly skip only when Docker is unavailable")
	assert.Empty(t, skipCalls, "Vault container startup/address failures must fail-fast, not skip")
}

func TestPostgresUnreachableHostIsNotEnvGated(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Join(root, "adapters", "postgres", "pool_test.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err)

	fn := findFuncDecl(file, "TestNewPool_UnreachableHost")
	require.NotNil(t, fn, "TestNewPool_UnreachableHost must exist")

	var findings []string
	scanner.EachNode[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
		switch selectorName(call.Fun) {
		case "Skip", "Skipf", "SkipNow":
			findings = append(findings, fset.Position(call.Pos()).String()+": unreachable-host test must not skip")
		case "LookupEnv", "Getenv":
			findings = append(findings, fset.Position(call.Pos()).String()+": unreachable-host test must not depend on env")
		}
	})

	assert.Empty(t, findings)
}

func TestCorebundleOutboxWiringDoesNotUseExternalDSNGate(t *testing.T) {
	root := findModuleRoot(t)
	path := filepath.Join(root, "cmd", "corebundle", "outbox_wiring_integration_test.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	require.NoError(t, err)

	var findings []string
	scanner.EachNode[ast.CallExpr](file, func(call *ast.CallExpr) {
		switch selectorName(call.Fun) {
		case "Skip", "Skipf", "SkipNow":
			findings = append(findings, fset.Position(call.Pos()).String()+": corebundle wiring test must self-provision dependencies")
		case "LookupEnv", "Getenv":
			if len(call.Args) == 1 && archStringLiteralValue(call.Args[0]) == "GOCELL_CONFIGCORE_DATABASE_URL" {
				findings = append(findings,
					fset.Position(call.Pos()).String()+
						": corebundle wiring test must not require external GOCELL_CONFIGCORE_DATABASE_URL")
			}
		}
	})

	assert.Empty(t, findings)
}

func TestTestcontainerHelpersRequireDockerBeforeRun(t *testing.T) {
	root := findModuleRoot(t)
	findings := collectTestcontainerDockerGuardFindings(t, root)
	assert.Empty(t, findings)
}

func collectTestcontainerDockerGuardFindings(t *testing.T, root string) []string {
	t.Helper()
	scope := scanner.ModuleScope(root, scanner.IncludeTests())
	files, err := scope.Files()
	require.NoError(t, err)

	var findings []string
	for _, path := range files {
		fileFindings, ferr := testcontainerDockerGuardFindingsForFile(path)
		require.NoError(t, ferr)
		findings = append(findings, fileFindings...)
	}
	return findings
}

func testcontainerDockerGuardFindingsForFile(path string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	aliases := testcontainerAliasesFor(file)
	if len(aliases.core)+len(aliases.modules) == 0 {
		return nil, nil
	}

	var findings []string
	scanner.EachNode[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if fn.Body == nil {
			return
		}
		if finding := testcontainerDockerGuardFindingForFunc(fset, fn, aliases); finding != "" {
			findings = append(findings, finding)
		}
	})
	return findings, nil
}

func testcontainerDockerGuardFindingForFunc(
	fset *token.FileSet,
	fn *ast.FuncDecl,
	aliases testcontainerAliases,
) string {
	runPos := firstTestcontainerRunPos(fn.Body, aliases)
	if !runPos.IsValid() {
		return ""
	}
	requireDockerPos := firstSelectorCallPos(fn.Body, "RequireDocker")
	if requireDockerPos.IsValid() && requireDockerPos < runPos {
		return ""
	}
	return fset.Position(runPos).String() +
		": " + fn.Name.Name + " must call testutil.RequireDocker(t) before starting a testcontainer"
}

type testcontainerAliases struct {
	core    map[string]struct{}
	modules map[string]struct{}
}

func testcontainerAliasesFor(file *ast.File) testcontainerAliases {
	aliases := testcontainerAliases{
		core:    map[string]struct{}{},
		modules: map[string]struct{}{},
	}
	for _, imp := range file.Imports {
		path := archStringLiteralValue(imp.Path)
		switch {
		case path == "github.com/testcontainers/testcontainers-go":
			if name := importSelectorName(imp, "testcontainers"); name != "" {
				aliases.core[name] = struct{}{}
			}
		case strings.HasPrefix(path, "github.com/testcontainers/testcontainers-go/modules/"):
			if name := importSelectorName(imp, filepath.Base(path)); name != "" {
				aliases.modules[name] = struct{}{}
			}
		}
	}
	return aliases
}

func importSelectorName(imp *ast.ImportSpec, defaultName string) string {
	if imp.Name == nil {
		return defaultName
	}
	switch imp.Name.Name {
	case ".", "_":
		return ""
	default:
		return imp.Name.Name
	}
}

func findFuncDecl(file *ast.File, name string) *ast.FuncDecl {
	var found *ast.FuncDecl
	scanner.EachNode[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if found == nil && fn.Name.Name == name {
			found = fn
		}
	})
	return found
}

func firstTestcontainerRunPos(body *ast.BlockStmt, aliases testcontainerAliases) token.Pos {
	var out token.Pos
	scanner.EachNode[ast.CallExpr](body, func(call *ast.CallExpr) {
		if out.IsValid() {
			return
		}
		if isTestcontainerRun(call.Fun, aliases) {
			out = call.Pos()
		}
	})
	return out
}

func isTestcontainerRun(expr ast.Expr, aliases testcontainerAliases) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	if sel.Sel.Name == "GenericContainer" {
		_, ok := aliases.core[ident.Name]
		return ok
	}
	if sel.Sel.Name != "Run" {
		return false
	}
	_, ok = aliases.modules[ident.Name]
	return ok
}

func firstSelectorCallPos(body *ast.BlockStmt, name string) token.Pos {
	var out token.Pos
	scanner.EachNode[ast.CallExpr](body, func(call *ast.CallExpr) {
		if out.IsValid() {
			return
		}
		if selectorName(call.Fun) == name {
			out = call.Pos()
		}
	})
	return out
}

func archStringLiteralValue(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok {
		return ""
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return value
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
