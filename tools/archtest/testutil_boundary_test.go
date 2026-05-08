// INVARIANT: TESTUTIL-BOUNDARY-01: any package whose import path contains a
// "testutil" segment may only be imported by *_test.go files or by other
// test-infrastructure packages (segment name ending in "test", paths under
// tests/, or sibling testutil files).
package archtest

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLayerTestutil enforces LAYER-TESTUTIL:
//
// No production Go file (i.e. NOT *_test.go and NOT in a test-infrastructure
// directory) may import any package whose import path contains a "testutil"
// segment.
//
// Rationale: testutil packages are shared test fixtures that often pull in
// the `testing` stdlib package and t-bound helpers; importing them from
// production code would smuggle test infrastructure into a release binary
// and signals a layering mistake. Examples currently in tree:
//
//   - cells/accesscore/internal/testutil  (per-cell fixtures, t-bound)
//   - cells/configcore/internal/testutil  (per-cell fixtures)
//   - pkg/testutil/fileutil               (cross-cutting file I/O helpers, t-bound)
//   - pkg/testutil/sloghelper             (cross-cutting log parsing helpers)
//   - pkg/testutil/testtime               (cross-cutting timeout constants)
//   - tests/testutil                      (e2e helpers, t-bound)
//
// Cross-package conformance suites (e.g. kernel/outbox/outboxtest,
// runtime/distlock/locktest) are themselves test infrastructure: they cannot
// be *_test.go files because Go forbids cross-package _test.go imports. The
// rule treats any file whose path contains a "testutil" segment, a segment
// ending in "test" (outboxtest / locktest / healthtest / archtest / etc.),
// or sits under tests/ as test infrastructure and lets it import testutil
// packages. Production code (cmd/, examples/, cells/, runtime/, adapters/,
// kernel/, pkg/ excluding pkg/testutil) gets the boundary enforced.
//
// The rule is discovery-based: any new directory that matches the
// test-infrastructure pattern is automatically covered without further edits.
func TestLayerTestutil(t *testing.T) {
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)

	allGoFiles, err := collectGoFiles(root)
	require.NoError(t, err, "failed to collect .go files")
	require.NotEmpty(t, allGoFiles, "no .go files found — module root may be wrong")

	testutilRoots := discoverTestutilRoots(t, root, allGoFiles)
	require.NotEmpty(t, testutilRoots,
		"expected to discover at least one testutil tree (pkg/testutil/* or cells/*/internal/testutil)")

	var violations []string
	for _, f := range allGoFiles {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		rel, err := filepath.Rel(root, f)
		require.NoError(t, err)
		rel = filepath.ToSlash(rel)
		if isTestInfraPath(rel) {
			continue
		}

		imports, err := parseImports(f)
		require.NoError(t, err, "failed to parse %s", f)
		for _, imp := range imports {
			if !strings.HasPrefix(imp, modPath+"/") {
				continue
			}
			impRel := strings.TrimPrefix(imp, modPath+"/")
			if extractTestutilRoot(impRel) == "" {
				continue
			}
			violations = append(violations,
				fmt.Sprintf("LAYER-TESTUTIL: %s (production file) imports %s (only *_test.go or test-infra packages may)", rel, imp))
		}
	}

	if len(violations) > 0 {
		for _, v := range violations {
			t.Logf("%s", v)
		}
	}
	assert.Empty(t, violations,
		"production (non-_test.go, non-test-infra) files must not import testutil packages; "+
			"testutil packages are test-fixture code — import them only from *_test.go or test-infrastructure files")
}

// extractTestutilRoot returns the directory prefix up to and including the
// first "testutil" segment, or "" if pkgRel has no "testutil" segment.
//
// Examples:
//
//	cells/accesscore/internal/testutil          → cells/accesscore/internal/testutil
//	cells/accesscore/internal/testutil/sub      → cells/accesscore/internal/testutil
//	pkg/testutil/fileutil                       → pkg/testutil
//	tests/testutil                              → tests/testutil
//	runtime/foo                                 → "" (no testutil segment)
func extractTestutilRoot(pkgRel string) string {
	parts := strings.Split(pkgRel, "/")
	for i, p := range parts {
		if p == "testutil" {
			return strings.Join(parts[:i+1], "/")
		}
	}
	return ""
}

// isTestInfraPath reports whether the given module-relative path lives in a
// directory that is itself test infrastructure: a "testutil" segment, a
// segment ending in "test" (e.g. outboxtest, locktest), or a path under
// tests/. Test-infra files are allowed to import testutil packages even
// though they are not *_test.go.
func isTestInfraPath(rel string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) > 0 && parts[0] == "tests" {
		return true
	}
	for _, p := range parts {
		if p == "testutil" {
			return true
		}
		// Segment ending in "test" (and not the literal word "test", which
		// is rare as a directory name and would be ambiguous): outboxtest,
		// locktest, healthtest, archtest, pgtest, authtest, ...
		if len(p) > 4 && strings.HasSuffix(p, "test") {
			return true
		}
	}
	return false
}

// discoverTestutilRoots returns the set of testutil-tree roots (relative to
// module root, slash-separated) that contain at least one Go file.
func discoverTestutilRoots(t *testing.T, root string, goFiles []string) map[string]struct{} {
	t.Helper()
	out := map[string]struct{}{}
	for _, f := range goFiles {
		rel, err := filepath.Rel(root, f)
		require.NoError(t, err)
		dir := filepath.ToSlash(filepath.Dir(rel))
		if r := extractTestutilRoot(dir); r != "" {
			out[r] = struct{}{}
		}
	}
	return out
}
