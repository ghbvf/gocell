package archtest

// auth_keystest_boundary.go — importable AUTH-KEYSTEST-IMPORT-BOUNDARY-01 rule
// logic (#1632 M3).
//
// This is the non-test home of the AUTH-KEYSTEST-IMPORT-BOUNDARY-01 scanner so
// it can be compiled and run by an external Cell repository (Go never compiles a
// dependency's _test.go). GoCell's own TestAuthKeystestBoundary in
// auth_keystest_boundary_test.go calls the same CheckAuthKeystestBoundary —
// single source, no parallel rule body. The file-collection / import-parse
// helpers (collectGoFiles, parseImports) live in auth_authtest_boundary.go and
// are shared.
//
// # AUTH-KEYSTEST-IMPORT-BOUNDARY-01
//
// The runtime/auth/keystest sub-package is test-only. It provides ephemeral
// RSA key-pair helpers (MustGenerateKeyPair, MustNewKeySet, MustNewKeyProvider)
// that panic on RNG failure and are designed exclusively for use in _test.go
// files and test binaries.
//
// This rule enforces four boundaries:
//
//   - AUTH-KEYSTEST-A: kernel/** must not import keystest (kernel must not
//     depend on runtime/ — hard layering violation).
//
//   - AUTH-KEYSTEST-B: cells/** non-_test.go files must not import keystest
//     (cells must not depend on test-only helpers in production code paths).
//
//   - AUTH-KEYSTEST-C: runtime/** non-_test.go files must not import keystest
//     (production runtime code must use auth.GenerateRSAKeyPair() instead).
//
//   - AUTH-KEYSTEST-D: adapters/**, cmd/**, and examples/** non-_test.go files
//     must not import keystest. examples/ is intentionally NOT exempt: the
//     correct production path for ephemeral keys in examples is
//     auth.GenerateRSAKeyPair() (error-first) as established by
//     cmd/corebundle/secrets.go and examples/ssobff/app.go. Importing keystest
//     in an examples production file is the exact mistake this rule prevents.
//
// Exemptions:
//   - The keystest package's own source files (runtime/auth/keystest/).
//   - Any _test.go file anywhere in the module (_test.go files may import
//     keystest for test fixture construction).
//
// AI-robust grade: Medium — import-path string matching via go/parser. The
// Hard primary defense is the physical isolation of Must* key helpers in
// runtime/auth/keystest/: production packages that do not import the package
// cannot access MustGenerateKeyPair. This archtest is the secondary defense
// preventing non-test files from importing keystest.
//
// Blind-spot inventory:
//
//   - dot-import (`import . "runtime/auth/keystest"`): go/parser still records
//     the import path in f.Imports, so parseImports captures it. No blind spot.
//
//   - blank import (`import _ "runtime/auth/keystest"`): same — path still
//     recorded in f.Imports. No blind spot.
//
//   - Transitive import (a.go → b.go → keystest): this rule only checks direct
//     imports, not transitive closure. A transitive import would require adding
//     a new production package that imports keystest, which would itself violate
//     this rule. No practical blind spot.
//
//   - Build-tag-only files: go/parser falls back to rawScanImports (line-scan
//     fallback) when full parse fails. Import paths are still captured.
//
// runtime/auth/keystest is a GoCell platform symbol, so its import path is
// derived from PlatformModulePath (ARCHTEST-MODULE-PATH-FUNNEL-01: no bare
// literal); the scan scope is the running module (collectGoFiles).
//
// Not registered in StandardCellRules: the rule constrains GoCell's own
// runtime/auth/keystest layout with no ConfigForExternalCell
// consumer-extension → vacuous-green in an external module; kept importable &
// module-path-agnostic.
//
// ref: ADR `docs/architecture/202605171800-adr-kernel-mustctor-removal.md`
// ref: AUTH-AUTHTEST-BOUNDARY-01 in auth_authtest_boundary.go

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// CheckAuthKeystestBoundary runs AUTH-KEYSTEST-IMPORT-BOUNDARY-01 (sub-rules
// A/B/C/D) over the running module and returns its diagnostics. It is the
// importable rule body; GoCell's TestAuthKeystestBoundary calls it directly —
// single source, no parallel rule body.
func CheckAuthKeystestBoundary(t *testing.T, _ ConfigForExternalCell) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	keystestImport := PlatformFrameworkModulePath + "/runtime/auth/keystest"

	allGoFiles, err := collectGoFiles(root)
	if err != nil {
		t.Fatalf("failed to collect .go files: %v", err)
	}
	if len(allGoFiles) == 0 {
		t.Fatal("no .go files found — module root may be wrong")
	}

	keystestPkgDir := filepath.Join(root, "framework", "runtime", "auth", "keystest")

	var diags []Diagnostic
	for _, f := range allGoFiles {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		sub, suffix, ok := keystestRuleFor(rel, filepath.Dir(f), strings.HasSuffix(f, "_test.go"), keystestPkgDir)
		if !ok {
			continue
		}
		if !fileImportsKeystest(t, f, keystestImport) {
			continue
		}
		diags = append(diags, Diagnostic{
			Rel:     rel,
			Line:    importLine(f, keystestImport),
			Message: fmt.Sprintf("%s: %s imports %s — %s", sub, rel, keystestImport, suffix),
		})
	}
	return diags
}

// keystestRuleFor classifies a file (by its module-relative path, directory,
// test-file status, and the keystest package dir) into the AUTH-KEYSTEST sub-rule
// that governs it, returning the sub-rule code, the remediation suffix, and ok=false
// when no boundary applies (file is out of scope or an exempt _test.go / keystest
// own file). Rule A covers any kernel/ file (incl _test.go); B/C/D cover non-test
// files only; C additionally exempts the keystest package's own implementation.
func keystestRuleFor(rel, dir string, isTest bool, keystestPkgDir string) (sub, suffix string, ok bool) {
	switch {
	case strings.HasPrefix(rel, "framework/kernel/"):
		return "AUTH-KEYSTEST-A", "kernel must not depend on runtime/", true
	case strings.HasPrefix(rel, PlatformCellsDir+"/"):
		if isTest {
			return "", "", false
		}
		return "AUTH-KEYSTEST-B",
			"cells production code must not import test-only key helpers; use auth.GenerateRSAKeyPair()", true
	case strings.HasPrefix(rel, "framework/runtime/"):
		if isTest || dir == keystestPkgDir {
			return "", "", false
		}
		return "AUTH-KEYSTEST-C",
			"use auth.GenerateRSAKeyPair() for production ephemeral key generation", true
	case strings.HasPrefix(rel, "adapters/"),
		strings.HasPrefix(rel, "cmd/"),
		strings.HasPrefix(rel, "examples/"):
		if isTest {
			return "", "", false
		}
		return "AUTH-KEYSTEST-D",
			"use auth.GenerateRSAKeyPair() for ephemeral key generation in production and demo code", true
	}
	return "", "", false
}

// fileImportsKeystest reports whether the file at path directly imports
// keystestImport (parseImports captures dot- and blank-imports too; a parse
// failure falls back to a line-scan inside parseImports).
func fileImportsKeystest(t *testing.T, path, keystestImport string) bool {
	t.Helper()
	imports, err := parseImports(path)
	if err != nil {
		t.Fatalf("failed to parse %s: %v", path, err)
	}
	for _, imp := range imports {
		if imp == keystestImport {
			return true
		}
	}
	return false
}
