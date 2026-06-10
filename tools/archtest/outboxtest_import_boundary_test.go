//go:build archtest

// INVARIANT: OUTBOXTEST-IMPORT-BOUNDARY-01: production Go files must not import
// kernel/outbox/outboxtest — that package's (*Recorder).CellEmitter() test seam
// calls outbox.WrapEmitterForCell (a composition-root-only funnel that
// CELL-RAW-INFRA-WRAPPER-LOCATION-01 allowlists recorder.go for). Without an
// import boundary, any production file could import outboxtest and obtain a
// sealed outbox.CellEmitter via the Recorder seam, bypassing the
// "only composition roots wrap raw infra into sealed markers" defense. This
// rule is complementary to TESTUTIL-BOUNDARY-01 ("testutil" segment) and
// CELLTEST-IMPORT-SCOPE-01 (cells/*/*test); neither covers kernel/outbox/outboxtest
// (CELLTEST-IMPORT-SCOPE-01's own pattern table even asserts it wantMatch:false).
package archtest

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outboxtestImportPathPattern matches the kernel/outbox/outboxtest package
// exactly (org/repo-agnostic). The trailing `$` anchors to the top-level
// package — sub-packages are intentionally NOT matched (a sub-package importer
// must traverse this package first; this mirrors CELLTEST-IMPORT-SCOPE-01's
// anchoring choice).
//
// NARROW SCOPE (deliberate): this rule guards only kernel/outbox/outboxtest —
// the precise gap opened by PR #970's new (*Recorder).CellEmitter() sealed seam.
// Generalizing to all kernel/runtime/adapters/pkg `*test` test-infra packages
// (runtime/distlock/locktest, runtime/outbox/outboxtest, kernel/persistence/
// persistencetest, ...) is tracked in gh issue #986: a broad "*test suffix"
// rule false-positives on cells/accesscore/internal/ports/conformance/
// conformance.go importing runtime/distlock/locktest (a conformance suite whose
// path has no `*test` segment, so isTestInfraPath does not exempt it). #986
// resolves that exemption first, then lands the generalized rule.
var outboxtestImportPathPattern = regexp.MustCompile(`^github\.com/[^/]+/[^/]+/kernel/outbox/outboxtest$`)

// isOutboxtestImportPath reports whether an absolute import path is exactly the
// kernel/outbox/outboxtest test-infrastructure package.
//
// AI-robust rating: Hard (with documented blind spots). The check resolves a
// Go import-path string literal; an import alias renames only the local
// identifier, never the path literal, so it cannot bypass this scan. The
// carrier mirrors the two sibling boundary archtests (reuses isTestInfraPath)
// rather than .golangci.yml depguard — the test-infra importer exemption is
// the non-trivial isTestInfraPath segment logic, already single-sourced and
// reused by TESTUTIL-BOUNDARY-01 / CELLTEST-IMPORT-SCOPE-01; a depguard glob
// carve-out for that exemption would be more fragile.
//
// Blind spots (non-coverage outside declared rule scope):
//   - Import aliases that rename the local identifier: the matched literal is
//     the import path, not the alias, so aliasing does not evade the match.
//   - Build-tag-gated imports (//go:build ignore) that never compile: not
//     production imports in practice; they never reach a release binary.
//   - Sub-packages (kernel/outbox/outboxtest/sub): anchored out by `$`; an
//     importer must traverse this matched root package first.
func isOutboxtestImportPath(importPath string) bool {
	return outboxtestImportPathPattern.MatchString(importPath)
}

// TestOutboxtestImportBoundary enforces OUTBOXTEST-IMPORT-BOUNDARY-01:
//
// No production Go file (i.e. NOT *_test.go and NOT in a test-infrastructure
// directory) may import kernel/outbox/outboxtest. The package is test
// infrastructure (it cannot be a *_test.go file because Go forbids cross-package
// _test.go imports), and its (*Recorder).CellEmitter() seam is a sanctioned
// WrapEmitterForCell call site; importing it from production code would re-open
// the sealed-marker bypass that CELL-RAW-INFRA-WRAPPER-LOCATION-01 closes.
//
// The check is discovery-based: it scans every production .go file, so no
// per-file edit is needed when new production code is added. Test-infra
// importers (configcoretest / accesscoretest / persistencetest /
// runtime/outbox/outboxtest, all matched by isTestInfraPath) remain exempt.
func TestOutboxtestImportBoundary(t *testing.T) {
	root := findModuleRoot(t)
	modPath := readModulePath(t, root)

	allGoFiles, err := collectGoFiles(root)
	require.NoError(t, err, "failed to collect .go files")
	require.NotEmpty(t, allGoFiles, "no .go files found — module root may be wrong")

	var violations []string
	for _, f := range allGoFiles {
		// Skip _test.go — they are permitted to import test-infra packages.
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		rel, err := filepath.Rel(root, f)
		require.NoError(t, err)
		rel = filepath.ToSlash(rel)
		// Skip test-infrastructure paths (outboxtest/, configcoretest/,
		// persistencetest/, etc.) — they may import sibling test packages.
		if isTestInfraPath(rel) {
			continue
		}

		imports, err := parseImports(f)
		require.NoError(t, err, "failed to parse %s", f)
		for _, imp := range imports {
			if !strings.HasPrefix(imp, modPath+"/") {
				continue
			}
			if isOutboxtestImportPath(imp) {
				violations = append(violations,
					fmt.Sprintf("OUTBOXTEST-IMPORT-BOUNDARY-01: %s (production file) imports %s "+
						"(kernel/outbox/outboxtest is test infrastructure exposing the "+
						"(*Recorder).CellEmitter() WrapEmitterForCell seam; import it only from "+
						"*_test.go or test-infrastructure files)", rel, imp))
			}
		}
	}

	if len(violations) > 0 {
		for _, v := range violations {
			t.Logf("%s", v)
		}
	}
	assert.Empty(t, violations,
		"production (non-_test.go, non-test-infra) files must not import kernel/outbox/outboxtest; "+
			"its Recorder.CellEmitter() seam wraps into a sealed CellEmitter — import it only from "+
			"*_test.go or test-infrastructure files")
}

// TestOutboxtestImportPath_PatternTable is a table-driven unit test for the
// isOutboxtestImportPath helper. It doubles as the AI-robust blind-spot
// self-check: each "outside declared scope" case is explicitly asserted to
// return false, confirming the rule does NOT attempt to cover those forms.
func TestOutboxtestImportPath_PatternTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		path      string
		wantMatch bool
	}{
		// Positive: should match (violation when imported by production code).
		{
			name:      "kernel outboxtest direct",
			path:      PlatformModulePath + "/kernel/outbox/outboxtest",
			wantMatch: true,
		},
		{
			name:      "org-agnostic kernel outboxtest",
			path:      "github.com/acme/myrepo/kernel/outbox/outboxtest",
			wantMatch: true,
		},
		// Negative: out of this narrow rule's declared scope.
		{
			name:      "runtime outboxtest (different package; deferred to #986 broad rule)",
			path:      PlatformModulePath + "/runtime/outbox/outboxtest",
			wantMatch: false,
		},
		{
			name:      "distlock locktest (deferred to #986 broad rule)",
			path:      PlatformModulePath + "/runtime/distlock/locktest",
			wantMatch: false,
		},
		{
			name:      "kernel outbox (production package)",
			path:      PlatformModulePath + "/kernel/outbox",
			wantMatch: false,
		},
		{
			name:      "outboxtest sub-package (blind spot — anchored out)",
			path:      PlatformModulePath + "/kernel/outbox/outboxtest/sub",
			wantMatch: false,
		},
		{
			name:      "cells celltest (covered by CELLTEST-IMPORT-SCOPE-01)",
			path:      PlatformModulePath + "/cells/configcore/configcoretest",
			wantMatch: false,
		},
		{
			name:      "std library",
			path:      "context",
			wantMatch: false,
		},
		{
			name:      "third party",
			path:      "github.com/stretchr/testify/assert",
			wantMatch: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isOutboxtestImportPath(tc.path)
			assert.Equal(t, tc.wantMatch, got, "isOutboxtestImportPath(%q)", tc.path)
		})
	}
}
