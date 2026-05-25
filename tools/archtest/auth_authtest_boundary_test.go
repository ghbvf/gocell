// INVARIANT: AUTH-AUTHTEST-BOUNDARY-01: authtest sub-package is test-only; auth.Authenticated() must stay deleted
package archtest

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	scannerPkg "github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestAuthAuthtestBoundary enforces two rules around the two test-only authtest
// packages in the module:
//
//   - `runtime/internal/authtest` — policy fixture (RequireAuthenticated; the
//     PR #267 substrate moved to internal/ by issue #638)
//   - `kernel/auth/authtest` — AuthPlan factory fixture (MustAuthJWT, etc.;
//     test-only Must* helpers used by composition-root test wiring)
//
// Rules:
//
//   - AUTH-AUTHTEST-A: no Go file anywhere in the module may contain the
//     literal call expression "auth.Authenticated()" — this seals the deleted
//     export and prevents accidental reintroduction.
//
//   - AUTH-AUTHTEST-C: non-test Go files (files not ending in _test.go) must
//     not import EITHER authtest package anywhere in the module — both
//     packages are exclusively for _test.go consumers.
//
// Each authtest package's own implementation files are exempt from
// AUTH-AUTHTEST-C (they ARE the implementation, not importers).
//
// # B retired (issue #638)
//
// Pre-#638 this file also enforced AUTH-AUTHTEST-B ("cells/**, examples/**,
// kernel/** must not import runtime/auth/authtest"). After moving the package
// to runtime/internal/authtest the Go compiler's internal/ rule refuses imports
// from outside the runtime/ subtree at compile time (cells/, examples/, kernel/,
// cmd/, adapters/, tools/, tests/ all blocked) — a Hard upgrade per
// ai-robust.md §Hard 范本目录 → "internal/ wrap 包". The archtest B subtest +
// its negative probe are removed as redundant.
//
// Note: kernel/auth/authtest does NOT have an equivalent Hard upgrade path —
// its consumers span cmd/, runtime/, kernel/, tests/ subtrees, so no single
// internal/ placement can cover them. AUTH-AUTHTEST-C remains the boundary
// (Medium archtest, terminal grade per ai-robust §Funnel 双向锁评级).
func TestAuthAuthtestBoundary(t *testing.T) {
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)
	authtestImports := []string{
		modPath + "/runtime/internal/authtest",
		modPath + "/kernel/auth/authtest",
	}

	// Collect all .go files once and share across rules.
	allGoFiles, err := collectGoFiles(root)
	require.NoError(t, err, "failed to collect .go files")
	require.NotEmpty(t, allGoFiles, "no .go files found — module root may be wrong")

	// AUTH-AUTHTEST-A: ban auth.Authenticated() *call expressions* in all .go
	// files. Detection is AST-based (go/parser → scanner.EachInSubtree[ast.CallExpr]
	// with SelectorExpr Fun X.auth, Sel.Authenticated) so that comments, doc strings,
	// commit-message-style log messages, and unrelated identical strings inside
	// string literals are not misclassified as violations. Exclude tools/archtest
	// itself (this file references the symbol in test names and probe content)
	// and runtime/internal/authtest (the replacement package's doc comment names
	// the deleted function — comments are AST-stripped so this is precaution only).
	t.Run("AUTH-AUTHTEST-A_no_auth_Authenticated_call", func(t *testing.T) {
		var hits []string
		for _, f := range allGoFiles {
			rel, _ := filepath.Rel(root, f)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "tools/archtest/") ||
				strings.HasPrefix(rel, "runtime/internal/authtest/") ||
				strings.HasPrefix(rel, "kernel/auth/authtest/") {
				continue
			}
			callHits, err := findCallExpr(f, "auth", "Authenticated")
			require.NoErrorf(t, err, "failed to AST-scan %s", f)
			for _, line := range callHits {
				hits = append(hits, fmt.Sprintf("%s:%d", rel, line))
			}
		}
		if len(hits) > 0 {
			for _, h := range hits {
				t.Logf("AUTH-AUTHTEST-A violation (real call expression): %s", h)
			}
		}
		assert.Empty(t, hits,
			"auth.Authenticated() must not be called anywhere in the codebase; "+
				"the function has been deleted — use auth.AnyRole(...) in production, "+
				"authtest.RequireAuthenticated() in runtime _test.go files")
	})

	// AUTH-AUTHTEST-C: non-test Go files must not import EITHER authtest package
	// (runtime/internal/authtest, kernel/auth/authtest). Exception: each
	// authtest package's own source files are excluded (they are the
	// implementation, not consumers).
	t.Run("AUTH-AUTHTEST-C_only_test_files_may_import_authtest", func(t *testing.T) {
		authtestPkgDirs := []string{
			filepath.Join(root, "runtime", "internal", "authtest"),
			filepath.Join(root, "kernel", "auth", "authtest"),
		}
		isAuthtestPkgDir := func(dir string) bool {
			for _, p := range authtestPkgDirs {
				if dir == p {
					return true
				}
			}
			return false
		}
		isAuthtestImport := func(imp string) bool {
			for _, p := range authtestImports {
				if imp == p {
					return true
				}
			}
			return false
		}

		var violations []string
		for _, f := range allGoFiles {
			// Skip _test.go files — they are permitted by this rule.
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			// Skip the authtest packages' own implementation files.
			if isAuthtestPkgDir(filepath.Dir(f)) {
				continue
			}
			imports, err := parseImports(f)
			require.NoError(t, err, "failed to parse %s", f)
			for _, imp := range imports {
				if isAuthtestImport(imp) {
					rel, _ := filepath.Rel(root, f)
					rel = filepath.ToSlash(rel)
					violations = append(violations,
						fmt.Sprintf("AUTH-AUTHTEST-C: %s (non-test file) imports %s (only _test.go files may import authtest)", rel, imp))
				}
			}
		}
		if len(violations) > 0 {
			for _, v := range violations {
				t.Logf("%s", v)
			}
		}
		assert.Empty(t, violations,
			"non-test .go files must not import runtime/internal/authtest or kernel/auth/authtest; "+
				"move your auth policy helper into a _test.go file, or use "+
				"auth.TestContext(subject, roles) for cell handler tests")
	})
}

// collectGoFiles returns absolute paths to all *.go files in the module,
// including _test.go files, skipping vendor, hidden directories, generated,
// testdata, worktrees, and node_modules. tools/archtest is excluded to avoid
// archtest scanning its own source for rule violations that reference forbidden
// strings in comments/test names. Both AUTH-AUTHTEST-A and AUTH-AUTHTEST-C
// rely on this list — A applies its own inline exemption for the authtest
// package dirs; C operates on the same list, which already skips
// tools/archtest, so archtest comments mentioning the forbidden import paths
// do not produce false positives.
func collectGoFiles(root string) ([]string, error) {
	// IncludeGenerated honors the rule's "anywhere in the module" docstring:
	// codegen output (generated/contracts/**) must also obey the boundary;
	// otherwise a regenerated handler reintroducing auth.Authenticated() or
	// importing runtime/internal/authtest would silently bypass the rule.
	scope := scannerPkg.ModuleScope(root, scannerPkg.IncludeTests(), scannerPkg.IncludeGenerated())
	all, err := scope.Files()
	if err != nil {
		return nil, err
	}
	archtestRel := filepath.Join("tools", "archtest") + string(filepath.Separator)
	archtestRelExact := filepath.Join("tools", "archtest")
	var files []string
	for _, f := range all {
		rel, relErr := filepath.Rel(root, f)
		if relErr != nil {
			continue
		}
		if rel == archtestRelExact || strings.HasPrefix(rel, archtestRel) {
			continue
		}
		files = append(files, f)
	}
	return files, nil
}

// parseImports parses a single Go source file and returns the list of import
// paths it declares. Uses go/parser for correctness; does not execute any Go
// toolchain commands.
func parseImports(path string) ([]string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.ImportsOnly|parser.SkipObjectResolution)
	if err != nil {
		return rawScanImports(data, path), err
	}
	var imports []string
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		imports = append(imports, path)
	}
	return imports, nil
}

// findCallExpr parses path with full AST and returns the line numbers of every
// call expression of the form "<pkg>.<sel>(...)" where the receiver matches
// pkgIdent and the selector matches selName. Comments and string literals do
// not match because they are not represented as ast.CallExpr nodes — the entire
// point of moving rule A from text grep to AST detection is to avoid those
// false positives. Parse failures are returned to make malformed files fail
// the archtest directly.
func findCallExpr(path, pkgIdent, selName string) ([]int, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var lines []int
	scannerPkg.EachInSubtree[ast.CallExpr](f, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return
		}
		if ident.Name == pkgIdent && sel.Sel.Name == selName {
			lines = append(lines, fset.Position(call.Lparen).Line)
		}
	})
	return lines, nil
}

// rawScanImports is a line-scanner fallback used when go/parser fails (e.g.
// build-tag-only files). It extracts quoted import paths from import blocks.
func rawScanImports(data []byte, _ string) []string {
	var imports []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	inImport := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == `import (` || line == "import(" {
			inImport = true
			continue
		}
		if inImport && line == ")" {
			inImport = false
			continue
		}
		if inImport || strings.HasPrefix(line, `import "`) {
			// Extract the quoted path.
			start := strings.Index(line, `"`)
			end := strings.LastIndex(line, `"`)
			if start >= 0 && end > start {
				imports = append(imports, line[start+1:end])
			}
		}
	}
	return imports
}

// TestAuthAuthtestBoundary_NegativeProbes validates that the rule checks
// themselves work correctly (test-the-test) using synthetic fixtures.
func TestAuthAuthtestBoundary_NegativeProbes(t *testing.T) {
	t.Parallel()

	const modPath = "github.com/ghbvf/gocell"
	runtimeAuthtestImport := modPath + "/runtime/internal/authtest"
	kernelAuthtestImport := modPath + "/kernel/auth/authtest"

	// Probe A1: findCallExpr must detect a real auth.Authenticated() call site.
	t.Run("A1_findCallExpr_detects_real_call", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		bogus := filepath.Join(tmp, "bogus_test.go")
		// Real call expression — must be detected.
		if err := os.WriteFile(bogus, []byte("package x\nvar _ = auth.Authenticated()\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		hits, err := findCallExpr(bogus, "auth", "Authenticated")
		require.NoError(t, err)
		assert.NotEmpty(t, hits,
			"negative probe A1: findCallExpr must detect real auth.Authenticated() call expression")
	})

	// Probe A2: findCallExpr must IGNORE the literal text inside string constants
	// and comments (the headline reason rule A was upgraded from grepInDir to
	// AST). Without this guarantee, doc strings or audit-message templates that
	// happen to mention auth.Authenticated() would noise CI red.
	t.Run("A2_findCallExpr_ignores_strings_and_comments", func(t *testing.T) {
		t.Parallel()
		tmp := t.TempDir()
		decoy := filepath.Join(tmp, "decoy_test.go")
		const decoyContent = `package x
// auth.Authenticated() — historical reference in a comment, must not match.
var msg = "auth.Authenticated() — string literal mentioning the symbol, must not match."
`
		if err := os.WriteFile(decoy, []byte(decoyContent), 0o644); err != nil {
			t.Fatal(err)
		}
		hits, err := findCallExpr(decoy, "auth", "Authenticated")
		require.NoError(t, err)
		assert.Empty(t, hits,
			"negative probe A2: findCallExpr must NOT match auth.Authenticated() inside comments or string literals")
	})

	// Probe C1: a non-test file importing runtime/internal/authtest must be caught.
	t.Run("C1_detects_non_test_runtime_authtest_import", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		pkgDir := filepath.Join(root, "runtime", "somepackage")
		require.NoError(t, os.MkdirAll(pkgDir, 0o755))

		content := fmt.Sprintf("package somepackage\nimport _ %q\n", runtimeAuthtestImport)
		nonTestFile := filepath.Join(pkgDir, "helpers.go") // NOT _test.go
		require.NoError(t, os.WriteFile(nonTestFile, []byte(content), 0o644))

		imports, err := parseImports(nonTestFile)
		require.NoError(t, err)
		found := false
		for _, imp := range imports {
			if imp == runtimeAuthtestImport {
				found = true
			}
		}
		assert.True(t, found, "negative probe C1: parseImports must detect runtime/internal/authtest import in non-test file")
		assert.False(t, strings.HasSuffix(nonTestFile, "_test.go"),
			"negative probe C1: fixture file must not be a _test.go file")
	})

	// Probe C2: a non-test file importing kernel/auth/authtest must also be caught.
	// Mirrors C1 for the second authtest package to ensure AUTH-AUTHTEST-C
	// covers both paths uniformly.
	t.Run("C2_detects_non_test_kernel_authtest_import", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		pkgDir := filepath.Join(root, "kernel", "someother")
		require.NoError(t, os.MkdirAll(pkgDir, 0o755))

		content := fmt.Sprintf("package someother\nimport _ %q\n", kernelAuthtestImport)
		nonTestFile := filepath.Join(pkgDir, "helpers.go") // NOT _test.go
		require.NoError(t, os.WriteFile(nonTestFile, []byte(content), 0o644))

		imports, err := parseImports(nonTestFile)
		require.NoError(t, err)
		found := false
		for _, imp := range imports {
			if imp == kernelAuthtestImport {
				found = true
			}
		}
		assert.True(t, found, "negative probe C2: parseImports must detect kernel/auth/authtest import in non-test file")
		assert.False(t, strings.HasSuffix(nonTestFile, "_test.go"),
			"negative probe C2: fixture file must not be a _test.go file")
	})
}
