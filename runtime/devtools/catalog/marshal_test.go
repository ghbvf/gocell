// Package catalog_test — marshal_test.go: tests for MarshalDocument.
package catalog_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/devtools/catalog"
)

// ---- TestMarshalDocument_JSONGolden ----

func TestMarshalDocument_JSONGolden(t *testing.T) {
	pm := minimalPM()
	opts := catalog.ExportOptions{
		Clock:  fixedClock(),
		Root:   "/projects/gocell",
		Filter: catalog.Filter{Include: catalog.AllIncluded()},
	}
	d, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)

	got, err := catalog.MarshalDocument(d, "json")
	require.NoError(t, err)

	goldenPath := "testdata/golden/export_minimal.json"
	if *update {
		require.NoError(t, os.WriteFile(goldenPath, got, 0o644))
	}
	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

// ---- TestMarshalDocument_YAMLGolden ----

func TestMarshalDocument_YAMLGolden(t *testing.T) {
	pm := fullPM()
	opts := catalog.ExportOptions{
		Clock:  fixedClock(),
		Root:   "/projects/gocell",
		Filter: catalog.Filter{Include: catalog.AllIncluded()},
	}
	d, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)

	got, err := catalog.MarshalDocument(d, "yaml")
	require.NoError(t, err)

	goldenPath := "testdata/golden/export_full.yaml"
	if *update {
		require.NoError(t, os.WriteFile(goldenPath, got, 0o644))
	}
	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

// ---- TestMarshalDocument_FilteredGolden ----

func TestMarshalDocument_FilteredGolden(t *testing.T) {
	pm := fullPM()
	cellDeps := &catalog.CellDepGraph{
		Nodes: []string{"accesscore", "auditcore"},
		Edges: []catalog.CellEdge{{From: "accesscore", To: "auditcore"}},
	}
	opts := catalog.ExportOptions{
		Clock: fixedClock(),
		Root:  "/projects/gocell",
		Filter: catalog.Filter{
			Cells:   []string{"accesscore"},
			Include: catalog.IncludeOptions{CellDeps: true, PackageDeps: true},
		},
		CellDeps: cellDeps,
		Packages: &catalog.PackageDepsView{}, // loading state: Graph==nil, Error==""
	}
	d, err := catalog.BuildDocument(pm, opts)
	require.NoError(t, err)

	got, err := catalog.MarshalDocument(d, "json")
	require.NoError(t, err)

	goldenPath := "testdata/golden/export_filtered.json"
	if *update {
		require.NoError(t, os.WriteFile(goldenPath, got, 0o644))
	}
	want, err := os.ReadFile(goldenPath)
	require.NoError(t, err)
	require.Equal(t, string(want), string(got))
}

// ---- TestMarshalDocument_BadFormat ----

func TestMarshalDocument_BadFormat(t *testing.T) {
	d := catalog.Document{}
	_, err := catalog.MarshalDocument(d, "xml")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "xml")
}

// ---- TestMarshalDocument_EmptyDocument ----

func TestMarshalDocument_EmptyDocument(t *testing.T) {
	d := catalog.Document{}
	got, err := catalog.MarshalDocument(d, "json")
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(got, &m))
	assert.Contains(t, m, "schemaVersion", "schemaVersion field must be present")
	assert.Contains(t, m, "apiVersion", "apiVersion field must be present")
}
