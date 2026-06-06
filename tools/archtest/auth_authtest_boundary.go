package archtest

// auth_authtest_boundary.go — importable AUTH-AUTHTEST-BOUNDARY-01 rule logic
// (#1632 M3).
//
// This is the non-test home of the AUTH-AUTHTEST-BOUNDARY-01 scanner (plus the
// shared file-collection / import-parse helpers it and AUTH-KEYSTEST-IMPORT-
// BOUNDARY-01 use) so they can be compiled and run by an external Cell
// repository (Go never compiles a dependency's _test.go). GoCell's own
// TestAuthAuthtestBoundary in auth_authtest_boundary_test.go calls the same
// CheckAuthAuthtestBoundary — single source, no parallel rule body.
//
// # AUTH-AUTHTEST-BOUNDARY-01
//
// Enforces two rules around the two test-only authtest packages in the module:
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
// Pre-#638 this rule also enforced AUTH-AUTHTEST-B ("cells/**, examples/**,
// kernel/** must not import runtime/auth/authtest"). After moving the package
// to runtime/internal/authtest the Go compiler's internal/ rule refuses imports
// from outside the runtime/ subtree at compile time (cells/, examples/, kernel/,
// cmd/, adapters/, tools/, tests/ all blocked) — a Hard upgrade per
// ai-robust.md §Hard 范本目录 → "internal/ wrap 包". The B subtest + its
// negative probe are removed as redundant.
//
// Note: kernel/auth/authtest does NOT have an equivalent Hard upgrade path —
// its consumers span cmd/, runtime/, kernel/, tests/ subtrees, so no single
// internal/ placement can cover them. AUTH-AUTHTEST-C remains the boundary
// (Medium archtest, terminal grade per ai-robust §Funnel 双向锁评级).
//
// The two authtest packages are GoCell platform symbols, so their import paths
// are derived from PlatformModulePath (ARCHTEST-MODULE-PATH-FUNNEL-01: no bare
// literal); the scan scope is the running module (collectGoFiles).
//
// Not registered in StandardCellRules: the rule constrains GoCell's own
// internal/ authtest package layout with no ConfigForExternalCell
// consumer-extension → vacuous-green in an external module; kept importable &
// module-path-agnostic.

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

	scannerPkg "github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// CheckAuthAuthtestBoundary runs AUTH-AUTHTEST-BOUNDARY-01 (sub-rules A + C)
// over the running module and returns its diagnostics. It is the importable
// rule body; GoCell's TestAuthAuthtestBoundary calls it directly — single
// source, no parallel rule body.
func CheckAuthAuthtestBoundary(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	allGoFiles, err := collectGoFiles(root)
	if err != nil {
		t.Fatalf("failed to collect .go files: %v", err)
	}
	if len(allGoFiles) == 0 {
		t.Fatal("no .go files found — module root may be wrong")
	}
	var diags []Diagnostic
	diags = append(diags, scanAuthAuthenticatedCalls(t, root, allGoFiles)...)
	diags = append(diags, scanAuthtestImports(t, root, allGoFiles)...)
	return diags
}

// scanAuthAuthenticatedCalls is AUTH-AUTHTEST-A: ban auth.Authenticated() *call
// expressions* in all .go files. Detection is AST-based (findCallExpr) so that
// comments, doc strings, and unrelated identical strings inside string literals
// are not misclassified. Exclude tools/archtest itself (this rule references the
// symbol in fixture content) and the two authtest package dirs (their doc
// comments name the deleted function — comments are AST-stripped so this is
// precaution only).
func scanAuthAuthenticatedCalls(t *testing.T, root string, allGoFiles []string) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	for _, f := range allGoFiles {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "tools/archtest/") ||
			strings.HasPrefix(rel, "runtime/internal/authtest/") ||
			strings.HasPrefix(rel, "kernel/auth/authtest/") {
			continue
		}
		callHits, err := findCallExpr(f, "auth", "Authenticated")
		if err != nil {
			t.Fatalf("failed to AST-scan %s: %v", f, err)
		}
		for _, line := range callHits {
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: line,
				Message: "AUTH-AUTHTEST-A: auth.Authenticated() must not be called anywhere " +
					"in the codebase; the function has been deleted — use auth.AnyRole(...) " +
					"in production, authtest.RequireAuthenticated() in runtime _test.go files",
			})
		}
	}
	return diags
}

// scanAuthtestImports is AUTH-AUTHTEST-C: non-test Go files must not import
// EITHER authtest package (runtime/internal/authtest, kernel/auth/authtest).
// Exception: each authtest package's own source files are excluded (they are the
// implementation, not consumers).
func scanAuthtestImports(t *testing.T, root string, allGoFiles []string) []Diagnostic {
	t.Helper()
	authtestImports := map[string]bool{
		PlatformModulePath + "/runtime/internal/authtest": true,
		PlatformModulePath + "/kernel/auth/authtest":      true,
	}
	authtestPkgDirs := map[string]bool{
		filepath.Join(root, "runtime", "internal", "authtest"): true,
		filepath.Join(root, "kernel", "auth", "authtest"):      true,
	}

	var diags []Diagnostic
	for _, f := range allGoFiles {
		// _test.go files are permitted; authtest packages' own files are exempt.
		if strings.HasSuffix(f, "_test.go") || authtestPkgDirs[filepath.Dir(f)] {
			continue
		}
		imports, err := parseImports(f)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", f, err)
		}
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		for _, imp := range imports {
			if authtestImports[imp] {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: importLine(f, imp),
					Message: fmt.Sprintf(
						"AUTH-AUTHTEST-C: %s (non-test file) imports %s — only _test.go files "+
							"may import authtest; move your auth policy helper into a _test.go "+
							"file, or use auth.TestContext(subject, roles) for cell handler tests",
						rel, imp),
				})
			}
		}
	}
	return diags
}

// collectGoFiles returns absolute paths to all *.go files in the module,
// including _test.go files, skipping vendor, hidden directories, generated,
// testdata, worktrees, and node_modules (the mechanical exclusions performed
// by scannerPkg.ModuleScope). Shared by AUTH-AUTHTEST-BOUNDARY-01 and
// AUTH-KEYSTEST-IMPORT-BOUNDARY-01; rule-specific exclusions live in each Check.
//
//   - AUTH-AUTHTEST-A inlines `strings.HasPrefix(rel, "tools/archtest/")` plus
//     the two authtest package dirs to suppress AST false positives caused by
//     archtest's own fixture content / doc comments naming the deleted symbol.
//   - AUTH-AUTHTEST-C does NOT exclude tools/archtest — it scans imports, not
//     symbol names, so non-_test.go files at the top level of tools/archtest/
//     must obey the boundary. (tools/archtest/internal/** is framework-excluded
//     by scannerPkg.ModuleScope.archtestInternalRel — a documented fail-closed
//     decision so archtest fixtures can deliberately contain forbidden patterns;
//     see scanner/scope.go.) C's own scope exclusions are just _test.go and the
//     authtest packages' own implementation dirs.
func collectGoFiles(root string) ([]string, error) {
	// IncludeGenerated honors the rule's "anywhere in the module" docstring:
	// codegen output (generated/contracts/**) must also obey the boundary;
	// otherwise a regenerated handler reintroducing auth.Authenticated() or
	// importing runtime/internal/authtest would silently bypass the rule.
	scope := scannerPkg.ModuleScope(root, scannerPkg.IncludeTests(), scannerPkg.IncludeGenerated())
	return scope.Files()
}

// parseImports parses a single Go source file and returns the list of import
// paths it declares. Uses go/parser for correctness; does not execute any Go
// toolchain commands. Shared with AUTH-KEYSTEST-IMPORT-BOUNDARY-01.
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

// importLine returns the 1-based line of the import spec for importPath in the
// file at path, or 0 if absent / unparseable. Lets the import-boundary rules
// (AUTH-AUTHTEST-C / AUTH-KEYSTEST) attach a precise location to their
// diagnostics instead of rendering "file.go:0". Shared with
// AUTH-KEYSTEST-IMPORT-BOUNDARY-01.
func importLine(path, importPath string) int {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly|parser.SkipObjectResolution)
	if err != nil {
		return 0
	}
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, `"`) == importPath {
			return fset.Position(imp.Path.Pos()).Line
		}
	}
	return 0
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
