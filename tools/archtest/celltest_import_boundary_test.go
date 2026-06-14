//go:build archtest

// INVARIANT: CELLTEST-IMPORT-BOUNDARY-01
//
// # CELLTEST-IMPORT-BOUNDARY-01
//
// The kernel/cell/celltest sub-package is test-only. It provides panic-on-error
// helpers (MustAuthJWT, MustAuthJWTFromAssembly, MustAuthServiceToken) and the
// TestMux / RunRepoReadinessConformance test harness, designed exclusively for
// use in _test.go files and test binaries.
//
// This rule enforces three boundaries:
//
//   - CELLTEST-A: Non-_test.go files must not import kernel/cell/celltest
//     anywhere in the module (exempt: celltest package's own source files, and
//     any _test.go file). Production code must use auth.NewAuthJWT / auth.NewAuthJWTFromAssembly
//     / auth.NewAuthServiceToken (error-first) directly, propagating the error
//     to bootstrap — see ADR docs/architecture/202605171800-adr-kernel-mustctor-removal.md.
//
//   - CELLTEST-B: kernel/** (excluding the celltest package itself and its
//     direct parent kernel/cell/_test.go files) must not import
//     kernel/cell/celltest, including _test.go files. Reason: kernel is
//     a physical-isolation layer that must not depend on test-fixture sub-packages
//     defined inside itself — this would create a test-fixture self-dependency
//     cycle that is invisible to go build but architecturally unsound. A kernel
//     sub-package that needs test helpers should define them as unexported
//     functions in its own _test.go.
//     Exception: kernel/cell/ (the direct parent of celltest) _test.go files are
//     exempt because Go's standard model for testing a sub-package includes the
//     parent package's _test.go verifying the sub-package's panic/error behavior
//     (same pattern as net/http testing httptest helpers).
//
//   - CELLTEST-C: examples/** non-_test.go files must not import
//     kernel/cell/celltest. This mirrors AUTH-KEYSTEST-D: examples are not
//     exempt because the correct production path for constructing AuthPlan values
//     is auth.NewAuthJWT / auth.NewAuthJWTFromAssembly (error-first), as
//     established by cmd/corebundle. Importing celltest in an examples production
//     file is the exact mistake this rule prevents.
//
// # Decision log (why B is kernel-only, not cells/)
//
// cells/** _test.go files legitimately import kernel/cell/celltest to construct
// AuthPlan values (MustAuthJWT etc.) for HTTP handler tests — this is exactly the
// intended K8s httptest-style usage. Prohibiting cells/ _test.go files from
// importing celltest would break dozens of existing handler tests and offer no
// safety benefit (the helpers are _test.go-only and cannot reach production paths
// through standard build). The boundary that matters is:
//
//  1. non-_test.go files (CELLTEST-A) — no layer is exempt;
//  2. kernel/ own files including _test.go (CELLTEST-B) — layering rule;
//  3. examples/ non-_test.go (CELLTEST-C) — mirrors keystest boundary.
//
// CELLTEST-B currently covers kernel/ own files; cells/_test.go and
// examples/_test.go are out of scope for this rule.
//
// # AI-robust grade: Medium — import-path string matching via go/parser.
//
// The Hard primary defense is the physical isolation of Must* auth helpers in
// kernel/cell/celltest/: production packages that do not import the package
// cannot access MustAuthJWT*. This archtest is the secondary defense preventing
// non-test files from importing celltest.
//
// # Blind-spot inventory
//
//   - dot-import (`import . "kernel/cell/celltest"`): go/parser still records
//     the import path in f.Imports, so parseImports captures it. No blind spot.
//
//   - blank import (`import _ "kernel/cell/celltest"`): same — path still
//     recorded in f.Imports. No blind spot.
//
//   - Transitive import (a.go → b.go → celltest): this rule only checks direct
//     imports, not transitive closure. A transitive import would require a new
//     production package that directly imports celltest, which would itself
//     violate this rule. No practical blind spot.
//
//   - Build-tag-only files: go/parser falls back to rawScanImports (line-scan
//     fallback) when full parse fails. Import paths are still captured.
//
// ref: ADR `docs/architecture/202605171800-adr-kernel-mustctor-removal.md`
// ref: AUTH-KEYSTEST-IMPORT-BOUNDARY-01 in tools/archtest/auth_keystest_boundary_test.go
// ref: AUTH-AUTHTEST-BOUNDARY-01 in tools/archtest/auth_authtest_boundary_test.go
// CELLTEST-B2-CELLS-EXAMPLES-TESTFILES: extend CELLTEST-B to cells/_test.go +
//
//	examples/_test.go if test-helper import policy tightens in the future.
package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCelltestImportBoundary enforces CELLTEST-IMPORT-BOUNDARY-01 (A/B/C) via
// CheckCelltestImportBoundary (celltest_import_boundary.go). The scanner logic
// lives in the non-test file so external Cell repositories can import and run
// the rule without needing GoCell's _test.go compilation.
func TestCelltestImportBoundary(t *testing.T) {
	Report(t, "CELLTEST-IMPORT-BOUNDARY-01", CheckCelltestImportBoundary(t, ConfigForExternalCell{}))
}

// TestCelltestImportBoundary_SubRules drives the actual rule bodies
// (celltestSubA / celltestSubB / celltestSubC) over a synthetic module laid out
// under a temp root, asserting the exact set of flagged files for each sub-rule.
// Unlike the prior negative probes — which only exercised parseImports and never
// reached a rule body — a regression in any sub-rule's skip logic (the _test.go
// exemption, the celltest-package self-exemption, the kernel/cell parent
// _test.go carve-out, or the kernel/ vs examples/ prefix gating) now fails this
// test.
//
// The synthetic module path is deliberately NON-github (example.test/extmod) so
// the boundary rule's module-path-agnostic prefix handling is exercised too, and
// each flagged diagnostic's line is asserted to be the real import line (F7), not
// a hardcoded 1.
func TestCelltestImportBoundary_SubRules(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const modPath = "example.test/extmod" // NON-github → proves module-path-agnostic
	celltestImport := modPath + "/framework/kernel/cell/celltest"
	celltestPkgDir := filepath.Join(root, "framework", "kernel", "cell", "celltest")
	celltestParentDir := filepath.Join(root, "framework", "kernel", "cell")

	// write creates root/<rel> with a blank import of celltest on line 2 and
	// returns the absolute path.
	write := func(rel string) string {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(abs), 0o755))
		content := fmt.Sprintf("package p\nimport _ %q\n", celltestImport)
		require.NoError(t, os.WriteFile(abs, []byte(content), 0o644))
		return abs
	}

	files := []string{
		write("cells/foo/handler.go"),                       // A RED (non-test, outside celltest dir)
		write("cells/foo/handler_test.go"),                  // A GREEN (_test.go)
		write("framework/kernel/sub/thing_test.go"),         // B RED (kernel _test.go)
		write("framework/kernel/cell/celltest/celltest.go"), // A & B GREEN (celltest's own source)
		write("framework/kernel/cell/cell_test.go"),         // B GREEN (kernel/cell parent _test.go carve-out)
		write("examples/app/app.go"),                        // A RED + C RED (examples non-test)
		write("examples/app/app_test.go"),                   // C GREEN (_test.go)
	}

	// relsOf returns the sorted module-relative Rel set of diags, asserting each
	// diagnostic points at the real import line (2), never a hardcoded 1.
	relsOf := func(t *testing.T, diags []Diagnostic) []string {
		t.Helper()
		var rels []string
		for _, d := range diags {
			assert.Equal(t, 2, d.Line, "diagnostic %s must point at the real import line (2), not 1", d.Rel)
			rels = append(rels, d.Rel)
		}
		sort.Strings(rels)
		return rels
	}

	diagsA := celltestSubA(t, root, files, celltestPkgDir, celltestImport)
	assert.Equal(t, []string{"cells/foo/handler.go", "examples/app/app.go"}, relsOf(t, diagsA),
		"CELLTEST-A flags every non-_test.go file (outside celltest's own dir) importing celltest")

	diagsB := celltestSubB(t, root, files, celltestPkgDir, celltestParentDir, celltestImport)
	assert.Equal(t, []string{"framework/kernel/sub/thing_test.go"}, relsOf(t, diagsB),
		"CELLTEST-B flags kernel/ files (incl _test.go) except celltest's own dir and the kernel/cell parent _test.go")

	diagsC := celltestSubC(t, root, files, celltestImport)
	assert.Equal(t, []string{"examples/app/app.go"}, relsOf(t, diagsC),
		"CELLTEST-C flags examples/ non-_test.go files importing celltest")
}
