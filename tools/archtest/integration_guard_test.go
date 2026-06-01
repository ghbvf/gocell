// INVARIANT: INTEGRATION-GUARD-01: vault integration container failures must fail-fast without hanging
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestVaultContainerStartersFailFast asserts every function in adapters/vault
// that starts a testcontainer fails fast on container errors — it must not call
// t.Skip/Skipf/SkipNow. The sole sanctioned skip is testutil.RequireDocker (a
// missing Docker daemon); its before-Run ordering is enforced module-wide by
// TestTestcontainerHelpersRequireDockerBeforeRun, so this test only adds the
// no-skip half.
//
// Coverage is derived (PR for #636): it enumerates vault *_test.go funcs that
// invoke a testcontainer constructor (module .Run / core GenericContainer) via
// the shared alias/run detection, rather than hardcoding startVaultContainer.
// A new container helper (e.g. the k3s+Vault Kubernetes-auth e2e in
// k8s_auth_e2e_integration_test.go) is therefore auto-covered.
//
// Scope note: detection keys on container constructors (module .Run /
// GenericContainer). network.New is intentionally NOT treated as a container
// start — a docker network is not a container with a fail-fast cost — so a
// hypothetical network-only helper would not be flagged; that is by design.
//
// AI-robust grade: Medium (AST/selector-derived container-starter set; no name
// allowlist). Hard ceiling = a single sanctioned testutil funnel that bakes in
// RequireDocker + no-skip for every container start (cf.
// PG-TESTCONTAINER-FUNNEL-01 / tcpostgres.Run single-funnel); tracked at
// gh #1466. This replaced the prior hardcoded-name
// TestVaultIntegrationContainerFailuresFailFast (Soft: a new vault container
// helper escaped the no-skip check).
func TestVaultContainerStartersFailFast(t *testing.T) {
	root := findModuleRoot(t)
	dir := filepath.Join(root, "adapters", "vault")
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	fset := token.NewFileSet()
	var starters int
	var skipCalls []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, perr := parser.ParseFile(fset, filepath.Join(dir, entry.Name()), nil, 0)
		require.NoError(t, perr)
		aliases := testcontainerAliasesFor(file)
		if len(aliases.core)+len(aliases.modules) == 0 {
			continue
		}
		scanner.EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
			if fn.Body == nil || !firstTestcontainerRunPos(fn.Body, aliases).IsValid() {
				return
			}
			starters++
			scanner.EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
				switch selectorName(call.Fun) {
				case "Skip", "Skipf", "SkipNow":
					skipCalls = append(skipCalls, fset.Position(call.Pos()).String())
				}
			})
		})
	}

	require.Positive(t, starters, "expected at least one vault container-starting func (startVaultContainer)")
	assert.Empty(t, skipCalls, "vault container starters must fail-fast on container errors, not skip")
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
	scanner.EachInSubtree[ast.CallExpr](fn.Body, func(call *ast.CallExpr) {
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
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
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
	scanner.EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
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
	scanner.EachInSubtree[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
		if found == nil && fn.Name.Name == name {
			found = fn
		}
	})
	return found
}

func firstTestcontainerRunPos(body *ast.BlockStmt, aliases testcontainerAliases) token.Pos {
	var out token.Pos
	scanner.EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
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
	scanner.EachInSubtree[ast.CallExpr](body, func(call *ast.CallExpr) {
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
