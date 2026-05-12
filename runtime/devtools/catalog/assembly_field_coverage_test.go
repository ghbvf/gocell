package catalog_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/devtools/catalog"
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
				Cells:               []string{"alpha"},
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
	doc, err := catalog.BuildDocument(pm, catalog.ExportOptions{
		Clock: clockmock.New(time.Date(2026, 5, 7, 0, 0, 0, 0, time.UTC)),
	})
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
