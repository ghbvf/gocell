// INVARIANT: TYPESEVAL-BUILDTAGS-COMMENTGROUP-COVERAGE-01
//   - INVARIANT: TYPESEVAL-BUILDTAGS-SCOPE-FAILCLOSED-01
//   - INVARIANT: TYPESEVAL-BUILDTAGS-LEGACY-DIRECTIVE-01

package typeseval

import (
	"fmt"
	"go/build/constraint"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// repoSkipTagAllowlist names project-internal tags that the coverage
// self-check knows about but that are NOT propagated into LoadPackages
// (KnownNonDefaultTags) and are NOT implicit toolchain defaults. They
// gate non-source-controlled content (codegen output) or are intentional
// skip markers; archtest always scans the default variant.
//
// Tags that the Go toolchain sets implicitly (GOOS/GOARCH/cgo/unix/gc/go1.X)
// are no longer listed here — they are provided by BuildContextPredicate()
// which sources them from build.Default.ReleaseTags + hardcoded syslist,
// ensuring toolchain upgrades automatically propagate without hand-edits.
var repoSkipTagAllowlist = map[string]bool{
	// Synthetic always-excluded marker — no file in any real build set uses this.
	"never": true,
	// catalog_gen — build-mode marker for codegen output. The active variant
	// in source control is cmd/corebundle/catalog_gen_stub.go gated on
	// //go:build !catalog_gen. The generated catalog_gen.go counterpart is
	// .gitignore'd and only built in CI under -tags=catalog_gen. archtest
	// scans the source-of-truth stub, so catalog_gen must NOT be propagated
	// into KnownNonDefaultTags() (doing so causes LoadPackages to attempt
	// loading the absent generated file → undefined symbol on clean tree).
	"catalog_gen": true,
}

// isGoVersionTag matches tags like "go1.18", "go1.21", etc.
func isGoVersionTag(s string) bool {
	return strings.HasPrefix(s, "go1.")
}

// repoSkipDirs lists top-level directories that walkBuildTagFiles must NOT
// descend into. These contain fixtures, generated code, vendored deps,
// VCS metadata, or worktree alternates — none of which gate production
// behavior.
var repoSkipDirs = map[string]bool{
	"vendor":       true,
	".git":         true,
	"generated":    true,
	"testdata":     true,
	"worktrees":    true,
	"node_modules": true,
}

// TestKnownNonDefaultTagsCoverage is a fail-closed self-test: it walks every
// .go file in the repo (production and test), parses any //go:build or
// // +build directive using ParseBuildConstraint (AST-aware, both modern and
// legacy), and asserts every referenced tag is either a Go-toolchain
// platform tag (in platformTagAllowlist) or appears in
// FlatNonDefaultTags(). A new build tag introduced anywhere
// under the module without a corresponding KnownNonDefaultTags() update
// makes this test fail.
//
// Closes PR445-FU finding F2's drift risk: archtest rules that iterate
// build tag combinations (svctoken_caller_cell, test_time_literal, future
// rules) all read from the same single source and are guaranteed not to
// silently miss a newly-introduced tag.
//
// LEGACY-DIRECTIVE-01: unlike the old bufio.Scanner path, ParseBuildConstraint
// also covers // +build legacy directives via constraint.IsPlusBuild.
func TestKnownNonDefaultTagsCoverage(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	known := map[string]bool{}
	for _, tag := range FlatNonDefaultTags() {
		known[tag] = true
	}

	// Collect every tag-identifier appearing in any build directive,
	// alongside the file path (kept for diagnostic output on failure).
	type seenEntry struct {
		paths []string
	}
	seen := map[string]*seenEntry{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != root && repoSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		expr, perr := ParseBuildConstraint(path)
		if perr != nil {
			return perr
		}
		if expr == nil {
			return nil
		}
		var tags []string
		walkConstraintTags(expr, &tags)
		for _, tag := range tags {
			if _, ok := seen[tag]; !ok {
				seen[tag] = &seenEntry{}
			}
			rel, _ := filepath.Rel(root, path)
			seen[tag].paths = append(seen[tag].paths, rel)
		}
		return nil
	})
	require.NoError(t, err, "filepath.WalkDir")

	defaultPred := BuildContextPredicate()
	var unknown []string
	for tag, entry := range seen {
		if known[tag] || defaultPred(tag) || repoSkipTagAllowlist[tag] || isGoVersionTag(tag) {
			continue
		}
		example := entry.paths[0]
		unknown = append(unknown, fmt.Sprintf("%q (first seen at %s)", tag, example))
	}
	sort.Strings(unknown)

	require.Empty(t, unknown,
		"build tags referenced in //go:build or // +build directives but missing from "+
			"KnownNonDefaultTags() / platformTagAllowlist: %v.\n"+
			"Add the new tag combination to KnownNonDefaultTags() so archtest "+
			"rules that iterate tag-sets (svctoken_caller_cell, test_time_literal, "+
			"etc.) load the gated files instead of silently skipping them.",
		unknown)
}

// buildEvalPredicate constructs the predicate used in the dual-eval logic for
// TestKnownNonDefaultTagsFlatLoadCoverage and
// TestFlatLoadCoverage_DetectsAmbiguousConstraint.
//
// The predicate includes:
//   - the supplied project tags (non-default tags from KnownNonDefaultTags slices)
//   - all implicit toolchain defaults via BuildContextPredicate (GOOS/GOARCH/cgo/unix/gc/go1.X)
//   - any go-version tags seen in the goVersionTags set (belt-and-suspenders for
//     go1.X versions above the current toolchain floor)
//
// This prevents platform/OS/arch and go-version tags from causing
// false-positive ambiguity: those tags are handled by the Go toolchain
// and do not represent project-specific gate interactions.
func buildEvalPredicate(projectTags map[string]bool, goVersionTags map[string]bool) func(string) bool {
	var extra []string
	for t := range projectTags {
		extra = append(extra, t)
	}
	for t := range goVersionTags {
		extra = append(extra, t)
	}
	return BuildContextPredicate(extra...)
}

// collectGoVersionTags walks the repo and collects every go1.* tag referenced
// in any build directive. These are included in predicates so that files
// gated by go-version constraints do not trigger false-positive ambiguity.
func collectGoVersionTags(root string) (map[string]bool, error) {
	out := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != root && repoSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		expr, perr := ParseBuildConstraint(path)
		if perr != nil || expr == nil {
			return perr
		}
		var tags []string
		walkConstraintTags(expr, &tags)
		for _, tag := range tags {
			if isGoVersionTag(tag) {
				out[tag] = true
			}
		}
		return nil
	})
	return out, err
}

// TestKnownNonDefaultTagsFlatLoadCoverage is the SCOPE-FAILCLOSED-01 core
// self-check. It verifies the flat-union assumption behind FlatNonDefaultTags:
// for every .go file whose build constraint is satisfied by at least one
// non-default KnownNonDefaultTags() slice, the constraint must also be
// satisfied when the full flat union (FlatNonDefaultTags) is used as the
// tag predicate.
//
// If a file has a constraint like `//go:build integration && !e2e` then:
//   - Under the flat union (which includes BOTH integration and e2e), the
//     constraint evaluates false (because !e2e is false).
//   - Under the {"integration"} slice alone, it evaluates true.
//
// This is the ambiguity FlatNonDefaultTags() cannot handle — the flat-load
// would silently skip the file. This test catches that case and forces the
// author to adjust the constraint (no allowlist exemption per charter §3
// Soft-form ban).
//
// Note: files that are satisfied under the default build context (no extra
// tags) are intentionally excluded from the ambiguity check. A constraint
// like `//go:build !catalog_gen` evaluates to true when the tag set is empty
// (i.e. default build), which means the file is always present in a plain
// `go build ./...`. Such files are NOT an archtest concern: they are visible
// to every rule that does not happen to set `catalog_gen`. The concern is
// exclusively about files that require a positive non-default tag (like
// `integration`) but are excluded by the flat union due to a simultaneous
// negated constraint (like `!e2e`).
func TestKnownNonDefaultTagsFlatLoadCoverage(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)

	// Collect go-version tags to include in eval predicates (avoids false
	// positives from go1.X constraints that are toolchain-handled).
	goVersionTags, err := collectGoVersionTags(root)
	require.NoError(t, err, "collectGoVersionTags")

	// Build the flat predicate: all project-specific tags + platform + go-version.
	flatProjectTags := map[string]bool{}
	for _, tag := range FlatNonDefaultTags() {
		flatProjectTags[tag] = true
	}
	flatPred := buildEvalPredicate(flatProjectTags, goVersionTags)

	// defaultPred: empty tag set + platform/go-version only. Used to detect
	// "default-build-always-included" files (negated-only guards like !catalog_gen).
	defaultPred := buildEvalPredicate(map[string]bool{}, goVersionTags)

	// Build per-slice predicates for NON-NIL groups only.
	var slicePredicates []func(string) bool
	for _, group := range KnownNonDefaultTags() {
		if len(group) == 0 {
			continue // skip nil/empty (default build)
		}
		groupMap := map[string]bool{}
		for _, tag := range group {
			groupMap[tag] = true
		}
		slicePredicates = append(slicePredicates, buildEvalPredicate(groupMap, goVersionTags))
	}

	var ambiguous []string

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != root && repoSkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		expr, perr := ParseBuildConstraint(path)
		if perr != nil {
			return perr
		}
		if expr == nil {
			return nil
		}

		satisfiedByFlat := expr.Eval(flatPred)
		if satisfiedByFlat {
			// Flat union covers this file — no ambiguity.
			return nil
		}

		// Files satisfied under the default build context (no extra tags) are
		// "always-included" files (e.g. //go:build !catalog_gen). They are not
		// an archtest concern: they appear in every default build and in every
		// tag-specific rule that does not happen to add the negated tag.
		if expr.Eval(defaultPred) {
			return nil
		}

		// Check whether any individual non-default KnownNonDefaultTags slice
		// would satisfy it. If so, the flat-load assumption is broken for this
		// file: a rule using that specific slice would load it, but FlatNonDefaultTags
		// would silently exclude it.
		satisfiedByAny := false
		for _, pred := range slicePredicates {
			if expr.Eval(pred) {
				satisfiedByAny = true
				break
			}
		}

		if satisfiedByAny {
			rel, _ := filepath.Rel(root, path)
			ambiguous = append(ambiguous, rel)
		}
		return nil
	})
	require.NoError(t, walkErr, "filepath.WalkDir")

	sort.Strings(ambiguous)
	require.Empty(t, ambiguous,
		"FlatNonDefaultTags single-load misses these files; adjust constraint—"+
			"no allowlist exemption per charter §3 Soft-form ban")
}

// TestFlatLoadCoverage_DetectsAmbiguousConstraint is a regression test that
// verifies the fail-closed mechanism in TestKnownNonDefaultTagsFlatLoadCoverage
// actually triggers for the exact pattern it is designed to catch:
// `//go:build integration && !e2e`.
//
// Under FlatNonDefaultTags (which includes both "integration" and "e2e"):
//   - satisfiedByFlat == false  (because !e2e is false when e2e is in the flat set)
//
// Under the {"integration"} KnownNonDefaultTags slice:
//   - satisfiedByAny == true   (integration is true, e2e is absent so !e2e is true)
//
// The file must therefore be detected as ambiguous.
func TestFlatLoadCoverage_DetectsAmbiguousConstraint(t *testing.T) {
	t.Parallel()

	// Write a temporary .go file with the ambiguous constraint pattern.
	dir := t.TempDir()
	f, err := os.CreateTemp(dir, "ambiguous_*.go")
	require.NoError(t, err)
	_, err = f.WriteString("//go:build integration && !e2e\n\npackage p\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	tmpPath := f.Name()

	expr, err := ParseBuildConstraint(tmpPath)
	require.NoError(t, err)
	require.NotNil(t, expr, "expected non-nil constraint.Expr for integration && !e2e")

	// Build predicates with an empty go-version set (no go1.* tags in temp file).
	emptyGoVersionTags := map[string]bool{}

	// flat predicate includes both integration and e2e → !e2e is false → overall false.
	flatProjectTags := map[string]bool{}
	for _, tag := range FlatNonDefaultTags() {
		flatProjectTags[tag] = true
	}
	flatPred := buildEvalPredicate(flatProjectTags, emptyGoVersionTags)
	satisfiedByFlat := expr.Eval(flatPred)

	// default predicate: empty tag set (no extra tags). Used to exclude
	// "always-included" negated-only guards like //go:build !catalog_gen.
	defaultPred := buildEvalPredicate(map[string]bool{}, emptyGoVersionTags)
	satisfiedByDefault := expr.Eval(defaultPred)

	// Per-slice check (non-nil groups only): the {"integration"} group has
	// integration=true, e2e absent → !e2e=true → overall true.
	satisfiedByAny := false
	for _, group := range KnownNonDefaultTags() {
		if len(group) == 0 {
			continue // skip nil/empty (default build) — same as main test
		}
		groupMap := map[string]bool{}
		for _, tag := range group {
			groupMap[tag] = true
		}
		pred := buildEvalPredicate(groupMap, emptyGoVersionTags)
		if expr.Eval(pred) {
			satisfiedByAny = true
			break
		}
	}

	require.False(t, satisfiedByFlat,
		"expected satisfiedByFlat==false for 'integration && !e2e' under flat union "+
			"(flat set includes both integration and e2e, so !e2e evaluates false)")
	require.False(t, satisfiedByDefault,
		"expected satisfiedByDefault==false for 'integration && !e2e' under empty tag set "+
			"(integration is absent, so the overall AND expression is false)")
	require.True(t, satisfiedByAny,
		"expected satisfiedByAny==true for 'integration && !e2e' under the "+
			"{'integration'} slice (e2e absent means !e2e evaluates true)")

	// Confirm the dual-eval logic flags the file as ambiguous.
	// The file is NOT satisfied by default (requires integration) and NOT by flat
	// (blocked by !e2e), but IS satisfied by the {"integration"} slice.
	isAmbiguous := !satisfiedByFlat && !satisfiedByDefault && satisfiedByAny
	require.True(t, isAmbiguous,
		"fail-closed mechanism must detect 'integration && !e2e' as ambiguous: "+
			"satisfiedByFlat=%v satisfiedByDefault=%v satisfiedByAny=%v",
		satisfiedByFlat, satisfiedByDefault, satisfiedByAny)
}

// repoRoot returns the module root by walking up from the test binary's
// working directory until it finds a go.mod file.
func repoRoot(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	d := cwd
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			t.Fatalf("repoRoot: go.mod not found above %s", cwd)
		}
		d = parent
	}
}

// Compile-time verification: constraint.Expr interface usage must remain
// consistent with the standard library. This blank assignment ensures
// the compiler validates the Eval call signature.
var _ = (constraint.Expr)(nil)
