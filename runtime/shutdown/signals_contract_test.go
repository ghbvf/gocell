package shutdown

import (
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
	matches, err := filepath.Glob("signals_*.go")
	require.NoError(t, err)

	signalFiles := make([]string, 0, len(matches))
	for _, m := range matches {
		if !strings.HasSuffix(m, "_test.go") {
			signalFiles = append(signalFiles, m)
		}
	}

	expected := map[string]string{
		"signals_unix.go":    "unix",
		"signals_windows.go": "windows",
		"signals_other.go":   "!unix && !windows",
	}

	require.Len(t, signalFiles, len(expected),
		"expected exactly %d signals_*.go files (unix/windows/other partition); got %v",
		len(expected), signalFiles)

	fset := token.NewFileSet()
	for _, path := range signalFiles {
		base := filepath.Base(path)
		want, ok := expected[base]
		require.Truef(t, ok, "unexpected signals_*.go file: %s — keep the unix/windows/other partition stable", base)

		src, err := os.ReadFile(path)
		require.NoError(t, err)
		f, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		require.NoError(t, err)

		var found string
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if strings.HasPrefix(c.Text, "//go:build ") {
					found = strings.TrimPrefix(c.Text, "//go:build ")
					break
				}
			}
			if found != "" {
				break
			}
		}
		assert.Equalf(t, want, found,
			"%s must declare //go:build %s to remain mutually exclusive with the other signals_*.go files",
			base, want)
	}
}
