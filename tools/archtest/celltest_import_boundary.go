package archtest

// celltest_import_boundary.go — importable CELLTEST-IMPORT-BOUNDARY-01 rule
// logic (#1640 M3 PR-9).
//
// Non-test home for CELLTEST-IMPORT-BOUNDARY-01 scanner logic, so it can be
// compiled and run by an external Cell repository (Go never compiles a
// dependency's _test.go, so rule logic external repos must run cannot live in
// a _test.go file). GoCell's own TestCelltestImportBoundary in
// celltest_import_boundary_test.go dogfoods CheckCelltestImportBoundary —
// single source, no parallel rule body.
//
// Rule: CELLTEST-IMPORT-BOUNDARY-01 — enforces three sub-rules:
//
//   - CELLTEST-A: Non-_test.go files must not import kernel/cell/celltest.
//   - CELLTEST-B: kernel/** (excluding celltest itself and kernel/cell/ _test.go)
//     must not import kernel/cell/celltest.
//   - CELLTEST-C: examples/** non-_test.go files must not import
//     kernel/cell/celltest.
//
// Dogfood test: TestCelltestImportBoundary (celltest_import_boundary_test.go).
//
// # Register status
//
// Not registered in StandardCellRules: allowlist/scan-scope is gocell-hardcoded
// (kernel/cell/celltest path, examples/ prefix) with no ConfigForExternalCell
// consumer-extension — vacuous/false-red externally; kept importable +
// module-path-agnostic + fork-safe.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// CheckCelltestImportBoundary enforces CELLTEST-IMPORT-BOUNDARY-01 (A/B/C)
// across the full module:
//
//   - A: no non-_test.go file (except celltest's own sources) imports celltest.
//   - B: no kernel/ file (except celltest itself and kernel/cell/ _test.go)
//     imports celltest.
//   - C: no examples/ non-_test.go file imports celltest.
//
// t.Fatalf is used only for infrastructure failures (collectGoFiles,
// parseImports, moduleImportPath). Violations are returned as []Diagnostic;
// callers use Report to surface them.
func CheckCelltestImportBoundary(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	if err != nil {
		t.Fatalf("CheckCelltestImportBoundary: cannot read module path: %v", err)
	}
	celltestImport := modPath + "/kernel/cell/celltest"

	allGoFiles, err := collectGoFiles(root)
	if err != nil {
		t.Fatalf("CheckCelltestImportBoundary: failed to collect .go files: %v", err)
	}
	if len(allGoFiles) == 0 {
		t.Fatalf("CheckCelltestImportBoundary: no .go files found — module root may be wrong")
	}

	celltestPkgDir := filepath.Join(root, "kernel", "cell", "celltest")
	celltestParentDir := filepath.Join(root, "kernel", "cell")

	var diags []Diagnostic
	diags = append(diags, celltestSubA(t, root, allGoFiles, celltestPkgDir, celltestImport)...)
	diags = append(diags, celltestSubB(t, root, allGoFiles, celltestPkgDir, celltestParentDir, celltestImport)...)
	diags = append(diags, celltestSubC(t, root, allGoFiles, celltestImport)...)
	return diags
}

// celltestImportLineOr1 returns the 1-based source line of importPath's import
// spec in file f, falling back to 1 when the spec cannot be located (parse
// hiccup) so the diagnostic stays clickable. Shared by the celltest boundary
// sub-rules and celltestScopeCheckFile.
func celltestImportLineOr1(f, importPath string) int {
	if line := importLine(f, importPath); line > 0 {
		return line
	}
	return 1
}

// celltestSubA checks CELLTEST-A: non-_test.go files (except celltest's own
// sources) must not import celltest.
func celltestSubA(t *testing.T, root string, allGoFiles []string, celltestPkgDir, celltestImport string) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	for _, f := range allGoFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		if filepath.Dir(f) == celltestPkgDir {
			continue
		}
		imports, err := parseImports(f)
		if err != nil {
			t.Fatalf("CheckCelltestImportBoundary/A: failed to parse %s: %v", f, err)
		}
		for _, imp := range imports {
			if imp == celltestImport {
				rel, _ := filepath.Rel(root, f)
				rel = filepath.ToSlash(rel)
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: celltestImportLineOr1(f, celltestImport),
					Message: fmt.Sprintf("CELLTEST-A: non-test file imports %s — "+
						"production code must use auth.NewAuthJWT / auth.NewAuthJWTFromAssembly / "+
						"auth.NewAuthServiceToken (error-first) instead", celltestImport),
				})
			}
		}
	}
	return diags
}

// celltestSubB checks CELLTEST-B: kernel/** (except celltest itself and
// kernel/cell/ _test.go) must not import celltest.
func celltestSubB(t *testing.T, root string, allGoFiles []string, celltestPkgDir, celltestParentDir, celltestImport string) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	for _, f := range allGoFiles {
		if filepath.Dir(f) == celltestPkgDir {
			continue
		}
		if filepath.Dir(f) == celltestParentDir && strings.HasSuffix(f, "_test.go") {
			continue
		}
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, "kernel/") {
			continue
		}
		imports, err := parseImports(f)
		if err != nil {
			t.Fatalf("CheckCelltestImportBoundary/B: failed to parse %s: %v", f, err)
		}
		for _, imp := range imports {
			if imp == celltestImport {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: celltestImportLineOr1(f, celltestImport),
					Message: fmt.Sprintf("CELLTEST-B: kernel file imports %s — "+
						"kernel packages (including _test.go) must not import "+
						"kernel/cell/celltest; layering rule: kernel must not depend on "+
						"test-fixture sub-packages defined within itself", celltestImport),
				})
			}
		}
	}
	return diags
}

// celltestSubC checks CELLTEST-C: examples/** non-_test.go files must not
// import celltest.
func celltestSubC(t *testing.T, root string, allGoFiles []string, celltestImport string) []Diagnostic {
	t.Helper()
	var diags []Diagnostic
	for _, f := range allGoFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		if !strings.HasPrefix(rel, "examples/") {
			continue
		}
		imports, err := parseImports(f)
		if err != nil {
			t.Fatalf("CheckCelltestImportBoundary/C: failed to parse %s: %v", f, err)
		}
		for _, imp := range imports {
			if imp == celltestImport {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: celltestImportLineOr1(f, celltestImport),
					Message: fmt.Sprintf("CELLTEST-C: examples non-test file imports %s — "+
						"examples production code must use auth.NewAuth* (kernel/auth, error-first) "+
						"instead of celltest panic helpers", celltestImport),
				})
			}
		}
	}
	return diags
}
