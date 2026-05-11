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
// legacy // +build directives, mixed/paired form, multi-line AND merging,
// and position-sensitivity (constraints below the package clause or in a
// non-leading CommentGroup must be ignored, matching Go toolchain semantics).
func TestParseBuildConstraint(t *testing.T) {
	t.Parallel()

	type evalCase struct {
		active []string
		want   bool
	}

	cases := []struct {
		name      string
		source    string
		wantNil   bool
		evals     []evalCase
		wantParse bool // expect ParseBuildConstraint to return non-nil error
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
			name:   "legacy_plus_build_paired_with_modern",
			source: "//go:build foo\n// +build foo\n\npackage p\n",
			evals: []evalCase{
				{active: []string{"foo"}, want: true},
				{active: nil, want: false},
			},
		},
		{
			name: "multi_line_modern_and_merge",
			// Two modern directives are AND-merged per Go spec (read.go).
			source: "//go:build foo\n//go:build bar\n\npackage p\n",
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
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := writeTempGoFile(t, tc.name+".go", tc.source)

			expr, err := typeseval.ParseBuildConstraint(path)
			if tc.wantParse {
				require.Error(t, err)
				return
			}
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
