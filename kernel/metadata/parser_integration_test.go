//go:build integration

package metadata

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// testProjectRoot returns the absolute path to the project root directory.
func testProjectRoot(t *testing.T) string {
	t.Helper()
	// This file lives at kernel/metadata/parser_integration_test.go.
	// Walk up two levels to reach the project root.
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")
	return filepath.Join(filepath.Dir(thisFile), "..", "..")
}

// TestParseRealProject is a smoke test: Parse must not error on the real
// project files. Detailed field mapping is covered by TestParseFS_FullProject;
// project-state invariants (slice/contract/journey consistency) are enforced
// by `gocell validate` (TOPO/REF/FMT/ADV governance rules).
func TestParseRealProject(t *testing.T) {
	root := testProjectRoot(t)
	p := NewParser(root)
	_, err := p.Parse()
	require.NoError(t, err, "Parse should succeed on real project files")
}

