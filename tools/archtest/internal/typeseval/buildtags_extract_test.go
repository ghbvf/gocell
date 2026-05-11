// INVARIANT: TYPESEVAL-BUILDTAGS-COMMENTGROUP-COVERAGE-01
//   - INVARIANT: TYPESEVAL-BUILDTAGS-LEGACY-DIRECTIVE-01

package typeseval_test

import (
	"go/build/constraint"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// TestParseBuildConstraint covers the AST-aware build-tag extractor that
// supersedes the prior bufio.Scanner line-prefix path. Cases enumerate the
// directive shapes that the typeseval coverage self-check and downstream
// consumer rules must observe: modern //go:build (with AND / OR / NOT),
// legacy // +build directives, mixed/paired form, multi-line // +build
// AND-merging, and position-sensitivity (constraints below the package clause
// or in a non-leading CommentGroup must be ignored, matching Go toolchain
// semantics).
//
// Directive precedence matches cmd/go (go/build/build.go parseFileHeader):
// when //go:build is present it is authoritative; // +build lines are ignored.
// Multiple //go:build lines are rejected (see TestParseBuildConstraint_MultipleGoBuildDirectives).
func TestParseBuildConstraint(t *testing.T) {
	t.Parallel()

	type evalCase struct {
		active []string
		want   bool
	}

	cases := []struct {
		name    string
		source  string
		wantNil bool
		evals   []evalCase
	}{
		{
			name:   "modern_single_tag",
			source: "//go:build foo\n\npackage p\n",
			evals: []evalCase{
				{active: []string{"foo"}, want: true},
				{active: nil, want: false},
			},
		},
		{
			name:   "modern_or",
			source: "//go:build foo || bar\n\npackage p\n",
			evals: []evalCase{
				{active: []string{"foo"}, want: true},
				{active: []string{"bar"}, want: true},
				{active: nil, want: false},
			},
		},
		{
			name:   "modern_and_not",
			source: "//go:build foo && !bar\n\npackage p\n",
			evals: []evalCase{
				{active: []string{"foo"}, want: true},
				{active: []string{"foo", "bar"}, want: false},
				{active: []string{"bar"}, want: false},
			},
		},
		{
			name:   "legacy_plus_build",
			source: "// +build foo\n\npackage p\n",
			evals: []evalCase{
				{active: []string{"foo"}, want: true},
				{active: nil, want: false},
			},
		},
		{
			name: "legacy_plus_build_paired_with_modern",
			// The modern directive is authoritative per cmd/go semantics (parseFileHeader);
			// the legacy line is retained only for old-toolchain compat and is ignored.
			source: "//go:build foo\n// +build foo\n\npackage p\n",
			evals: []evalCase{
				{active: []string{"foo"}, want: true},
				{active: nil, want: false},
			},
		},
		{
			name: "multi_line_legacy_plus_build_and_merge",
			// Two // +build lines (no //go:build) are AND-merged per
			// go/build/constraint package doc.
			source: "// +build foo\n// +build bar\n\npackage p\n",
			evals: []evalCase{
				{active: []string{"foo"}, want: false},
				{active: []string{"bar"}, want: false},
				{active: []string{"foo", "bar"}, want: true},
			},
		},
		{
			name:    "constraint_after_package_ignored",
			source:  "package p\n\n//go:build foo\n",
			wantNil: true,
		},
		{
			name: "constraint_in_second_commentgroup_extracted",
			// Per Go spec, build constraints may appear in any pre-package
			// CommentGroup preceded only by blank lines and other line
			// comments. The second CommentGroup here qualifies, so the
			// //go:build foo directive must be honored (matches cmd/go's
			// go/build/read.go behavior).
			source: "// header doc only\n\n//go:build foo\n\npackage p\n",
			evals: []evalCase{
				{active: []string{"foo"}, want: true},
				{active: nil, want: false},
			},
		},
		{
			name:    "no_constraint",
			source:  "// doc only\n\npackage p\n",
			wantNil: true,
		},
		{
			name:    "no_comments_at_all",
			source:  "package p\n",
			wantNil: true,
		},
		{
			name: "legacy_plus_build_unknown_tag_regression",
			// LEGACY-DIRECTIVE-01 explicit regression: legacy directives must
			// now surface tags so the coverage self-check would catch any
			// pre-1.17 file referencing a tag not in KnownNonDefaultTags.
			source: "// +build legacy_xyz\n\npackage p\n",
			evals: []evalCase{
				{active: []string{"legacy_xyz"}, want: true},
				{active: nil, want: false},
			},
		},
		{
			// P2 regression: valid //go:build with malformed // +build → return
			// go:build expr, no error. cmd/go's shouldBuild only scans // +build
			// when goBuild == nil, so malformed +build lines are irrelevant here.
			name:   "valid_gobuild_plus_malformed_plusbuild_ignored",
			source: "//go:build foo\n// +build !!!invalid syntax!!!\n\npackage p\n",
			evals:  []evalCase{{active: []string{"foo"}, want: true}},
		},
		{
			// P2 regression: +build without blank line before package clause →
			// cmd/go does not recognize it; must be ignored (return nil constraint).
			name:    "legacy_plus_build_no_blank_line_ignored",
			source:  "// +build foo\npackage p\n",
			wantNil: true,
		},
		{
			// P2 regression: +build in last CG with blank line before package → honored.
			name:   "legacy_plus_build_blank_line_before_package_honored",
			source: "// +build foo\n\npackage p\n",
			evals:  []evalCase{{active: []string{"foo"}, want: true}},
		},
		{
			// P2 regression: +build in non-last CG that has a blank-line gap to
			// the next CG (AST splits CGs at blank lines, so the gap exists by
			// construction whenever there are two distinct CGs). Must be honored.
			name:   "legacy_plus_build_in_non_last_cg_honored",
			source: "// +build foo\n\n// doc\n\npackage p\n",
			evals:  []evalCase{{active: []string{"foo"}, want: true}},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := writeTempGoFile(t, tc.name+".go", tc.source)

			expr, err := typeseval.ParseBuildConstraint(path)
			require.NoError(t, err)

			if tc.wantNil {
				require.Nil(t, expr, "expected no constraint to be discovered")
				return
			}
			require.NotNil(t, expr, "expected a constraint expr")
			for _, ev := range tc.evals {
				got := expr.Eval(makeTagPredicate(ev.active))
				require.Equalf(t, ev.want, got, "tags=%v", ev.active)
			}
		})
	}
}

// TestParseBuildConstraint_MultipleGoBuildDirectives asserts that a file
// containing two //go:build lines is rejected, matching cmd/go's
// errMultipleGoBuild semantics (go/build/build.go:1660). This is the
// regression test that locks in the F2 directive-semantics fix.
func TestParseBuildConstraint_MultipleGoBuildDirectives(t *testing.T) {
	t.Parallel()
	// Two //go:build lines in the same CommentGroup — cmd/go rejects this.
	path := writeTempGoFile(t, "multi_gobuild.go", "//go:build foo\n//go:build bar\n\npackage p\n")

	_, err := typeseval.ParseBuildConstraint(path)
	require.Error(t, err, "multiple //go:build directives must be rejected (errMultipleGoBuild)")
	require.Contains(t, err.Error(), "multiple //go:build directives")
}

// TestParseBuildConstraint_MalformedDirectiveFailsClosed asserts that a
// syntactically invalid //go:build line surfaces an error, preventing the
// caller from silently skipping a file whose constraint cannot be evaluated.
func TestParseBuildConstraint_MalformedDirectiveFailsClosed(t *testing.T) {
	t.Parallel()
	path := writeTempGoFile(t, "malformed.go", "//go:build foo &&\n\npackage p\n")

	_, err := typeseval.ParseBuildConstraint(path)
	require.Error(t, err, "malformed directive must fail-closed")
}

// TestParseBuildConstraint_MissingFile surfaces IO errors so callers cannot
// confuse "file missing" with "no constraint".
func TestParseBuildConstraint_MissingFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	missing := filepath.Join(dir, "does_not_exist.go")

	_, err := typeseval.ParseBuildConstraint(missing)
	require.Error(t, err)
}

func writeTempGoFile(t *testing.T, name, src string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))
	return path
}

func makeTagPredicate(active []string) func(tag string) bool {
	set := make(map[string]bool, len(active))
	for _, t := range active {
		set[t] = true
	}
	return func(tag string) bool { return set[tag] }
}

// ensure import alias is exercised even if all Eval cases are empty.
var _ = constraint.Parse
