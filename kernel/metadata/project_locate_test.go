package metadata

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestProjectMeta_Locate_Known(t *testing.T) {
	pm := &ProjectMeta{}
	node := parseTestNode(t, "id: test-cell\nowner:\n  team: platform\n")
	pm.SetFileNode("cells/test/cell.yaml", node)

	pos := pm.Locate("cells/test/cell.yaml", "id")
	assert.True(t, pos.Known())
	assert.Equal(t, 1, pos.Line)
}

func TestProjectMeta_Locate_NilFileNodes(t *testing.T) {
	pm := &ProjectMeta{}
	pos := pm.Locate("any.yaml", "id")
	assert.False(t, pos.Known())
}

func TestProjectMeta_Locate_MissingFile(t *testing.T) {
	pm := &ProjectMeta{}
	node := parseTestNode(t, "id: x\n")
	pm.SetFileNode("a.yaml", node)

	pos := pm.Locate("b.yaml", "id")
	assert.False(t, pos.Known())
}

func TestProjectMeta_Locate_EmptyArgs(t *testing.T) {
	pm := &ProjectMeta{}
	node := parseTestNode(t, "id: x\n")
	pm.SetFileNode("a.yaml", node)

	assert.False(t, pm.Locate("", "id").Known())
	assert.False(t, pm.Locate("a.yaml", "").Known())
}

func TestProjectMeta_FileNode_Present(t *testing.T) {
	pm := &ProjectMeta{}
	node := parseTestNode(t, "id: x\n")
	pm.SetFileNode("a.yaml", node)

	got, ok := pm.FileNode("a.yaml")
	require.True(t, ok)
	assert.NotNil(t, got)
}

func TestProjectMeta_FileNode_Absent(t *testing.T) {
	pm := &ProjectMeta{}
	got, ok := pm.FileNode("missing.yaml")
	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestProjectMeta_HasFileNodes(t *testing.T) {
	pm := &ProjectMeta{}
	assert.False(t, pm.HasFileNodes())

	pm.SetFileNode("a.yaml", parseTestNode(t, "id: x\n"))
	assert.True(t, pm.HasFileNodes())
}

// parseTestNode decodes a YAML string into a *yaml.Node for testing.
func parseTestNode(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(src), &n))
	return &n
}
