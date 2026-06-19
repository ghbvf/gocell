package archtestrunner

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
