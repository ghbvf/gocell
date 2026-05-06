package metadata

import (
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimalAssemblyFS builds an fstest.MapFS with a single cell and an assembly
// yaml. asmYAML is the raw assembly.yaml content; cellLevel is the consistency
// level of the single cell "testcell".
func minimalAssemblyFS(asmYAML, cellLevel string) fstest.MapFS {
	return fstest.MapFS{
		"cells/testcell/cell.yaml": &fstest.MapFile{Data: []byte(`id: testcell
type: core
consistencyLevel: ` + cellLevel + `
owner:
  team: platform
  role: cell-owner
schema:
  primary: cell_test
verify:
  smoke:
    - smoke.testcell.startup
`)},
		"assemblies/mybundle/assembly.yaml": &fstest.MapFile{Data: []byte(asmYAML)},
	}
}

// TestParseAssembly_MinimalYAML verifies that an assembly.yaml with only
// id/cells/owner (no build block) has all build fields derived.
func TestParseAssembly_MinimalYAML(t *testing.T) {
	fsys := minimalAssemblyFS(`id: mybundle
cells:
  - testcell
owner:
  team: platform
  role: bundle-owner
`, "L1")

	pm, err := NewParser("").ParseFS(fsys)
	require.NoError(t, err)

	asm := pm.Assemblies["mybundle"]
	require.NotNil(t, asm)

	assert.Equal(t, "cmd/mybundle/main.go", asm.Build.Entrypoint)
	assert.Equal(t, "mybundle", asm.Build.Binary)
	assert.Equal(t, "k8s", asm.Build.DeployTemplate)
	assert.Equal(t, "L1", asm.MaxConsistencyLevel)
	assert.Equal(t, "platform", asm.Owner.Team)
	assert.Equal(t, "bundle-owner", asm.Owner.Role)
}

// TestParseAssembly_OwnerOmittedParsesEmpty asserts the layering decision:
// parser does not enforce owner required; that's a schema/governance concern.
// Yaml decoder keeps Owner zero-valued; downstream governance/schema validates.
func TestParseAssembly_OwnerOmittedParsesEmpty(t *testing.T) {
	fsys := minimalAssemblyFS(`id: mybundle
cells:
  - testcell
`, "L0")

	pm, err := NewParser("").ParseFS(fsys)
	require.NoError(t, err)
	asm := pm.Assemblies["mybundle"]
	require.NotNil(t, asm)
	assert.Empty(t, asm.Owner.Team, "Owner.Team is left empty for governance/schema layer")
	assert.Empty(t, asm.Owner.Role)
	// Build defaults still derive
	assert.Equal(t, "cmd/mybundle/main.go", asm.Build.Entrypoint)
}

// TestParseAssembly_RejectsMaxConsistencyLevelKey checks that a yaml key
// "maxConsistencyLevel" is rejected by KnownFields(true) since the struct tag
// is yaml:"-". The structural invariant (yaml tag must be "-") is statically
// enforced by archtest ASSEMBLY-MAXCONSISTENCY-DERIVED-03; this test only
// asserts the runtime rejection occurs, without coupling to yaml.v3's
// internal error message format. Aligns with sigs.k8s.io/yaml convention:
// strict-decode tests assert err != nil only.
func TestParseAssembly_RejectsMaxConsistencyLevelKey(t *testing.T) {
	fsys := minimalAssemblyFS(`id: mybundle
cells:
  - testcell
owner:
  team: platform
  role: bundle-owner
maxConsistencyLevel: L1
`, "L1")

	_, err := NewParser("").ParseFS(fsys)
	require.Error(t, err)
}

// TestParseAssembly_DeployTemplateExplicit verifies that an explicitly set
// deployTemplate is not overwritten by derive.
func TestParseAssembly_DeployTemplateExplicit(t *testing.T) {
	fsys := minimalAssemblyFS(`id: mybundle
cells:
  - testcell
owner:
  team: platform
  role: bundle-owner
build:
  deployTemplate: compose
`, "L2")

	pm, err := NewParser("").ParseFS(fsys)
	require.NoError(t, err)

	asm := pm.Assemblies["mybundle"]
	require.NotNil(t, asm)
	assert.Equal(t, "compose", asm.Build.DeployTemplate)
	// Entrypoint and Binary still derived
	assert.Equal(t, "cmd/mybundle/main.go", asm.Build.Entrypoint)
	assert.Equal(t, "mybundle", asm.Build.Binary)
	assert.Equal(t, "L2", asm.MaxConsistencyLevel)
}
