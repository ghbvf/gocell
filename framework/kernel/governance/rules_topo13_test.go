// INVARIANT: TOPO-13 broker-mandatory static gate (Epic #1423 US3)
// Medium, permanent ceiling. Production-reachable since TOPO-12 was removed in
// US5 #1966. Blind-spot: cannot verify a broker is wired (runtime concern). See
// validateTOPO13 godoc and ADR 202606131142-1423.

package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
)

// buildTOPO13Project builds a ProjectMeta for TOPO-13 tests.
// pubCellID is the publisher cell; subCellID is the subscriber cell.
// Both are in the assembly with the given topology. The contract is always
// kind=event because TOPO-13 only checks event contracts.
func buildTOPO13Project(
	pubCellID, subCellID string,
	asmCellIDs []string,
	topo metadata.TopologyMeta,
) *metadata.ProjectMeta {
	cells := map[string]*metadata.CellMeta{}
	cells[pubCellID] = topoTestCell(pubCellID)
	if subCellID != pubCellID {
		cells[subCellID] = topoTestCell(subCellID)
	}

	contract := topoTestContract("event.data.v1", "event", pubCellID)
	contract.Endpoints.Subscribers = []string{subCellID}

	return &metadata.ProjectMeta{
		Cells:  cells,
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"event.data.v1": contract,
		},
		Journeys: map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly(asmCellIDs, topo),
		},
	}
}

// =============================================================================
// TOPO-13: broker-mandatory static gate
// =============================================================================

// TestTOPO13_CrossProcessEvent_Split is the primary RED test.
// Publisher cell is in colocated; subscriber cell is in remote → cross-process
// event pub/sub → TOPO-13 must fire.
//
// Anti-vacuity note: if this test returns 0 findings, the detector is broken.
// TestTOPO13_ColocatedEvent_NoError must also pass (0 findings) to confirm the
// detector is not a false-positive machine.
func TestTOPO13_CrossProcessEvent_Split(t *testing.T) {
	pub := metadatatest.CellIDCellA // colocated side
	sub := metadatatest.CellIDCellB // remote side

	pm := buildTOPO13Project(
		pub, sub, []string{pub, sub},
		metadata.TopologyMeta{
			Colocated: []string{pub},
			Remote:    []metadata.TopologyRemoteEntry{{CellID: sub, Endpoint: "remote.svc:9090"}},
		},
	)
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO13(), codeTOPO13)
	require.GreaterOrEqual(t, len(got), 1,
		"split topology with cross-process event pub/sub must produce ≥1 TOPO-13 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueForbidden, got[0].IssueType)
	assert.NotEmpty(t, got[0].Fix)
	assert.NotEmpty(t, got[0].Message)
}

// TestTOPO13_ColocatedEvent_NoError is the anti-vacuity GREEN test.
// Publisher and subscriber are both in colocated (same process) → no cross-process
// boundary → TOPO-13 must NOT fire.
//
// If TestTOPO13_CrossProcessEvent_Split (RED) also returned 0, the detector
// would be vacuously non-firing — this test separates "detector works but no
// violation" from "detector is dead".
func TestTOPO13_ColocatedEvent_NoError(t *testing.T) {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB

	pm := buildTOPO13Project(
		pub, sub, []string{pub, sub},
		metadata.TopologyMeta{
			Colocated: []string{pub, sub},
		},
	)
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO13(), codeTOPO13)
	assert.Empty(t, got,
		"colocated pub/sub should produce 0 TOPO-13 findings (anti-vacuity: if RED test "+
			"also returns 0, the detector is broken)")
}

// TestTOPO13_EmptyTopology_NoError: empty topology → all cells are Local (same process)
// → no cross-process boundary → TOPO-13 must NOT fire.
func TestTOPO13_EmptyTopology_NoError(t *testing.T) {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB

	pm := buildTOPO13Project(
		pub, sub, []string{pub, sub},
		metadata.TopologyMeta{},
	)
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO13(), codeTOPO13)
	assert.Empty(t, got, "empty topology (all cells local) should produce 0 TOPO-13 findings")
}

// TestTOPO13_NonSplitAssembly_Skipped: assembly with no topology.remote →
// TOPO-13 skips it entirely (only split assemblies are checked).
func TestTOPO13_NonSplitAssembly_Skipped(t *testing.T) {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB

	// No remote entries → not a split assembly → TOPO-13 must skip.
	pm := buildTOPO13Project(
		pub, sub, []string{pub, sub},
		metadata.TopologyMeta{
			Colocated: []string{pub, sub},
		},
	)
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO13(), codeTOPO13)
	assert.Empty(t, got, "non-split assembly (no topology.remote) should produce 0 TOPO-13 findings")
}

// TestTOPO13_NonEventContract_Skipped: HTTP contract with cross-process endpoints
// is NOT a TOPO-13 violation (only event contracts are checked).
func TestTOPO13_NonEventContract_Skipped(t *testing.T) {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB

	// HTTP contract — not event kind → TOPO-13 must skip.
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDCellA: topoTestCell(pub),
			metadatatest.CellIDCellB: topoTestCell(sub),
		},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"http.data.v1": func() *metadata.ContractMeta {
				c := topoTestContract("http.data.v1", "http", pub)
				c.Endpoints.Clients = []string{sub}
				return c
			}(),
		},
		Journeys: map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{pub, sub}, metadata.TopologyMeta{
				Colocated: []string{pub},
				Remote:    []metadata.TopologyRemoteEntry{{CellID: sub, Endpoint: "remote.svc:9090"}},
			}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO13(), codeTOPO13)
	assert.Empty(t, got, "non-event contract (http) should be skipped by TOPO-13")
}

// TestTOPO13_ExternalActorSubscriber_Skipped: event contract with an external
// actor as subscriber — actors are not cells, TOPO-13 only governs cell placements.
func TestTOPO13_ExternalActorSubscriber_Skipped(t *testing.T) {
	pub := metadatatest.CellIDCellA
	actorID := "externalconsumer" // not a cell — a registered actor

	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDCellA: topoTestCell(pub),
		},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"event.data.v1": func() *metadata.ContractMeta {
				c := topoTestContract("event.data.v1", "event", pub)
				c.Endpoints.Subscribers = []string{actorID}
				return c
			}(),
		},
		Journeys: map[string]*metadata.JourneyMeta{},
		Actors: []metadata.ActorMeta{
			{ID: actorID},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{pub}, metadata.TopologyMeta{
				Colocated: []string{pub},
			}),
		},
	}
	// Give the assembly a remote entry so it counts as split, but actor is not a cell.
	// We need a second cell to have a valid split topology.
	bystander := metadatatest.CellIDCellB
	pm.Cells[metadatatest.CellIDCellB] = topoTestCell(bystander)
	pm.Assemblies["testasm"] = topoTestAssembly([]string{pub, bystander}, metadata.TopologyMeta{
		Colocated: []string{pub},
		Remote:    []metadata.TopologyRemoteEntry{{CellID: bystander, Endpoint: "remote.svc:9090"}},
	})

	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO13(), codeTOPO13)
	assert.Empty(t, got, "external actor subscriber should be skipped by TOPO-13 (not a cell)")
}

// TestTOPO13_FrameworkOwnedContract_Skipped: framework-owned event contract is
// provider-agnostic → skipped by TOPO-13 (mirrors TOPO-11 skip condition).
func TestTOPO13_FrameworkOwnedContract_Skipped(t *testing.T) {
	sub := metadatatest.CellIDCellA
	bystander := metadatatest.CellIDCellB

	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDCellA: topoTestCell(sub),
			metadatatest.CellIDCellB: topoTestCell(bystander),
		},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"event.framework.v1": {
				ID:               "event.framework.v1",
				Kind:             "event",
				Lifecycle:        "draft",
				ConsistencyLevel: "L1",
				OwnerCell:        metadata.FrameworkOwnerSentinel,
				File:             "contracts/event/framework/v1/contract.yaml",
				Endpoints: metadata.EndpointsMeta{
					Subscribers: []string{sub},
				},
			},
		},
		Journeys: map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{sub, bystander}, metadata.TopologyMeta{
				Colocated: []string{sub},
				Remote:    []metadata.TopologyRemoteEntry{{CellID: bystander, Endpoint: "remote.svc:9090"}},
			}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO13(), codeTOPO13)
	assert.Empty(t, got, "framework-owned event contract should be skipped by TOPO-13")
}

// TestTOPO13_SubscriberRemote_PublisherLocal: the reverse cross-process case —
// publisher in colocated, subscriber in remote → same violation → TOPO-13 must fire.
// (Verifies the IsLocal() != IsLocal() condition is symmetric.)
func TestTOPO13_SubscriberRemote_PublisherLocal(t *testing.T) {
	pub := metadatatest.CellIDCellA // colocated
	sub := metadatatest.CellIDCellB // remote

	pm := buildTOPO13Project(
		pub, sub, []string{pub, sub},
		metadata.TopologyMeta{
			Colocated: []string{pub},
			Remote:    []metadata.TopologyRemoteEntry{{CellID: sub, Endpoint: "remote.svc:9090"}},
		},
	)
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO13(), codeTOPO13)
	require.GreaterOrEqual(t, len(got), 1,
		"publisher local, subscriber remote must produce ≥1 TOPO-13 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
}

// TestTOPO13_PublisherRemote_SubscriberLocal: publisher in remote, subscriber
// in colocated → also cross-process → TOPO-13 must fire.
func TestTOPO13_PublisherRemote_SubscriberLocal(t *testing.T) {
	pub := metadatatest.CellIDCellA // remote
	sub := metadatatest.CellIDCellB // colocated

	pm := buildTOPO13Project(
		pub, sub, []string{pub, sub},
		metadata.TopologyMeta{
			Colocated: []string{sub},
			Remote:    []metadata.TopologyRemoteEntry{{CellID: pub, Endpoint: "remote.svc:9090"}},
		},
	)
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO13(), codeTOPO13)
	require.GreaterOrEqual(t, len(got), 1,
		"publisher remote, subscriber local must produce ≥1 TOPO-13 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
}

// topo13BothRemoteProject builds a split assembly where pub and sub are BOTH in
// topology.remote (at the given endpoints) plus a colocated bystander so the
// assembly counts as split.
func topo13BothRemoteProject(pubEndpoint, subEndpoint string) *metadata.ProjectMeta {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB
	bystander := "cellC"
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			pub:       topoTestCell(pub),
			sub:       topoTestCell(sub),
			bystander: topoTestCell(bystander),
		},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"event.data.v1": func() *metadata.ContractMeta {
				c := topoTestContract("event.data.v1", "event", pub)
				c.Endpoints.Subscribers = []string{sub}
				return c
			}(),
		},
		Journeys: map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{pub, sub, bystander}, metadata.TopologyMeta{
				Colocated: []string{bystander},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: pub, Endpoint: pubEndpoint},
					{CellID: sub, Endpoint: subEndpoint},
				},
			}),
		},
	}
}

// TestTOPO13_BothRemote_DifferentEndpoint_Fired locks the fix for the both-remote
// blind-spot: two remote cells at DIFFERENT endpoints are different processes, so
// their cross-process event pub/sub DOES require a broker (each assembly
// fail-closes its own cross-process events; #2188 review F1).
func TestTOPO13_BothRemote_DifferentEndpoint_Fired(t *testing.T) {
	pm := topo13BothRemoteProject("pub.svc:9090", "sub.svc:9090")
	got := findByCode(NewValidator(pm, ".", clock.Real()).validateTOPO13(), codeTOPO13)
	assert.NotEmpty(t, got,
		"both-remote pub/sub at different endpoints are different processes → TOPO-13 must fire")
}

// TestTOPO13_BothRemote_SameEndpoint_NotFired: two remote cells at the SAME
// endpoint are deployed together (same process), so in-memory delivery between
// them is fine — no broker required, no finding.
func TestTOPO13_BothRemote_SameEndpoint_NotFired(t *testing.T) {
	pm := topo13BothRemoteProject("colo.svc:9090", "colo.svc:9090")
	got := findByCode(NewValidator(pm, ".", clock.Real()).validateTOPO13(), codeTOPO13)
	assert.Empty(t, got,
		"both-remote pub/sub at the SAME endpoint share a process → TOPO-13 must NOT fire")
}

// TestTOPO13_DraftEvent_Skipped: a cross-process event whose contract is not
// active (draft/deprecated) is not served, so it carries no live broker
// requirement — TOPO-13 skips it (active-only scope, #2188 review F4).
func TestTOPO13_DraftEvent_Skipped(t *testing.T) {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			pub: topoTestCell(pub),
			sub: topoTestCell(sub),
		},
		Slices: map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"event.data.v1": func() *metadata.ContractMeta {
				c := topoTestContract("event.data.v1", "event", pub)
				c.Lifecycle = "draft" // not active → not served
				c.Endpoints.Subscribers = []string{sub}
				return c
			}(),
		},
		Journeys: map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{pub, sub}, metadata.TopologyMeta{
				Colocated: []string{pub}, // pub local, sub remote → cross-process
				Remote:    []metadata.TopologyRemoteEntry{{CellID: sub, Endpoint: "sub.svc:9090"}},
			}),
		},
	}
	got := findByCode(NewValidator(pm, ".", clock.Real()).validateTOPO13(), codeTOPO13)
	assert.Empty(t, got, "draft (non-active) cross-process event must be skipped by TOPO-13")
}

// TestTOPO13_AntiVacuity: explicit RED/GREEN fixture pair confirming the
// detector is not vacuously passing.
// RED: cross-process split → ≥1 TOPO-13 finding.
// GREEN: same cells, but both colocated → 0 TOPO-13 findings.
func TestTOPO13_AntiVacuity(t *testing.T) {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB

	// RED: publisher colocated, subscriber remote.
	pmRed := buildTOPO13Project(
		pub, sub, []string{pub, sub},
		metadata.TopologyMeta{
			Colocated: []string{pub},
			Remote:    []metadata.TopologyRemoteEntry{{CellID: sub, Endpoint: "remote.svc:9090"}},
		},
	)
	valRed := NewValidator(pmRed, ".", clock.Real())
	redGot := findByCode(valRed.validateTOPO13(), codeTOPO13)
	require.NotEmpty(t, redGot,
		"anti-vacuity RED: cross-process event pub/sub must produce ≥1 TOPO-13 finding")

	// GREEN: both in colocated.
	pmGreen := buildTOPO13Project(
		pub, sub, []string{pub, sub},
		metadata.TopologyMeta{
			Colocated: []string{pub, sub},
		},
	)
	valGreen := NewValidator(pmGreen, ".", clock.Real())
	greenGot := findByCode(valGreen.validateTOPO13(), codeTOPO13)
	assert.Empty(t, greenGot,
		"anti-vacuity GREEN: co-located pub/sub must produce 0 TOPO-13 findings")
}
