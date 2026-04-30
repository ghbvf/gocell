package shutdown

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSignalsToWatch_NonEmpty asserts the per-platform signalsToWatch
// implementation returns at least one signal. Compiles and runs on every
// platform; combined with TestSignalsToWatch_BuildTagTriple it proves the
// build-tag mutual-exclusion contract holds.
func TestSignalsToWatch_NonEmpty(t *testing.T) {
	require.NotEmpty(t, signalsToWatch(),
		"signalsToWatch must return at least one signal — every platform needs SOME shutdown signal")
}

// TestSignalsToWatch_BuildTagTriple guards the compile-time uniqueness
// invariant: exactly three signals_*.go files must cover the
// (unix / windows / !unix && !windows) partition. A typo in a build
// constraint could silently overlap two files (duplicate symbol) or
// leave a platform unowned (link error) — this test fails up-front
// when the file set or its build constraints drift.
func TestSignalsToWatch_BuildTagTriple(t *testing.T) {
	expected := map[string]string{
		"signals_unix.go":    "unix",
		"signals_windows.go": "windows",
		"signals_other.go":   "!unix && !windows",
	}

	signalFiles := listSignalsSources(t)
	require.Len(t, signalFiles, len(expected),
		"expected exactly %d signals_*.go files (unix/windows/other partition); got %v",
		len(expected), signalFiles)

	for _, path := range signalFiles {
		base := filepath.Base(path)
		want, ok := expected[base]
		require.Truef(t, ok, "unexpected signals_*.go file: %s — keep the unix/windows/other partition stable", base)

		got := readBuildConstraint(t, path)
		assert.Equalf(t, want, got,
			"%s must declare //go:build %s to remain mutually exclusive with the other signals_*.go files",
			base, want)
	}
}

// listSignalsSources returns every non-test signals_*.go file in the package
// directory. Helper extraction keeps the parent test's cognitive complexity
// below the project's lint threshold.
func listSignalsSources(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("signals_*.go")
	require.NoError(t, err)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if !strings.HasSuffix(m, "_test.go") {
			out = append(out, m)
		}
	}
	return out
}

// readBuildConstraint parses path and returns the body of its first
// `//go:build` directive (the modern, post-Go-1.17 form). Returns "" when
// no directive is present so callers can assert the empty case explicitly.
func readBuildConstraint(t *testing.T, path string) string {
	t.Helper()
	src, err := os.ReadFile(path)
	require.NoError(t, err)
	f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ParseComments)
	require.NoError(t, err)
	return findBuildDirective(f.Comments)
}

// findBuildDirective scans an AST comment slice for the first `//go:build`
// directive and returns its constraint expression. Pure function so the
// caller does not need to track state across nested loops.
func findBuildDirective(groups []*ast.CommentGroup) string {
	const prefix = "//go:build "
	for _, cg := range groups {
		for _, c := range cg.List {
			if after, ok := strings.CutPrefix(c.Text, prefix); ok {
				return after
			}
		}
	}
	return ""
}
