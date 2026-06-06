package archtest

// celltest_import_scope.go — importable CELLTEST-IMPORT-SCOPE-01 rule logic (#1640 M3 PR-9).
//
// Non-test home for CELLTEST-IMPORT-SCOPE-01 scanner logic, so it can be
// compiled and run by an external Cell repository (Go never compiles a
// dependency's _test.go, so rule logic external repos must run cannot live in
// a _test.go file). GoCell's own TestCelltestImportScope in
// celltest_import_scope_test.go dogfoods CheckCelltestImportScope — single
// source, no parallel rule body.
//
// Rule: CELLTEST-IMPORT-SCOPE-01 — production Go files must not import packages
// matching cells/[a-z]+/[a-z]+test$ (the cells/{X}/{X}test/ naming pattern
// designates test-infrastructure packages).
//
// Dogfood test: TestCelltestImportScope (celltest_import_scope_test.go).
//
// # Register status
//
// Not registered in StandardCellRules: the rule enforces gocell's
// cells/{X}/{X}test test-infra layout convention (celltestImportPathPattern).
// An external cell repo not adopting this exact naming → no matches (vacuous);
// one using a different test-infra layout → not covered. To avoid imposing
// gocell's specific layout on forks it is kept importable + module-path-agnostic
// + fork-safe — an external repo following the same convention can call
// CheckCelltestImportScope directly.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// celltestImportPathPattern matches module-relative import paths whose final
// segment is `[a-z]+test` under cells/{X}/ (e.g. cells/configcore/configcoretest).
// The trailing `$` anchors the match to the top-level testutil package — nested
// sub-packages (cells/configcore/configcoretest/sub) are intentionally NOT
// matched here; they should be ban-covered by their parent package's
// boundary, which an importer must traverse through the matched root first.
var celltestImportPathPattern = regexp.MustCompile(`^github\.com/[^/]+/[^/]+/cells/[a-z]+/[a-z]+test$`)

// isCellTestImportPath reports whether an absolute import path matches the
// cells/{X}/{X}test naming convention.
//
// Blind spots: the pattern matches the full path literal from the import
// declaration; it does not follow type aliases or build-tag-gated imports.
// Both of those forms are effectively impossible to use as a production import
// without also triggering a Go compilation error, so they are acceptable
// non-coverage.
func isCellTestImportPath(importPath string) bool {
	return celltestImportPathPattern.MatchString(importPath)
}

// celltestScopeIsTestInfraPath reports whether rel (module-relative slash path)
// belongs to a test-infrastructure directory. Mirrored from isTestInfraPath in
// testutil_boundary_test.go (which cannot be called from non-test files).
func celltestScopeIsTestInfraPath(rel string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) > 0 && parts[0] == "tests" {
		return true
	}
	for _, p := range parts {
		if p == "testutil" {
			return true
		}
		// Segment ending in "test" (outboxtest, locktest, healthtest, archtest,
		// pgtest, authtest, configcoretest, …): anything >4 chars ending in "test".
		if len(p) > 4 && strings.HasSuffix(p, "test") {
			return true
		}
	}
	return false
}

// CheckCelltestImportScope enforces CELLTEST-IMPORT-SCOPE-01:
//
// No production Go file (i.e. NOT *_test.go and NOT in a test-infrastructure
// directory) may import a package whose import path matches
// cells/[a-z]+/[a-z]+test$. Those packages are cell-level testutil packages
// and importing them from production code would embed test fixtures and
// t-bound helpers in a release binary, signaling a layering mistake.
//
// The check is discovery-based: any new cells/{X}/{X}test/ package is
// automatically covered without further edits to this file.
//
// t.Fatalf is used only for infrastructure load failures (collectGoFiles,
// parseImports, moduleImportPath). Violations are returned as []Diagnostic
// and never cause t.Errorf — callers use Report to surface them.
func CheckCelltestImportScope(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("CheckCelltestImportScope: cannot read module path: %v", err)
	}

	allGoFiles, err := collectGoFiles(root)
	if err != nil {
		t.Fatalf("CheckCelltestImportScope: failed to collect .go files: %v", err)
	}
	if len(allGoFiles) == 0 {
		t.Fatalf("CheckCelltestImportScope: no .go files found — module root may be wrong")
	}

	var diags []Diagnostic
	for _, f := range allGoFiles {
		fileDiags, ferr := celltestScopeCheckFile(root, modPath, f)
		if ferr != nil {
			t.Fatalf("CheckCelltestImportScope: %v", ferr)
		}
		diags = append(diags, fileDiags...)
	}
	return diags
}

// celltestScopeCheckFile checks a single file for forbidden celltest imports.
// It returns (nil, nil) for files that are exempt (_test.go, test-infra paths).
// It returns (nil, non-nil error) for infrastructure failures (filepath.Rel,
// parseImports) so the caller can t.Fatalf them.
func celltestScopeCheckFile(root, modPath, f string) ([]Diagnostic, error) {
	if strings.HasSuffix(f, "_test.go") {
		return nil, nil
	}
	rel, rerr := filepath.Rel(root, f)
	if rerr != nil {
		return nil, fmt.Errorf("filepath.Rel: %w", rerr)
	}
	rel = filepath.ToSlash(rel)
	if celltestScopeIsTestInfraPath(rel) {
		return nil, nil
	}

	imports, perr := parseImports(f)
	if perr != nil {
		return nil, fmt.Errorf("failed to parse %s: %w", f, perr)
	}
	var diags []Diagnostic
	for _, imp := range imports {
		if !strings.HasPrefix(imp, modPath+"/") {
			continue
		}
		if isCellTestImportPath(imp) {
			diags = append(diags, Diagnostic{
				Rel:  rel,
				Line: 1,
				Message: fmt.Sprintf(
					"production file imports %s (cells/*/*test packages are test-infrastructure; "+
						"only *_test.go or test-infra packages may import them)", imp),
			})
		}
	}
	return diags, nil
}
