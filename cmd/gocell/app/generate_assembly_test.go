package app

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestAssemblyIDsToGenerate_AllSorted asserts that --all output ordering is
// stable. Go map iteration is randomized; without an explicit sort, stdout
// listings and golden-file comparisons drift between runs.
func TestAssemblyIDsToGenerate_AllSorted(t *testing.T) {
	t.Parallel()
	project := &metadata.ProjectMeta{
		Assemblies: map[string]*metadata.AssemblyMeta{
			"zeta":  {ID: "zeta"},
			"alpha": {ID: "alpha"},
			"mu":    {ID: "mu"},
		},
	}
	got := assemblyIDsToGenerate(project, "", true)
	assert.Equal(t, []string{"alpha", "mu", "zeta"}, got)
}

// TestAssemblyIDsToGenerate_SingleID returns the single id passed in.
func TestAssemblyIDsToGenerate_SingleID(t *testing.T) {
	t.Parallel()
	project := &metadata.ProjectMeta{
		Assemblies: map[string]*metadata.AssemblyMeta{
			"alpha": {ID: "alpha"},
			"beta":  {ID: "beta"},
		},
	}
	got := assemblyIDsToGenerate(project, "beta", false)
	assert.Equal(t, []string{"beta"}, got)
}
