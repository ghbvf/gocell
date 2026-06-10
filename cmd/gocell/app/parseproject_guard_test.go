package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseProjectGuarded_FlatLayoutMetadataNotDiscovered reproduces #1560 F2:
// a flat-layout cell.yaml (corecells-style — cell.yaml directly under
// <root>/<cell>/, not <root>/cells/<cell>/) is present on disk but the
// conventional locator discovers zero sources. The guard must turn that
// false-green into a hard error instead of a clean pass.
func TestParseProjectGuarded_FlatLayoutMetadataNotDiscovered(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/corecellslike\n"), 0o644))
	cellDir := filepath.Join(root, "accesscore")
	require.NoError(t, os.MkdirAll(cellDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "cell.yaml"),
		[]byte("id: accesscore\ntype: core\n"), 0o644))

	_, err := parseProjectGuarded(root)
	require.Error(t, err, "flat-layout metadata present but undiscovered must fail fast")
	assert.Contains(t, err.Error(), "zero metadata sources")
}

// TestParseProjectGuarded_EmptyProjectPasses confirms a genuinely empty project
// (no cell.yaml anywhere) still parses cleanly — the guard must not regress the
// fresh/empty-project success contract (TestDispatch_SuccessPath_ExitZero /
// TestRunCheckContractHealth).
func TestParseProjectGuarded_EmptyProjectPasses(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/empty\n"), 0o644))

	project, err := parseProjectGuarded(root)
	require.NoError(t, err, "empty project must parse cleanly")
	assert.True(t, projectHasNoSources(project))
}

// TestTreeHasCellMetadata pins the precise on-disk signal that distinguishes a
// layout/locator mismatch (metadata present) from a genuinely empty module, and
// that synthetic fixtures under testdata/ / generated/ never trip the guard.
func TestTreeHasCellMetadata(t *testing.T) {
	tests := []struct {
		name string
		rel  string // metadata file to plant ("" = none)
		want bool
	}{
		{"flat cell.yaml", "accesscore/cell.yaml", true},
		{"nested slice.yaml", "auditcore/slices/auditquery/slice.yaml", true},
		{"conventional cell.yaml", "cells/mycell/cell.yaml", true},
		{"testdata fixture skipped", "testdata/fixture/cells/c/cell.yaml", false},
		{"generated output skipped", "generated/c/cell.yaml", false},
		{"no metadata", "", false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"),
				[]byte("module example.com/x\n"), 0o644))
			if tc.rel != "" {
				p := filepath.Join(root, filepath.FromSlash(tc.rel))
				require.NoError(t, os.MkdirAll(filepath.Dir(p), 0o755))
				require.NoError(t, os.WriteFile(p, []byte("id: x\n"), 0o644))
			}
			assert.Equal(t, tc.want, treeHasCellMetadata(root))
		})
	}
}
