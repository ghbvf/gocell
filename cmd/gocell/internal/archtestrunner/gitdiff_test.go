package archtestrunner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFilterArchtestFiles verifies that filterArchtestFiles keeps only
// top-level tools/archtest/*_test.go paths and rejects subdirs, non-test
// files, and other paths.
func TestFilterArchtestFiles(t *testing.T) {
	cases := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name: "keeps top-level archtest test files",
			input: []string{
				"tools/archtest/layer_test.go",
				"tools/archtest/auth_test.go",
			},
			expected: []string{
				"tools/archtest/layer_test.go",
				"tools/archtest/auth_test.go",
			},
		},
		{
			name: "excludes subdir archtest files",
			input: []string{
				"tools/archtest/internal/foo_test.go",
				"tools/archtest/layer_test.go",
			},
			expected: []string{
				"tools/archtest/layer_test.go",
			},
		},
		{
			name: "excludes non-test go files",
			input: []string{
				"tools/archtest/helpers.go",
				"tools/archtest/layer_test.go",
			},
			expected: []string{
				"tools/archtest/layer_test.go",
			},
		},
		{
			name: "excludes files outside archtest",
			input: []string{
				"kernel/cell/cell.go",
				"tools/archtest/layer_test.go",
				"tools/depgraph/graph_test.go",
			},
			expected: []string{
				"tools/archtest/layer_test.go",
			},
		},
		{
			name:     "empty input returns empty",
			input:    []string{},
			expected: nil,
		},
		{
			name: "all unrelated paths",
			input: []string{
				"kernel/cell/cell.go",
				"runtime/auth/auth.go",
			},
			expected: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := filterArchtestFiles(tc.input)
			if tc.expected == nil {
				assert.Empty(t, got)
			} else {
				assert.Equal(t, tc.expected, got)
			}
		})
	}
}

// TestChangedFilesToTests verifies that AST scanning maps changed archtest
// files to their declared Test* functions.
func TestChangedFilesToTests(t *testing.T) {
	// Create a real temp dir with archtest-like test files.
	root := makeFakeArchtestDir(t, map[string]string{
		"foo_test.go": `//go:build archtest

// INVARIANT: FOO-01

package archtest

import "testing"

func TestFooA(t *testing.T) {}
func TestFooB(t *testing.T) {}
func helperFoo(t *testing.T) {}
`,
		"bar_test.go": `//go:build archtest

// INVARIANT: BAR-01

package archtest

import "testing"

func TestBarX(t *testing.T) {}
`,
	})

	// Simulate changed files: only foo_test.go changed.
	changedFiles := []string{"tools/archtest/foo_test.go"}
	tests, err := changedFilesToTests(root, changedFiles)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"TestFooA", "TestFooB"}, tests)
}

// TestChangedFilesToTests_NoArchtestFiles returns empty without error when no
// archtest files are among the changed files.
func TestChangedFilesToTests_NoArchtestFiles(t *testing.T) {
	root := t.TempDir()
	changedFiles := []string{"kernel/cell/cell.go", "runtime/auth/auth.go"}
	tests, err := changedFilesToTests(root, changedFiles)
	require.NoError(t, err)
	assert.Empty(t, tests)
}

// TestFilterArchtestFiles_PathSeparator verifies that filepath.ToSlash
// normalization does not break filtering on OS-specific path separators.
func TestFilterArchtestFiles_PathSeparator(t *testing.T) {
	// Use filepath.Join to simulate OS paths.
	input := []string{
		filepath.Join("tools", "archtest", "layer_test.go"),
	}
	got := filterArchtestFiles(input)
	// On any OS, the filtering must work.
	assert.Len(t, got, 1)
}

// TestSplitLines verifies splitting and trimming of newline-separated strings.
func TestSplitLines(t *testing.T) {
	input := "lineA\nlineB\n\nlineC\n"
	got := splitLines(input)
	assert.Equal(t, []string{"lineA", "lineB", "lineC"}, got)
}

// TestSplitLines_Empty returns nil for empty string.
func TestSplitLines_Empty(t *testing.T) {
	assert.Nil(t, splitLines(""))
}

// TestDedupe verifies deduplication preserving order.
func TestDedupe(t *testing.T) {
	input := []string{"a", "b", "a", "c", "b"}
	got := dedupe(input)
	assert.Equal(t, []string{"a", "b", "c"}, got)
}

// TestDedupe_Empty returns empty for nil input.
func TestDedupe_Empty(t *testing.T) {
	assert.Empty(t, dedupe(nil))
}

// TestIsTopLevelArchtestFile exercises various path shapes.
func TestIsTopLevelArchtestFile(t *testing.T) {
	cases := []struct {
		path   string
		wantOK bool
	}{
		{"tools/archtest/layer_test.go", true},
		{"tools/archtest/auth_invariants_test.go", true},
		{"tools/archtest/internal/foo_test.go", false}, // subdir
		{"tools/archtest/helpers.go", false},           // not _test.go
		{"tools/depgraph/layer_test.go", false},        // wrong dir
		{"kernel/cell/cell.go", false},                 // unrelated
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got := isTopLevelArchtestFile(tc.path)
			assert.Equal(t, tc.wantOK, got, "isTopLevelArchtestFile(%q)", tc.path)
		})
	}
}

// TestWriteJSONOut_WritesFile verifies that writeTestJSONOut creates the file
// and writes valid JSON event lines.
func TestWriteJSONOut_WritesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "events.json")

	lines := [][]byte{
		[]byte(`{"Action":"run","Test":"TestX"}`),
		[]byte(`{"Action":"pass","Test":"TestX","Elapsed":0.1}`),
	}
	require.NoError(t, writeTestJSONOut(path, lines))

	data, err := os.ReadFile(path) //nolint:gosec // path is constructed from t.TempDir()
	require.NoError(t, err)
	content := string(data)
	assert.Contains(t, content, `"Action":"run"`)
	assert.Contains(t, content, `"Action":"pass"`)
}

// TestWriteJSONOut_InvalidPath returns an error for an unwritable path.
func TestWriteJSONOut_InvalidPath(t *testing.T) {
	// Use a path under a non-existent directory.
	path := filepath.Join(t.TempDir(), "nonexistent", "events.json")
	err := writeTestJSONOut(path, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "archtestrunner")
}

// TestChangedFilesToTests_ParseError returns error for unparseable file.
func TestChangedFilesToTests_ParseError(t *testing.T) {
	root := t.TempDir()
	// Create an archtest test file with invalid Go syntax.
	archtestDir := filepath.Join(root, "tools", "archtest")
	require.NoError(t, os.MkdirAll(archtestDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(archtestDir, "bad_test.go"),
		[]byte("package archtest\nfunc TestBad( { invalid go syntax"),
		0o644,
	))

	changedFiles := []string{"tools/archtest/bad_test.go"}
	_, err := changedFilesToTests(root, changedFiles)
	require.Error(t, err)
}
