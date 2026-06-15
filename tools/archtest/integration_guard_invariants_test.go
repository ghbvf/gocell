//go:build archtest

// invariants asserted in this file:
//   - INVARIANT: INTEGRATION-GUARD-01
//   - INVARIANT: INTEGRATION-GUARD-POSTGRES-NO-ENVGATE-01
//   - INVARIANT: INTEGRATION-GUARD-COREBUNDLE-SELF-DSN-01
//   - INVARIANT: INTEGRATION-GUARD-DOCKER-BEFORE-RUN-01
//
// integration_guard_invariants_test.go consolidates the integration-test
// hygiene invariants — integration containers/tests must self-provision and
// fail-fast, never skip or env-gate. Promoted from integration_guard_test.go
// per ai-robust.md §"archtest 文件命名" (≥3 同主题 invariants →
// {theme}_invariants_test.go, each ID listed). The four guards were previously
// filed under the single vault-specific INTEGRATION-GUARD-01 header even
// though three cover postgres / corebundle / docker (#1491):
//   - INTEGRATION-GUARD-01: vault integration container failures must
//     fail-fast without hanging — TestVaultContainerStartersFailFast.
//   - INTEGRATION-GUARD-POSTGRES-NO-ENVGATE-01: postgres-unreachable tests
//     must not be env-gated; they run unconditionally —
//     TestPostgresUnreachableHostIsNotEnvGated.
//   - INTEGRATION-GUARD-COREBUNDLE-SELF-DSN-01: corebundle outbox wiring must
//     self-provision, not gate on an external DSN —
//     TestCorebundleOutboxWiringDoesNotUseExternalDSNGate.
//   - INTEGRATION-GUARD-DOCKER-BEFORE-RUN-01: testcontainer helpers must check
//     RequireDocker before Run — TestTestcontainerHelpersRequireDockerBeforeRun.
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
//
// Detector blind spots (ai-robust 盲区自检, asserted absent by
// detectorBlindSpotFindings below): the selector-name + import-alias detection
// cannot see these AST shapes, so each would silently bypass the no-skip scan
// rather than fail it. The reverse self-check asserts the vault test AST
// contains none of them today; a future occurrence fails this test:
//   - dot-import of a testcontainers core/module package — importSelectorName
//     returns "" for ".", blanking testcontainerAliasesFor's alias map, so a
//     bare Run(...) is invisible to isTestcontainerRun and the whole func is
//     never counted as a starter (its skips never scanned).
//   - a testcontainer constructor (modules .Run / core .GenericContainer) taken
//     as a function value (e.g. r := k3s.Run; r(...)) instead of called via
//     selector — firstTestcontainerRunPos only matches the call-position
//     selector form.
//   - t.Skip/Skipf/SkipNow taken as a function value (skip := t.Skip; skip())
//     — selectorName resolves .Sel.Name only when the selector is a call Fun, so
//     a value-form skip evades the skip scan.
//
// A typed (go/types) rewrite of the whole file would subsume these by resolving
// objects instead of selector names; that is the Hard ceiling tracked at
// gh #1466, not this PR's scope.
func TestVaultContainerStartersFailFast(t *testing.T) {
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{"adapters/vault"},
		scanner.IncludeTests(),
		scanner.MatchRels(func(rel string) bool {
			return strings.HasSuffix(filepath.Base(rel), "_test.go")
		}),
	)
	testFiles, err := scope.Files()
	require.NoError(t, err)

	fset := token.NewFileSet()
	var starters int
	var skipCalls []string
	var blindSpots []string
	for _, filePath := range testFiles {
		file, perr := parser.ParseFile(fset, filePath, nil, 0)
		require.NoError(t, perr)
		// Scan blind-spot forms before the alias-empty skip below: a dot-import
		// blanks the alias map, which would otherwise drop the file at the continue
		// and hide exactly the bypass the reverse self-check exists to catch.
		blindSpots = append(blindSpots, detectorBlindSpotFindings(fset, file)...)
		aliases := testcontainerAliasesFor(file)
		if len(aliases.core)+len(aliases.modules) == 0 {
			continue
		}
		scanner.EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
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
	assert.Empty(t, blindSpots,
		"detector blind-spot forms must be absent from vault test AST (see "+
			"TestVaultContainerStartersFailFast godoc); a new one would silently bypass the no-skip scan")
}

// detectorBlindSpotFindings reports the AST forms the selector-name + alias
// detection in TestVaultContainerStartersFailFast cannot see (enumerated in that
// test's godoc). It is the ai-robust reverse self-check: these forms must be
// absent from production (vault test) AST, so the Medium coverage is not silently
// bypassed.
func detectorBlindSpotFindings(fset *token.FileSet, file *ast.File) []string {
	findings := dotImportedTestcontainerFindings(fset, file)
	return append(findings, functionValueSelectorFindings(fset, file)...)
}

// dotImportedTestcontainerFindings flags a dot-import of a testcontainers core or
// module package: importSelectorName returns "" for ".", blanking the alias map
// in testcontainerAliasesFor so a bare Run(...) becomes invisible to detection.
func dotImportedTestcontainerFindings(fset *token.FileSet, file *ast.File) []string {
	var findings []string
	for _, imp := range file.Imports {
		if imp.Name == nil || imp.Name.Name != "." {
			continue
		}
		path := archStringLiteralValue(imp.Path)
		if path == "github.com/testcontainers/testcontainers-go" ||
			strings.HasPrefix(path, "github.com/testcontainers/testcontainers-go/modules/") {
			findings = append(findings, fset.Position(imp.Pos()).String()+
				": dot-import of a testcontainers package blanks the alias map and hides bare Run(...) from detection")
		}
	}
	return findings
}

// functionValueSelectorFindings flags a testcontainer constructor (.Run /
// .GenericContainer) or t.Skip/Skipf/SkipNow used as a function value rather than
// in call position: both evade the call-position selector matching in
// firstTestcontainerRunPos and the skip scan. A selector node that is the Fun of
// a CallExpr is in call position (detectable); any other occurrence of these
// selector names is a value-form blind spot.
func functionValueSelectorFindings(fset *token.FileSet, file *ast.File) []string {
	calledFuns := map[ast.Expr]struct{}{}
	scanner.EachInSubtree[ast.CallExpr](file, func(call *ast.CallExpr) {
		calledFuns[call.Fun] = struct{}{}
	})

	var findings []string
	scanner.EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		switch sel.Sel.Name {
		case "Run", "GenericContainer", "Skip", "Skipf", "SkipNow":
		default:
			return
		}
		if _, called := calledFuns[sel]; called {
			return
		}
		findings = append(findings, fset.Position(sel.Pos()).String()+
			": "+sel.Sel.Name+" used as a function value evades call-position selector detection")
	})
	return findings
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
	scanner.EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
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
	scanner.EachInChildren[ast.FuncDecl](file, func(fn *ast.FuncDecl) {
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
