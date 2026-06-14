package catalog_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/runtime/devtools/catalog"
)

// TestAssemblySpec_OwnerAndMaxConsistencyLevelRoundTrip exercises the
// metadata→AssemblySpec mapping for the K#10 Owner and MaxConsistencyLevel
// fields. The test guards against silent regressions where the metadata
// fields are populated but the wire DTO still emits zero values.
func TestAssemblySpec_OwnerAndMaxConsistencyLevelRoundTrip(t *testing.T) {
	t.Parallel()
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"alpha": {ID: "alpha", Type: "core", ConsistencyLevel: "L2"},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"mainbundle": {
				ID:                  "mainbundle",
				Cells:               metadata.CellRefs("alpha"),
				Owner:               metadata.OwnerMeta{Team: "platform", Role: "bundle-owner"},
				MaxConsistencyLevel: "L2",
				Build: metadata.BuildMeta{
					Entrypoint:     "cmd/mainbundle/main.go",
					Binary:         "mainbundle",
					DeployTemplate: "k8s",
				},
			},
		},
	}
	doc, err := catalog.BuildDocument(
		clockmock.New(time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)),
		pm,
		catalog.ExportOptions{},
	)
	require.NoError(t, err)

	var asmSpec catalog.AssemblySpec
	for _, e := range doc.Entities {
		if e.Kind == "Assembly" && e.Metadata.Name == "mainbundle" {
			s, ok := e.Spec.(catalog.AssemblySpec)
			require.True(t, ok)
			asmSpec = s
			break
		}
	}
	assert.Equal(t, "platform", asmSpec.Owner.Team)
	assert.Equal(t, "bundle-owner", asmSpec.Owner.Role)
	assert.Equal(t, "L2", asmSpec.MaxConsistencyLevel)
	assert.Equal(t, []string{"alpha"}, asmSpec.Cells)
	assert.Equal(t, "k8s", asmSpec.Build.DeployTemplate)
}

// TestAssemblySpec_TopologyRoundTrip exercises the metadata.TopologyMeta →
// AssemblySpecTopology mapping. The ASSEMBLY-META-DTO-COVERAGE-01 archtest only
// asserts the top-level `topology` field NAME is present on AssemblySpec; this
// test guards the actual VALUE mapping (colocated + remote cells) so a future
// edit that adds the field but forgets the buildAssemblyEntity copy is caught —
// the same focused round-trip guard role BuildMeta carries above.
func TestAssemblySpec_TopologyRoundTrip(t *testing.T) {
	t.Parallel()
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"alpha": {ID: "alpha", Type: "core", ConsistencyLevel: "L2"},
			"beta":  {ID: "beta", Type: "core", ConsistencyLevel: "L2"},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"splitbundle": {
				ID:    "splitbundle",
				Cells: metadata.CellRefs("alpha", "beta"),
				Owner: metadata.OwnerMeta{Team: "platform", Role: "bundle-owner"},
				Topology: metadata.TopologyMeta{
					Colocated: []string{"alpha"},
					Remote: []metadata.TopologyRemoteEntry{
						{CellID: "beta", Endpoint: "beta.svc:8080"},
					},
				},
			},
		},
	}
	doc, err := catalog.BuildDocument(
		clockmock.New(time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)),
		pm,
		catalog.ExportOptions{},
	)
	require.NoError(t, err)

	var asmSpec catalog.AssemblySpec
	for _, e := range doc.Entities {
		if e.Kind == "Assembly" && e.Metadata.Name == "splitbundle" {
			s, ok := e.Spec.(catalog.AssemblySpec)
			require.True(t, ok)
			asmSpec = s
			break
		}
	}
	assert.Equal(t, []string{"alpha"}, asmSpec.Topology.Colocated)
	require.Len(t, asmSpec.Topology.Remote, 1)
	assert.Equal(t, "beta", asmSpec.Topology.Remote[0].CellID)
	assert.Equal(t, "beta.svc:8080", asmSpec.Topology.Remote[0].Endpoint)
}
