//go:build archtest

// INVARIANT: ARCHTEST-LEAF-BUILD-TAG-01: every tools/archtest/*_test.go (leaf, not internal/) must carry //go:build archtest
//
// archtest is the repo's heaviest test suite (~1200 Test* funcs, ~5min in a
// single process). It is single-owned by hack/verify-archtest.sh (local
// `make verify`, K=1) + .github/workflows/archtest-nightly.yml (CI 24-shard) per
// ADR 202605120000, both opting in via `-tags=archtest`. The build tag keeps the
// suite OFF the bare `go test ./...` path: hack/verify-workspace-test.sh, the
// _build-lint.yml build-test tools shard, and any future workspace-module
// traversal compile the leaf as "no test files" and CANNOT re-add it to the
// make verify / PR critical path.
//
// Why this guard exists: #1803 split tools/ into its own workspace module, so
// hack/verify-workspace-test.sh's generic per-module `go test ./...` re-ran the
// archtest leaf (+~4min on every governance lane, archtest run twice locally),
// defeating governance.yml's VERIFY_SKIP=archtest. The tag closes that leak at
// the Go toolchain level (downstream Hard: compile-time exclusion). A new leaf
// *_test.go that forgets the tag silently leaks back onto that path — a perf
// regression that is GREEN (extra tests, not a failure) and so invisible without
// this guard. This test is the upstream completeness half of the funnel
// (Medium): a typed //go:build scan asserting every leaf file is exclusively
// archtest-gated. It is wired into the PR-time subset
// (hack/verify-archtest-invariants.sh) so a forgotten tag fails at merge, not
// only nightly.
//
// Default-safe convention shared with *_integration_test.go
// (BUILD-CONSTRAINT-INTEGRATION-TAG-01); reuses the same fileHasExclusivelyTag
// predicate. internal/ subpackages (typeseval / scanner / fixtures) stay
// UNtagged on purpose — lightweight libs exercised by test-race.yml and generic
// traversals, not the heavy suite (the scanner framework excludes
// tools/archtest/internal from this DirsScope automatically).
//
// AI-robust: Medium (typed build-constraint scan + synthetic red fixture below).
package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// archtestLeafDir is the module-relative leaf package whose *_test.go files
// carry the heavy archtest suite. Subdirectories (internal/) are out of scope.
const archtestLeafDir = "tools/archtest"

// fileExclusivelyArchtestGated reports whether path's //go:build constraint makes
// the "archtest" tag NECESSARY (the file is excluded from any build that lacks
// the tag) AND satisfiable (the file still compiles under the owner's
// `-tags=archtest`). A missing or unparseable constraint is not exclusively
// gated (fail-closed) — such a file would leak into a bare `go test ./...`.
//
// Why not reuse fileHasExclusivelyTag (BUILD-CONSTRAINT-INTEGRATION-TAG-01):
// that helper routes through typeseval.BuildContextPredicate, which treats every
// GOOS as satisfiable (union-over-GOOS) so it can reason about *_integration_test
// files that carry a plain `//go:build integration`. Two archtest leaf files
// legitimately combine the tag with a GOOS guard (`archtest && !windows`); under
// union-over-GOOS that helper would mis-flag them. This is a pure boolean check
// over the constraint expression — independent of host GOOS — which is the
// correct lens for "does archtest gate this file off the bare-`go test` path".
//
// Necessity is checked by pinning archtest=false and confirming the expression is
// false at both extremes of the remaining tags (all-true and all-false). For the
// constraint forms archtest leaf files use (`archtest`, `archtest && !GOOS`) this
// is exact; it correctly rejects `!windows` (leaks on non-windows),
// `archtest || windows` (leaks on windows), a wrong tag, and no constraint.
func fileExclusivelyArchtestGated(path string) (bool, error) {
	expr, err := ParseBuildConstraint(path)
	if err != nil {
		return false, err
	}
	if expr == nil {
		return false, nil
	}
	const tag = "archtest"
	archtestFalseOthersTrue := func(t string) bool { return t != tag } // archtest=0, others=1
	allFalse := func(string) bool { return false }                     // archtest=0, others=0
	onlyArchtest := func(t string) bool { return t == tag }            // archtest=1, others=0
	// Necessary: with archtest absent the file must not compile under either
	// extreme of the other tags (so it never enters a no-archtest build).
	necessary := !expr.Eval(archtestFalseOthersTrue) && !expr.Eval(allFalse)
	// Live: onlyArchtest sets archtest=true and all other tags=false (GOOS=false
	// → !GOOS=true). This accepts the two forms leaf files use — `archtest` (all
	// others false → expr=true) and `archtest && !GOOS` (GOOS=false → !GOOS=true
	// → expr=true) — while rejecting a dead gate like `archtest && !archtest`
	// (would be false). No current leaf uses `archtest && GOOS` (GOOS=false →
	// expr=false); adding one would require updating this predicate.
	live := expr.Eval(onlyArchtest)
	return necessary && live, nil
}

// findArchtestLeafTagViolations returns the module-relative paths of every
// tools/archtest/*_test.go (leaf only) whose //go:build constraint is NOT
// exclusively gated on the "archtest" tag — i.e. files that would still compile
// under a bare `go test ./...` and thus leak the heavy suite back onto the
// critical path. Missing or unparseable constraints count as violations
// (fail-closed). The returned considered count powers the anti-vacuity check.
//
// Per-file decision is fileExclusivelyArchtestGated (a GOOS-independent boolean
// necessity check; see its doc for why fileHasExclusivelyTag is the wrong lens
// for the `archtest && !GOOS` compounds). The scanner framework
// (DirsScope/Scope.Files) auto-excludes tools/archtest/internal, leaving only the
// leaf package.
func findArchtestLeafTagViolations(rootDir string) (violations []string, considered int, err error) {
	scope := DirsScope(rootDir, []string{archtestLeafDir}, IncludeTests())
	files, filesErr := scope.Files()
	if filesErr != nil {
		return nil, 0, filesErr
	}

	for _, path := range files {
		rel, relErr := filepath.Rel(rootDir, path)
		if relErr != nil {
			rel = path
		}
		relSlash := filepath.ToSlash(rel)
		// Leaf only: a file directly in tools/archtest/, not a subpackage.
		if filepath.ToSlash(filepath.Dir(relSlash)) != archtestLeafDir {
			continue
		}
		if !strings.HasSuffix(relSlash, "_test.go") {
			continue
		}
		considered++
		ok, checkErr := fileExclusivelyArchtestGated(path)
		if checkErr != nil || !ok {
			violations = append(violations, relSlash)
		}
	}
	return violations, considered, nil
}

// TestArchtest_AllLeafTestFiles_HaveArchtestBuildTag asserts every leaf archtest
// test file carries `//go:build archtest`. Wired into the PR-time subset
// (hack/verify-archtest-invariants.sh) so a forgotten tag fails at merge — the
// leak it prevents is otherwise a silent (green) perf regression on the make
// verify critical path. Cheap: a header-only //go:build parse per file, no
// packages.Load.
func TestArchtest_AllLeafTestFiles_HaveArchtestBuildTag(t *testing.T) {
	root := findModuleRoot(t)

	violations, considered, err := findArchtestLeafTagViolations(root)
	require.NoError(t, err, "error walking tools/archtest leaf")

	// Anti-vacuity: the PR that introduced this guard tagged ~288 leaf *_test.go
	// files. 200 is a conservative floor: a refactor that halves the suite still
	// passes, while a misconfigured scan (0 or a handful of files) fails loud.
	require.Greater(t, considered, 200,
		"anti-vacuity: expected the tools/archtest leaf to contain many *_test.go files; "+
			"scan considered only %d (rootDir=%s) — DirsScope/leaf filter likely broken",
		considered, root)

	if len(violations) > 0 {
		t.Logf("Found %d tools/archtest/*_test.go file(s) missing '//go:build archtest':", len(violations))
		for _, v := range violations {
			t.Logf("  %s", v)
		}
	}

	assert.Empty(t, violations,
		"every tools/archtest/*_test.go (leaf) must carry '//go:build archtest' so the heavy suite "+
			"stays off the bare `go test ./...` critical path; add the constraint at the top of each "+
			"listed file (single-owner: hack/verify-archtest.sh + .github/workflows/archtest-nightly.yml)")
}

// TestArchtest_LeafBuildTag_Violation_Fixture is the "test the test" synthetic
// red case for the per-file predicate. It exercises fileExclusivelyArchtestGated
// on temp files: a missing tag, a wrong tag, and a misplaced constraint are NOT
// exclusively archtest-gated (would be flagged); a bare archtest tag and a
// compound `archtest && !windows` ARE (mirrors the two leaf files that
// legitimately combine the tag with a GOOS constraint).
func TestArchtest_LeafBuildTag_Violation_Fixture(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		content       string
		wantExclusive bool
	}{
		{
			name:          "no_tag_test.go",
			content:       "package fixture\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
			wantExclusive: false,
		},
		{
			name:          "wrong_tag_test.go",
			content:       "//go:build other_tag\n\npackage fixture\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
			wantExclusive: false,
		},
		{
			name:          "good_test.go",
			content:       "//go:build archtest\n\npackage fixture\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
			wantExclusive: true,
		},
		{
			name:          "good_compound_test.go",
			content:       "//go:build archtest && !windows\n\npackage fixture\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
			wantExclusive: true,
		},
		{
			// Constraint after the package clause is invisible to the toolchain.
			name:          "misplaced_after_package_test.go",
			content:       "package fixture\n\n//go:build archtest\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
			wantExclusive: false,
		},
		{
			// OR form: `archtest || integration` leaks under `-tags=integration`
			// (integration=true → expr=true) — NOT exclusively archtest-gated.
			// archtestFalseOthersTrue makes integration=true → expr=true →
			// necessary=false.
			name:          "or_relaxed_test.go",
			content:       "//go:build archtest || integration\n\npackage fixture\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n",
			wantExclusive: false,
		},
	}

	root := t.TempDir()
	for _, c := range cases {
		path := filepath.Join(root, c.name)
		require.NoError(t, os.WriteFile(path, []byte(c.content), 0o644))
		got, err := fileExclusivelyArchtestGated(path)
		require.NoError(t, err, "fileExclusivelyArchtestGated(%q)", c.name)
		assert.Equal(t, c.wantExclusive, got,
			"fileExclusivelyArchtestGated(%q) = %v, want %v", c.name, got, c.wantExclusive)
	}
}
