//go:build unix || windows

package initialadmin

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSplitLines_CRLFEndings verifies that \r\n line endings are stripped.
func TestSplitLines_CRLFEndings(t *testing.T) {
	t.Parallel()
	input := "line1\r\nline2\r\nline3\r\n"
	got := splitLines(input)
	assert.Equal(t, []string{"line1", "line2", "line3"}, got)
}

// TestSplitLines_TrailingContentWithoutNewline verifies a line without a
// trailing \n is included in the result.
func TestSplitLines_TrailingContentWithoutNewline(t *testing.T) {
	t.Parallel()
	input := "line1\nline2"
	got := splitLines(input)
	assert.Equal(t, []string{"line1", "line2"}, got)
}

// TestSplitLines_EmptyLinesSkipped verifies that empty lines (consecutive \n)
// are not included in the result.
func TestSplitLines_EmptyLinesSkipped(t *testing.T) {
	t.Parallel()
	input := "line1\n\nline2\n\n"
	got := splitLines(input)
	assert.Equal(t, []string{"line1", "line2"}, got)
}

// TestSplitLines_EmptyString returns an empty slice for empty input.
func TestSplitLines_EmptyString(t *testing.T) {
	t.Parallel()
	got := splitLines("")
	assert.Empty(t, got)
}

// TestSplitLines_OnlyNewlines returns an empty slice when input is only newlines.
func TestSplitLines_OnlyNewlines(t *testing.T) {
	t.Parallel()
	got := splitLines("\n\n\n")
	assert.Empty(t, got)
}

// TestSplitLines_MixedEndings verifies that mixed \n and \r\n work correctly.
func TestSplitLines_MixedEndings(t *testing.T) {
	t.Parallel()
	input := "line1\nline2\r\nline3"
	got := splitLines(input)
	assert.Equal(t, []string{"line1", "line2", "line3"}, got)
}
