// INVARIANT: PG-TESTCONTAINER-FUNNEL-01
//
// # PG-TESTCONTAINER-FUNNEL-01
//
// tests/testutil/pgclone is the single sanctioned holder of
// `tcpostgres.Run` (testcontainers postgres module). Every PG integration
// test must obtain a per-test database via pgclone (directly, or via the
// pgshare wrapper for packages that can import adapters/postgres) — never by
// starting its own per-test container. This is the recurrence guard for the
// adapters/postgres test-perf refactor: ~93 per-test container spin-ups
// (~237s) collapsed onto one shared container + template clones.
//
// # Why a pure-AST scan (not typed, not depguard)
//
// Every tcpostgres.Run call and every testcontainers import lives in a
// `//go:build integration` file. Neither golangci-lint (depguard) nor the
// archtest typed façade (RunTypedProduction) loads integration-tagged files
// by default — both would need a global `integration` build tag, which would
// drag every other linter across all integration files (funlen / gocognit /
// dupl / gosec) for no benefit. So this guard parses files with
// parser.ParseFile (which ignores build constraints) and matches the call by
// import-path + alias + selector — the same mechanism as the sibling
// TestTestcontainerHelpersRequireDockerBeforeRun in integration_guard_test.go.
//
// AI-robust grade: Medium. The callsite identity (import path
// "...modules/postgres" + alias-resolved `.Run` selector + file allowlist) is
// stronger than a string anchor but is archtest-bound, not compile-time. Hard
// is structurally unreachable for "ban a third-party symbol outside an
// allowlist" — Go has no import-visibility modifier, so the compiler cannot
// forbid importing a public package; pure-AST file-allowlist is the highest
// tier this rule shape reaches for integration-tagged code.
//
// Blind-spot inventory (per ai-robust.md §"工具选定后强制盲区自检"):
//   - Function-value reference `run := tcpostgres.Run; run(...)`: COVERED — the
//     scan walks every <alias>.Run *ast.SelectorExpr (assignment RHS, arg pass,
//     or CallExpr.Fun alike), not just call targets. RED fixture
//     internal/pgcontainerredfixture/indirect_ref.go pins this form.
//   - Dot-import `import . ".../modules/postgres"; Run(...)`: `Run` would be a
//     bare *ast.Ident, not a SelectorExpr, so the scan misses it. Closed by the
//     reverse self-test TestPG_TESTCONTAINER_FUNNEL_01_NoDotImportBlindSpot which
//     asserts no dot-import of the module exists in the repo (also globally
//     banned by revive dot-imports).
//   - Reflection-based construction: out of scope, treated as theoretical.
//
// Carve-outs (allowlisted, backlog #890: migrate to pgclone): cmd/corebundle,
// tests/integration/outbox_fullchain_test.go (exact file — l2atomicity migrated
// to pgclone in #598), examples/iotdevice, cells/accesscore/slices/identitymanage —
// each has a distinct container need (full-app e2e harness / independent
// migration set / full-chain). They are NOT the adapters/postgres bottleneck.
// Per ai-robust.md §Funnel 双向锁评级, this Medium-upstream transition form is
// tracked for closure by backlog #890.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const pgTestcontainerModulePath = "github.com/testcontainers/testcontainers-go/modules/postgres"

// pgTestcontainerFunnelAllowlist holds module-relative path prefixes (slash
// form) permitted to call tcpostgres.Run directly. tests/testutil/pgclone is
// the sanctioned funnel; the rest are backlogged carve-outs.
var pgTestcontainerFunnelAllowlist = []string{
	"tests/testutil/pgclone/", // the sanctioned holder
	// Backlog #890: migrate these to pgclone. Each has a distinct container need:
	"cmd/corebundle/", // full-app e2e + outbox wiring harness
	// Exact file, not the tests/integration/ tree: l2atomicity migrated to
	// pgclone (#598), leaving outbox_fullchain_test.go as the sole direct caller.
	// A new per-test container anywhere else under tests/integration/ now fails.
	"tests/integration/outbox_fullchain_test.go", // backlog #890: full-chain harness
	"examples/iotdevice/",                        // example with its own PG migration set
	"cells/accesscore/slices/identitymanage/",    // single-service PG adapter test
}

// TestPG_TESTCONTAINER_FUNNEL_01 fails if any file outside the allowlist calls
// tcpostgres.Run. adapters/postgres now mints per-test DBs via pgclone, so the
// sole sanctioned holder is tests/testutil/pgclone; this scan keeps it that way
// (the backlogged carve-outs are the only other allowed callers).
func TestPG_TESTCONTAINER_FUNNEL_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	findings := collectPGTestcontainerRunFindings(t, root)
	assert.Empty(t, findings,
		"tcpostgres.Run must appear only in tests/testutil/pgclone (or the backlogged carve-outs); "+
			"use pgclone.CloneDSN/EmptyDSN (or pgshare.NewPerTestPool) instead of starting a per-test container")
}

// TestPG_TESTCONTAINER_FUNNEL_01_AllowlistBoundary pins the #598 narrowing: the
// tests/integration carve-out is the exact outbox_fullchain_test.go file, not
// the whole tree. It guards against (a) a typo in the exact path that would stop
// matching the real caller, and (b) re-widening the entry back to a directory
// prefix that would silently re-admit l2atomicity or any future sibling.
func TestPG_TESTCONTAINER_FUNNEL_01_AllowlistBoundary(t *testing.T) {
	t.Parallel()
	assert.True(t, pgFunnelAllowed("tests/integration/outbox_fullchain_test.go"),
		"the sole full-chain caller must stay allowlisted")
	assert.False(t, pgFunnelAllowed("tests/integration/l2atomicity/harness_test.go"),
		"l2atomicity migrated to pgclone (#598); it must not be re-admitted by a tree-wide prefix")
	assert.False(t, pgFunnelAllowed("tests/integration/some_new_test.go"),
		"a new tests/integration file must not be allowlisted by a tree-wide prefix")
}

func collectPGTestcontainerRunFindings(t *testing.T, root string) []string {
	t.Helper()
	files, err := scanner.ModuleScope(root, scanner.IncludeTests()).Files()
	require.NoError(t, err)

	var findings []string
	for _, path := range files {
		rel := relSlash(root, path)
		// archtest's internal tree (this rule's RED fixture included) is excluded
		// at the scanner layer; see archtestInternalRel in internal/scanner/scope.go.
		if pgFunnelAllowed(rel) {
			continue
		}
		line, ok, perr := firstPostgresRunLine(path)
		require.NoError(t, perr)
		if ok {
			findings = append(findings, fmt.Sprintf(
				"%s:%d: forbidden tcpostgres.Run outside tests/testutil/pgclone", rel, line))
		}
	}
	return findings
}

func pgFunnelAllowed(rel string) bool {
	for _, prefix := range pgTestcontainerFunnelAllowlist {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// firstPostgresRunLine parses path and returns the line of the first
// `<alias>.Run` call where <alias> is the postgres testcontainers module's
// import name (handles named/aliased imports via importSelectorName).
func firstPostgresRunLine(path string) (int, bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return 0, false, err
	}
	alias, ok := postgresModuleAlias(file)
	if !ok {
		return 0, false, nil
	}
	var pos token.Pos
	// Scan every <alias>.Run SelectorExpr, not just CallExpr.Fun — this also
	// catches indirect references like `run := tcpostgres.Run` (function-value
	// assignment) and `pass(tcpostgres.Run)`, which a CallExpr-only scan misses.
	scanner.EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if pos.IsValid() {
			return
		}
		if isPostgresModuleRun(sel, alias) {
			pos = sel.Pos()
		}
	})
	if pos.IsValid() {
		return fset.Position(pos).Line, true, nil
	}
	return 0, false, nil
}

// postgresModuleAlias returns the local import name bound to the testcontainers
// postgres module in file, or ("", false) if the module is not imported. A
// dot-import (name ".") returns ("", false) — that blind spot is closed by the
// dedicated reverse self-test below.
func postgresModuleAlias(file *ast.File) (string, bool) {
	for _, imp := range file.Imports {
		if archStringLiteralValue(imp.Path) != pgTestcontainerModulePath {
			continue
		}
		if name := importSelectorName(imp, "postgres"); name != "" {
			return name, true
		}
	}
	return "", false
}

func isPostgresModuleRun(expr ast.Expr, alias string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return id.Name == alias && sel.Sel.Name == "Run"
}

// TestPG_TESTCONTAINER_FUNNEL_01_RedFixture asserts the detector fires on a
// known-positive: tools/archtest/internal/pgcontainerredfixture/fixture.go
// deliberately calls tcpostgres.Run. Without this, a broken detector would
// silently pass the production scan.
func TestPG_TESTCONTAINER_FUNNEL_01_RedFixture(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	fixturePath := filepath.Join(root, "tools", "archtest", "internal", "pgcontainerredfixture", "fixture.go")
	line, ok, err := firstPostgresRunLine(fixturePath)
	require.NoError(t, err, "parse RED fixture")
	if !ok {
		t.Error("PG-TESTCONTAINER-FUNNEL-01 RED fixture: detector found no tcpostgres.Run in fixture.go; " +
			"firstPostgresRunLine / isPostgresModuleRun may be broken")
		return
	}
	t.Logf("RED fixture hit at fixture.go:%d", line)
}

// TestPG_TESTCONTAINER_FUNNEL_01_RedFixture_IndirectRef pins the F3 fix: the
// SelectorExpr scan must catch the function-value form `var indirectRun =
// tcpostgres.Run` in internal/pgcontainerredfixture/indirect_ref.go. A
// CallExpr-only scan (the original bug) would miss it.
func TestPG_TESTCONTAINER_FUNNEL_01_RedFixture_IndirectRef(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	fixturePath := filepath.Join(root, "tools", "archtest", "internal", "pgcontainerredfixture", "indirect_ref.go")
	line, ok, err := firstPostgresRunLine(fixturePath)
	require.NoError(t, err, "parse indirect-ref RED fixture")
	if !ok {
		t.Error("PG-TESTCONTAINER-FUNNEL-01 indirect-ref RED fixture: scan found no tcpostgres.Run " +
			"function-value reference; the SelectorExpr scan may have regressed to CallExpr-only")
		return
	}
	t.Logf("indirect-ref RED fixture hit at indirect_ref.go:%d", line)
}

// TestPG_TESTCONTAINER_FUNNEL_01_NoDotImportBlindSpot closes the dot-import
// blind spot: `import . ".../modules/postgres"` would make Run a bare ident
// that isPostgresModuleRun (SelectorExpr-only) misses. Asserts no file
// dot-imports the module (also globally banned by revive dot-imports).
func TestPG_TESTCONTAINER_FUNNEL_01_NoDotImportBlindSpot(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files, err := scanner.ModuleScope(root, scanner.IncludeTests()).Files()
	require.NoError(t, err)

	var findings []string
	for _, path := range files {
		rel := relSlash(root, path)
		dot, perr := fileDotImportsPostgresModule(path)
		require.NoError(t, perr)
		if dot {
			findings = append(findings, rel+": dot-import of testcontainers postgres module evades PG-TESTCONTAINER-FUNNEL-01")
		}
	}
	assert.Empty(t, findings,
		"dot-import of the testcontainers postgres module would make Run a bare ident and evade the funnel; forbidden")
}

func fileDotImportsPostgresModule(path string) (bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return false, err
	}
	for _, imp := range file.Imports {
		if archStringLiteralValue(imp.Path) != pgTestcontainerModulePath {
			continue
		}
		if imp.Name != nil && imp.Name.Name == "." {
			return true, nil
		}
	}
	return false, nil
}
