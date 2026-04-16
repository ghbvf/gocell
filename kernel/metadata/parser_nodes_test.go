package metadata

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestParseFS_NodesPopulated verifies that ParseFS records a DocumentNode
// for every parsed YAML file into ProjectMeta.FileNodes, enabling downstream
// validators to report file:line:column locations.
func TestParseFS_NodesPopulated(t *testing.T) {
	fs := fstest.MapFS{
		"cells/x/cell.yaml": &fstest.MapFile{Data: []byte(`id: x
type: core
consistencyLevel: L1
owner:
  team: t
  role: r
schema:
  primary: tbl
verify:
  smoke: []
`)},
		"cells/x/slices/s/slice.yaml": &fstest.MapFile{Data: []byte(`id: s
belongsToCell: x
contractUsages:
  - contract: http.foo.v1
    role: serve
verify:
  unit: []
  contract: []
allowedFiles:
  - cells/x/slices/s/**
`)},
		"contracts/http/foo/v1/contract.yaml": &fstest.MapFile{Data: []byte(`id: http.foo.v1
kind: http
lifecycle: active
endpoints:
  server: x
  clients:
    - y
`)},
		"journeys/J-smoke.yaml": &fstest.MapFile{Data: []byte(`id: J-smoke
goal: smoke
owner: {team: t, role: r}
cells: [x]
contracts: [http.foo.v1]
passCriteria: []
`)},
		"assemblies/a/assembly.yaml": &fstest.MapFile{Data: []byte(`id: a
cells: [x]
build:
  entrypoint: main.go
  binary: a
  deployTemplate: t
`)},
		"actors.yaml": &fstest.MapFile{Data: []byte(`- id: ext
  type: external
  maxConsistencyLevel: L2
`)},
		"journeys/status-board.yaml": &fstest.MapFile{Data: []byte(`- journeyId: J-smoke
  state: green
  risk: none
  blocker: ""
  updatedAt: "2026-01-01"
`)},
	}

	p := NewParser(".")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)
	require.NotNil(t, pm.FileNodes, "pm.FileNodes must be populated")

	wantFiles := []string{
		"cells/x/cell.yaml",
		"cells/x/slices/s/slice.yaml",
		"contracts/http/foo/v1/contract.yaml",
		"journeys/J-smoke.yaml",
		"assemblies/a/assembly.yaml",
		"actors.yaml",
		"journeys/status-board.yaml",
	}
	for _, path := range wantFiles {
		n, ok := pm.FileNodes[path]
		if !ok {
			t.Errorf("missing Node for %s", path)
			continue
		}
		require.NotNil(t, n, "Node for %s is nil", path)
		assert.Equal(t, yaml.DocumentNode, n.Kind, "Node for %s is not DocumentNode", path)
	}
}

// TestParseFS_NodesEnableLocate verifies that Find / Locate work against the
// stored nodes to recover line numbers for known fields.
func TestParseFS_NodesEnableLocate(t *testing.T) {
	fs := fstest.MapFS{
		"cells/x/cell.yaml": &fstest.MapFile{Data: []byte(
			"id: x\n" + // line 1
				"type: core\n" + // line 2
				"consistencyLevel: L1\n" + // line 3
				"owner:\n" + // line 4
				"  team: t\n" + // line 5
				"  role: r\n" + // line 6
				"schema:\n" + // line 7
				"  primary: tbl\n" + // line 8
				"verify:\n" + // line 9
				"  smoke: []\n", // line 10
		)},
	}

	p := NewParser(".")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)

	root := pm.FileNodes["cells/x/cell.yaml"]
	require.NotNil(t, root)

	// Top-level field line numbers.
	cases := map[string]int{
		"id":               1,
		"consistencyLevel": 3,
		"owner.team":       5,
		"owner.role":       6,
		"schema.primary":   8,
	}
	for path, wantLine := range cases {
		pos := Locate(root, path)
		if !pos.Known() {
			t.Errorf("Locate(%s) unknown", path)
			continue
		}
		assert.Equal(t, wantLine, pos.Line, "Locate(%s).Line mismatch", path)
	}
}

// TestParseFS_RejectsMultiDocument verifies the `---`-delimited second
// document triggers an error rather than being silently ignored by the first
// Decode call.
func TestParseFS_RejectsMultiDocument(t *testing.T) {
	fs := fstest.MapFS{
		"cells/x/cell.yaml": &fstest.MapFile{Data: []byte(
			"id: first\n" +
				"type: core\n" +
				"consistencyLevel: L1\n" +
				"owner: {team: t, role: r}\n" +
				"schema: {primary: tbl}\n" +
				"verify: {smoke: []}\n" +
				"---\n" +
				"id: second\n" +
				"type: core\n" +
				"consistencyLevel: L1\n" +
				"owner: {team: t, role: r}\n" +
				"schema: {primary: tbl}\n" +
				"verify: {smoke: []}\n",
		)},
	}
	_, err := NewParser(".").ParseFS(fs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multiple YAML documents")
}

// TestParseFS_AcceptsLeadingDocumentMarker: a single document preceded by the
// "---" document marker is valid YAML and must still parse.
func TestParseFS_AcceptsLeadingDocumentMarker(t *testing.T) {
	fs := fstest.MapFS{
		"cells/x/cell.yaml": &fstest.MapFile{Data: []byte(
			"---\n" +
				"id: x\n" +
				"type: core\n" +
				"consistencyLevel: L1\n" +
				"owner: {team: t, role: r}\n" +
				"schema: {primary: tbl}\n" +
				"verify: {smoke: []}\n",
		)},
	}
	pm, err := NewParser(".").ParseFS(fs)
	require.NoError(t, err)
	assert.NotNil(t, pm.Cells["x"])
}

// TestParseFS_NodesAbsentWhenEmpty verifies empty optional files are skipped
// (not stored under FileNodes) so downstream lookups stay nil-safe.
func TestParseFS_NodesAbsentWhenEmpty(t *testing.T) {
	fs := fstest.MapFS{
		"actors.yaml":                &fstest.MapFile{Data: []byte("")},
		"journeys/status-board.yaml": &fstest.MapFile{Data: []byte("")},
	}
	p := NewParser(".")
	pm, err := p.ParseFS(fs)
	require.NoError(t, err)

	_, haveActors := pm.FileNodes["actors.yaml"]
	_, haveStatus := pm.FileNodes["journeys/status-board.yaml"]
	assert.False(t, haveActors, "empty actors.yaml should not populate FileNodes")
	assert.False(t, haveStatus, "empty status-board.yaml should not populate FileNodes")
}
