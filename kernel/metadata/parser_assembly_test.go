package metadata

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minimalAssemblyFS builds an fstest.MapFS with a single cell and an assembly
// yaml. asmYAML is the raw assembly.yaml content; cellLevel is the consistency
// level of the single cell "testcell".
func minimalAssemblyFS(asmYAML string, cellLevel string) fstest.MapFS {
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

// TestParseAssembly_OwnerRequired checks that omitting owner causes the derive
// step to fail fast (owner.Team empty).
func TestParseAssembly_OwnerRequired(t *testing.T) {
	// assembly without owner field — KnownFields(true) passes (owner is optional
	// in yaml struct, schema validation is a separate governance phase), so we
	// rely on the derive step's owner-empty check.
	fsys := minimalAssemblyFS(`id: mybundle
cells:
  - testcell
`, "L0")

	_, err := NewParser("").ParseFS(fsys)
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "owner") ||
			strings.Contains(err.Error(), "ERR_METADATA_INVALID"),
		"expected owner-related error, got: %v", err)
}

// TestParseAssembly_RejectsMaxConsistencyLevelKey checks that a yaml key
// "maxConsistencyLevel" is rejected by KnownFields(true) since the struct tag
// is yaml:"-".
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
	assert.Contains(t, err.Error(), "maxConsistencyLevel")
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
