// INVARIANT: TOPO-14 split-topology mTLS endpoint gate (#2263)
// Hard-leaning (build-time, machine-decidable on the assembly endpoint scheme).
// Non-loopback remote endpoints must be https; loopback stays plaintext-eligible.
// Build-time half of the #2263 fail-closed double gate (runtime half =
// cellmodules/celltls.Resolve + celltransport.Resolve). See validateTOPO14 godoc.

package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// buildTOPO14Project builds a minimal ProjectMeta with a single assembly carrying
// the given deployment topology. TOPO-14 reads only Topology.Remote, so no cells
// / contracts are needed.
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
// remote endpoint reached over plaintext must fire TOPO-14.
//
// Anti-vacuity: TestTOPO14_NonLoopbackHTTPS_NoError (GREEN) must also pass so a
// 0-finding result here means a real broken detector, not "no violation exists".
func TestTOPO14_NonLoopbackPlaintext_Fires(t *testing.T) {
	pm := buildTOPO14Project(metadata.TopologyMeta{
		Remote: []metadata.TopologyRemoteEntry{{CellID: "configcore", Endpoint: "configcore.svc:9090"}},
	})
	got := findByCode(NewValidator(pm, ".", clock.Real()).validateTOPO14(), codeTOPO14)
	require.GreaterOrEqual(t, len(got), 1,
		"non-loopback plaintext remote endpoint must produce ≥1 TOPO-14 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueForbidden, got[0].IssueType)
	assert.NotEmpty(t, got[0].Fix)
	assert.NotEmpty(t, got[0].Message)
}

// TestTOPO14_NonLoopbackHTTPS_NoError is the GREEN anti-vacuity counterpart: a
// non-loopback https endpoint satisfies the gate.
func TestTOPO14_NonLoopbackHTTPS_NoError(t *testing.T) {
	pm := buildTOPO14Project(metadata.TopologyMeta{
		Remote: []metadata.TopologyRemoteEntry{{CellID: "configcore", Endpoint: "https://configcore.svc:8443"}},
	})
	got := findByCode(NewValidator(pm, ".", clock.Real()).validateTOPO14(), codeTOPO14)
	assert.Empty(t, got, "https non-loopback remote endpoint must not fire TOPO-14")
}

// TestTOPO14_Loopback_NoError confirms loopback endpoints stay plaintext-eligible
// (local multi-process dev), regardless of scheme.
func TestTOPO14_Loopback_NoError(t *testing.T) {
	for _, ep := range []string{"127.0.0.1:9090", "https://localhost:8443", "[::1]:9090"} {
		pm := buildTOPO14Project(metadata.TopologyMeta{
			Remote: []metadata.TopologyRemoteEntry{{CellID: "configcore", Endpoint: ep}},
		})
		got := findByCode(NewValidator(pm, ".", clock.Real()).validateTOPO14(), codeTOPO14)
		assert.Emptyf(t, got, "loopback endpoint %q must stay plaintext-eligible", ep)
	}
}

// TestTOPO14_AllColocated_NoError confirms an assembly with no remote cells never
// fires.
func TestTOPO14_AllColocated_NoError(t *testing.T) {
	pm := buildTOPO14Project(metadata.TopologyMeta{Colocated: []string{"accesscore"}})
	got := findByCode(NewValidator(pm, ".", clock.Real()).validateTOPO14(), codeTOPO14)
	assert.Empty(t, got)
}
