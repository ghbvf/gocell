// INVARIANT: TOPO-14 split-topology mTLS endpoint gate (#2263)
// Hard-leaning (build-time, machine-decidable on the assembly endpoint scheme).
// Non-loopback group endpoints (in a ≥2-group split) must be https; loopback
// stays plaintext-eligible. Build-time half of the #2263 fail-closed double gate
// (runtime half = cellmodules/celltls.Resolve + celltransport.Resolve). See
// validateTOPO14 godoc.

package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// buildTOPO14Project builds a minimal ProjectMeta with a single assembly carrying
// the given deployment topology. TOPO-14 reads only Topology.Groups[].Endpoint, so
// no cells / contracts are needed.
func buildTOPO14Project(topo metadata.TopologyMeta) *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly(nil, topo),
		},
	}
}

// TestTOPO14_NonLoopbackPlaintext_Fires is the primary RED test: a non-loopback
// group endpoint reached over plaintext (in a ≥2-group split) must fire TOPO-14.
//
// Anti-vacuity: TestTOPO14_NonLoopbackHTTPS_NoError (GREEN) must also pass so a
// 0-finding result here means a real broken detector, not "no violation exists".
func TestTOPO14_NonLoopbackPlaintext_Fires(t *testing.T) {
	pm := buildTOPO14Project(metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
		{Role: "core", Cells: []string{"configcore"}, Endpoint: "configcore.svc:9090"}, // plaintext non-loopback
		{Role: "edge", Cells: []string{"accesscore"}, Endpoint: "https://edge.svc:8443"},
	}})
	got := findByCode(NewValidator(pm, ".", clock.Real()).validateTOPO14(), codeTOPO14)
	require.GreaterOrEqual(t, len(got), 1,
		"non-loopback plaintext group endpoint must produce ≥1 TOPO-14 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueForbidden, got[0].IssueType)
	assert.NotEmpty(t, got[0].Fix)
	assert.NotEmpty(t, got[0].Message)
}

// TestTOPO14_NonLoopbackHTTPS_NoError is the GREEN anti-vacuity counterpart: all
// non-loopback group endpoints using https satisfy the gate.
func TestTOPO14_NonLoopbackHTTPS_NoError(t *testing.T) {
	pm := buildTOPO14Project(metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
		{Role: "core", Cells: []string{"configcore"}, Endpoint: "https://configcore.svc:8443"},
		{Role: "edge", Cells: []string{"accesscore"}, Endpoint: "https://edge.svc:8443"},
	}})
	got := findByCode(NewValidator(pm, ".", clock.Real()).validateTOPO14(), codeTOPO14)
	assert.Empty(t, got, "https non-loopback group endpoints must not fire TOPO-14")
}

// TestTOPO14_Loopback_NoError confirms loopback endpoints stay plaintext-eligible
// (local multi-process dev), regardless of scheme.
func TestTOPO14_Loopback_NoError(t *testing.T) {
	for _, ep := range []string{"127.0.0.1:9090", "https://localhost:8443", "[::1]:9090"} {
		pm := buildTOPO14Project(metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
			{Role: "core", Cells: []string{"configcore"}, Endpoint: ep},
			{Role: "edge", Cells: []string{"accesscore"}, Endpoint: "https://edge.svc:8443"},
		}})
		got := findByCode(NewValidator(pm, ".", clock.Real()).validateTOPO14(), codeTOPO14)
		assert.Emptyf(t, got, "loopback endpoint %q must stay plaintext-eligible", ep)
	}
}

// TestTOPO14_SingleGroup_NoError confirms an assembly with fewer than 2 groups has
// no cross-process boundary and never fires (even with a plaintext endpoint).
func TestTOPO14_SingleGroup_NoError(t *testing.T) {
	pm := buildTOPO14Project(metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
		{Role: "monolith", Cells: []string{"accesscore"}, Endpoint: "mono.svc:9090"},
	}})
	got := findByCode(NewValidator(pm, ".", clock.Real()).validateTOPO14(), codeTOPO14)
	assert.Empty(t, got, "single-group assembly has no cross-process boundary")
}

// TestTOPO14_AllColocated_NoError confirms an assembly with no topology never fires.
func TestTOPO14_AllColocated_NoError(t *testing.T) {
	pm := buildTOPO14Project(metadata.TopologyMeta{})
	got := findByCode(NewValidator(pm, ".", clock.Real()).validateTOPO14(), codeTOPO14)
	assert.Empty(t, got)
}
