package assembly

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
	ecErr "github.com/ghbvf/gocell/pkg/errcode"
)

// buildCompositionProjectForTopology returns a ProjectMeta with composition form
// (CompositionAPI: true) and an assembly whose topology can be set in tests.
func buildCompositionProjectForTopology(topo metadata.TopologyMeta) *metadata.ProjectMeta {
	cells := []metadata.AssemblyCellRef{
		{ID: metadatatest.CellIDAccessCore},
		{ID: metadatatest.CellIDAuditCore},
	}
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDAccessCore: {ID: metadatatest.CellIDAccessCore},
			metadatatest.CellIDAuditCore:  {ID: metadatatest.CellIDAuditCore},
		},
		Slices:    make(map[string]*metadata.SliceMeta),
		Contracts: make(map[string]*metadata.ContractMeta),
		Journeys:  make(map[string]*metadata.JourneyMeta),
		Assemblies: map[string]*metadata.AssemblyMeta{
			"topobundle": {
				ID:       "topobundle",
				Build:    metadata.BuildMeta{CompositionAPI: true},
				Cells:    cells,
				Topology: topo,
			},
		},
	}
}

// TestGenerateModulesGen_DeploymentTopology_Empty verifies that the composition
// form always emits generatedDeploymentTopology() returning bootstrap.DeploymentTopologySpec{}
// when the assembly has no topology declared (all-colocated default).
func TestGenerateModulesGen_DeploymentTopology_Empty(t *testing.T) {
	project := buildCompositionProjectForTopology(metadata.TopologyMeta{})
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateModulesGen("topobundle")
	require.NoError(t, err)

	content := string(out)
	// ALWAYS-emit: function must be present even when topology is empty.
	assert.Contains(t, content, "func generatedDeploymentTopology() bootstrap.DeploymentTopologySpec")
	// bootstrap import must always be present (return type references it).
	assert.Contains(t, content, `"github.com/ghbvf/gocell/runtime/bootstrap"`)
	// Empty topology returns zero-value spec.
	assert.Contains(t, content, "return bootstrap.DeploymentTopologySpec{}")
}

// TestGenerateModulesGen_DeploymentTopology_Colocated verifies the composition
// form emits a non-trivial generatedDeploymentTopology when topology has only
// colocated cells.
func TestGenerateModulesGen_DeploymentTopology_Colocated(t *testing.T) {
	topo := metadata.TopologyMeta{
		Colocated: []string{metadatatest.CellIDAccessCore, metadatatest.CellIDAuditCore},
	}
	project := buildCompositionProjectForTopology(topo)
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateModulesGen("topobundle")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, "func generatedDeploymentTopology() bootstrap.DeploymentTopologySpec")
	assert.Contains(t, content, `"github.com/ghbvf/gocell/runtime/bootstrap"`)
	// Must emit the Colocated slice with both cell IDs.
	assert.Contains(t, content, `"accesscore"`)
	assert.Contains(t, content, `"auditcore"`)
	// No Remote block.
	assert.NotContains(t, content, "RemoteCellEndpoint")
}

// TestGenerateModulesGen_DeploymentTopology_Remote verifies the composition
// form emits Remote entries in generatedDeploymentTopology.
func TestGenerateModulesGen_DeploymentTopology_Remote(t *testing.T) {
	topo := metadata.TopologyMeta{
		Colocated: []string{metadatatest.CellIDAccessCore},
		Remote:    []metadata.TopologyRemoteEntry{{CellID: metadatatest.CellIDAuditCore, Endpoint: "audit.svc:9090"}},
	}
	project := buildCompositionProjectForTopology(topo)
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	out, err := gen.GenerateModulesGen("topobundle")
	require.NoError(t, err)

	content := string(out)
	assert.Contains(t, content, "func generatedDeploymentTopology() bootstrap.DeploymentTopologySpec")
	assert.Contains(t, content, `"github.com/ghbvf/gocell/runtime/bootstrap"`)
	assert.Contains(t, content, `"accesscore"`)
	assert.Contains(t, content, `bootstrap.RemoteCellEndpoint`)
	assert.Contains(t, content, `"auditcore"`)
	assert.Contains(t, content, `"audit.svc:9090"`)
}

// TestGenerateModulesGen_DeploymentTopology_IllegalMutualExclusion is the
// synthetic-red test (F10): a cell appearing in both colocated AND remote is an
// illegal topology that GenerateModulesGen must reject before emitting any
// output, returning an errcode.ErrMetadataInvalid error.
func TestGenerateModulesGen_DeploymentTopology_IllegalMutualExclusion(t *testing.T) {
	topo := metadata.TopologyMeta{
		// accesscore in BOTH colocated and remote — mutual exclusion violation.
		Colocated: []string{metadatatest.CellIDAccessCore},
		Remote:    []metadata.TopologyRemoteEntry{{CellID: metadatatest.CellIDAccessCore, Endpoint: "dup.svc:9090"}},
	}
	project := buildCompositionProjectForTopology(topo)
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	_, err := gen.GenerateModulesGen("topobundle")
	require.Error(t, err, "illegal topology (mutual exclusion) must fail generation closed")

	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec), "error must be an errcode.Error, got: %T", err)
	assert.Equal(t, ecErr.ErrMetadataInvalid, ec.Code)
	assert.Contains(t, strings.ToLower(err.Error()), "mutual exclusion",
		"error message must mention 'mutual exclusion' so a generic error cannot pass vacuously")
}

// TestGenerateModulesGen_DeploymentTopology_IllegalNonExhaustive is a
// synthetic-red test (F10): a non-exhaustive topology (a cell appears in neither
// colocated nor remote) must fail generation closed.
func TestGenerateModulesGen_DeploymentTopology_IllegalNonExhaustive(t *testing.T) {
	topo := metadata.TopologyMeta{
		// Only accesscore declared; auditcore missing — non-exhaustive.
		Colocated: []string{metadatatest.CellIDAccessCore},
	}
	project := buildCompositionProjectForTopology(topo)
	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")

	_, err := gen.GenerateModulesGen("topobundle")
	require.Error(t, err, "non-exhaustive topology must fail generation closed")

	var ec *ecErr.Error
	require.True(t, errors.As(err, &ec), "error must be an errcode.Error, got: %T", err)
	assert.Equal(t, ecErr.ErrMetadataInvalid, ec.Code)
	assert.Contains(t, strings.ToLower(err.Error()), "non-exhaustive",
		"error message must mention 'non-exhaustive' so a generic error cannot pass vacuously")
}

// TestBuildDeploymentTopologyData_Empty verifies the helper returns a zero value
// for an empty metadata.TopologyMeta.
func TestBuildDeploymentTopologyData_Empty(t *testing.T) {
	got := buildDeploymentTopologyData(metadata.TopologyMeta{})
	assert.Nil(t, got.Colocated)
	assert.Nil(t, got.Remote)
}

// TestBuildDeploymentTopologyData_Populated verifies the helper faithfully
// copies colocated IDs and remote entries.
func TestBuildDeploymentTopologyData_Populated(t *testing.T) {
	topo := metadata.TopologyMeta{
		Colocated: []string{"aaa", "bbb"},
		Remote:    []metadata.TopologyRemoteEntry{{CellID: "ccc", Endpoint: "ccc.svc:9090"}},
	}
	got := buildDeploymentTopologyData(topo)
	assert.Equal(t, []string{"aaa", "bbb"}, got.Colocated)
	require.Len(t, got.Remote, 1)
	assert.Equal(t, "ccc", got.Remote[0].CellID)
	assert.Equal(t, "ccc.svc:9090", got.Remote[0].Endpoint)

	// Verify it is a defensive copy: mutating the topo should not affect the data.
	topo.Colocated[0] = "mutated"
	assert.Equal(t, "aaa", got.Colocated[0], "buildDeploymentTopologyData must copy the slice")
}

// TestGenerateModulesGen_CompositionForm_IncludesDeploymentTopology updates the
// existing composition-form golden assertion to confirm generatedDeploymentTopology
// is always emitted alongside generatedProjectionSourceTopics.
func TestGenerateModulesGen_CompositionForm_IncludesDeploymentTopology(t *testing.T) {
	project := buildModulesTestProject()
	asm := project.Assemblies["corebundle"]
	asm.Build.CompositionAPI = true

	gen := NewGenerator(project, "github.com/ghbvf/gocell", "")
	out, err := gen.GenerateModulesGen("corebundle")
	require.NoError(t, err)

	content := string(out)
	// Existing assertions still hold (regression guard).
	assert.Contains(t, content, "func generatedCellModules() []composition.CellModule")
	assert.Contains(t, content, "func generatedProjectionSourceTopics() []string")
	// New: generatedDeploymentTopology must ALWAYS be emitted.
	assert.Contains(t, content, "func generatedDeploymentTopology() bootstrap.DeploymentTopologySpec")
	// bootstrap import must be present.
	assert.Contains(t, content, `"github.com/ghbvf/gocell/runtime/bootstrap"`)
	// corebundle has no topology → empty spec.
	assert.Contains(t, content, "return bootstrap.DeploymentTopologySpec{}")
	// Ensure the function name does not appear multiple times (emitted exactly once).
	assert.Equal(t, 1, strings.Count(content, "func generatedDeploymentTopology()"),
		"generatedDeploymentTopology must be emitted exactly once")
}
