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

// twoGroups places pub and sub in separate deployment groups (cross-process).
func twoGroups(pub, sub string) metadata.TopologyMeta {
	return metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
		{Role: "core", Cells: []string{pub}, Endpoint: "core.svc:9090"},
		{Role: "edge", Cells: []string{sub}, Endpoint: "edge.svc:9090"},
	}}
}

// oneGroup places pub and sub in a single deployment group (same process).
func oneGroup(pub, sub string) metadata.TopologyMeta {
	return metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
		{Role: "monolith", Cells: []string{pub, sub}, Endpoint: "mono.svc:9090"},
	}}
}

// =============================================================================
// TOPO-13: broker-mandatory static gate
// =============================================================================

// TestTOPO13_CrossProcessEvent_Split is the primary RED test.
// Publisher and subscriber are in different deployment groups → cross-process
// event pub/sub → TOPO-13 must fire.
//
// Anti-vacuity note: if this test returns 0 findings, the detector is broken.
// TestTOPO13_ColocatedEvent_NoError must also pass (0 findings) to confirm the
// detector is not a false-positive machine.
func TestTOPO13_CrossProcessEvent_Split(t *testing.T) {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB

	pm := buildTOPO13Project(pub, sub, []string{pub, sub}, twoGroups(pub, sub))
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO13(), codeTOPO13)
	require.GreaterOrEqual(t, len(got), 1,
		"split topology with cross-process event pub/sub must produce ≥1 TOPO-13 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueForbidden, got[0].IssueType)
	assert.NotEmpty(t, got[0].Fix)
	assert.NotEmpty(t, got[0].Message)
}

// TestTOPO13_ColocatedEvent_NoError covers the <2-group SKIP path: a single
// group covering pub+sub has no process boundary, so validateTOPO13 skips the
// assembly before the SameGroup predicate is ever evaluated → no finding. The
// ≥2-group GREEN that actually exercises the SameGroup predicate (pub+sub in the
// same group alongside another group) is TestTOPO13_SameGroup_NotFired.
func TestTOPO13_ColocatedEvent_NoError(t *testing.T) {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB

	pm := buildTOPO13Project(pub, sub, []string{pub, sub}, oneGroup(pub, sub))
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO13(), codeTOPO13)
	assert.Empty(t, got,
		"co-located (single-group) pub/sub should produce 0 TOPO-13 findings (anti-vacuity: if RED test "+
			"also returns 0, the detector is broken)")
}

// TestTOPO13_EmptyTopology_NoError: empty topology → all cells co-located (same
// process) → no cross-process boundary → TOPO-13 must NOT fire.
func TestTOPO13_EmptyTopology_NoError(t *testing.T) {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB

	pm := buildTOPO13Project(pub, sub, []string{pub, sub}, metadata.TopologyMeta{})
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO13(), codeTOPO13)
	assert.Empty(t, got, "empty topology (all cells local) should produce 0 TOPO-13 findings")
}

// TestTOPO13_NonSplitAssembly_Skipped: assembly with fewer than 2 groups →
// TOPO-13 skips it entirely (only split assemblies are checked).
func TestTOPO13_NonSplitAssembly_Skipped(t *testing.T) {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB

	// Single group → not a split assembly → TOPO-13 must skip.
	pm := buildTOPO13Project(pub, sub, []string{pub, sub}, oneGroup(pub, sub))
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO13(), codeTOPO13)
	assert.Empty(t, got, "non-split assembly (<2 groups) should produce 0 TOPO-13 findings")
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
			"testasm": topoTestAssembly([]string{pub, sub}, twoGroups(pub, sub)),
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
	bystander := metadatatest.CellIDCellB

	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDCellA: topoTestCell(pub),
			metadatatest.CellIDCellB: topoTestCell(bystander),
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
		// Two groups so the assembly is split, but the subscriber is an actor, not a cell.
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{pub, bystander}, twoGroups(pub, bystander)),
		},
	}
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
			"testasm": topoTestAssembly([]string{sub, bystander}, twoGroups(sub, bystander)),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO13(), codeTOPO13)
	assert.Empty(t, got, "framework-owned event contract should be skipped by TOPO-13")
}

// TestTOPO13_SubscriberRemote_PublisherLocal: publisher and subscriber in
// different groups → cross-process → TOPO-13 must fire (symmetry check).
func TestTOPO13_SubscriberRemote_PublisherLocal(t *testing.T) {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB

	pm := buildTOPO13Project(pub, sub, []string{pub, sub}, twoGroups(pub, sub))
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO13(), codeTOPO13)
	require.GreaterOrEqual(t, len(got), 1,
		"publisher and subscriber in different groups must produce ≥1 TOPO-13 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
}

// TestTOPO13_PublisherRemote_SubscriberLocal: publisher and subscriber in
// different groups (roles swapped) → also cross-process → TOPO-13 must fire.
func TestTOPO13_PublisherRemote_SubscriberLocal(t *testing.T) {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB

	// Swap which group holds which cell — outcome must be symmetric.
	topo := metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
		{Role: "core", Cells: []string{sub}, Endpoint: "core.svc:9090"},
		{Role: "edge", Cells: []string{pub}, Endpoint: "edge.svc:9090"},
	}}
	pm := buildTOPO13Project(pub, sub, []string{pub, sub}, topo)
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO13(), codeTOPO13)
	require.GreaterOrEqual(t, len(got), 1,
		"publisher and subscriber in different groups must produce ≥1 TOPO-13 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
}

// topo13GroupedProject builds a split assembly (≥2 groups) with pub, sub and a
// bystander. When pubSubSameGroup is true, pub and sub share one group (same
// process → no broker needed); otherwise each is in its own group (cross-process).
func topo13GroupedProject(pubSubSameGroup bool) *metadata.ProjectMeta {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB
	bystander := metadatatest.CellIDCellC
	var groups []metadata.TopologyGroup
	if pubSubSameGroup {
		groups = []metadata.TopologyGroup{
			{Role: "g1", Cells: []string{pub, sub}, Endpoint: "g1.svc:9090"},
			{Role: "g2", Cells: []string{bystander}, Endpoint: "g2.svc:9090"},
		}
	} else {
		groups = []metadata.TopologyGroup{
			{Role: "g1", Cells: []string{pub}, Endpoint: "g1.svc:9090"},
			{Role: "g2", Cells: []string{sub}, Endpoint: "g2.svc:9090"},
			{Role: "g3", Cells: []string{bystander}, Endpoint: "g3.svc:9090"},
		}
	}
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDCellA: topoTestCell(pub),
			metadatatest.CellIDCellB: topoTestCell(sub),
			metadatatest.CellIDCellC: topoTestCell(bystander),
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
			"testasm": topoTestAssembly([]string{pub, sub, bystander}, metadata.TopologyMeta{Groups: groups}),
		},
	}
}

// TestTOPO13_DifferentGroups_Fired locks cross-process detection: pub and sub in
// different groups (within a ≥2-group split) require a broker — TOPO-13 fires.
func TestTOPO13_DifferentGroups_Fired(t *testing.T) {
	pm := topo13GroupedProject(false)
	got := findByCode(NewValidator(pm, ".", clock.Real()).validateTOPO13(), codeTOPO13)
	assert.NotEmpty(t, got,
		"pub/sub in different groups are different processes → TOPO-13 must fire")
}

// TestTOPO13_SameGroup_NotFired: pub and sub in the SAME group (within a split
// assembly) share a process → in-memory delivery is fine → no finding. This is
// the ≥2-group anti-vacuity GREEN for the SameGroup predicate (not just the
// <2-group skip).
func TestTOPO13_SameGroup_NotFired(t *testing.T) {
	pm := topo13GroupedProject(true)
	got := findByCode(NewValidator(pm, ".", clock.Real()).validateTOPO13(), codeTOPO13)
	assert.Empty(t, got,
		"pub/sub in the SAME group share a process → TOPO-13 must NOT fire")
}

// TestTOPO13_DraftEvent_Skipped: a cross-process event whose contract is not
// active (draft/deprecated) is not served, so it carries no live broker
// requirement — TOPO-13 skips it (active-only scope, #2188 review F4).
func TestTOPO13_DraftEvent_Skipped(t *testing.T) {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDCellA: topoTestCell(pub),
			metadatatest.CellIDCellB: topoTestCell(sub),
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
			"testasm": topoTestAssembly([]string{pub, sub}, twoGroups(pub, sub)), // cross-process
		},
	}
	got := findByCode(NewValidator(pm, ".", clock.Real()).validateTOPO13(), codeTOPO13)
	assert.Empty(t, got, "draft (non-active) cross-process event must be skipped by TOPO-13")
}

// TestTOPO13_AntiVacuity: explicit RED/GREEN fixture pair confirming the
// detector is not vacuously passing.
// RED: cross-process split → ≥1 TOPO-13 finding.
// GREEN: same cells, but single group → 0 TOPO-13 findings.
func TestTOPO13_AntiVacuity(t *testing.T) {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB

	// RED: pub and sub in different groups.
	pmRed := buildTOPO13Project(pub, sub, []string{pub, sub}, twoGroups(pub, sub))
	valRed := NewValidator(pmRed, ".", clock.Real())
	redGot := findByCode(valRed.validateTOPO13(), codeTOPO13)
	require.NotEmpty(t, redGot,
		"anti-vacuity RED: cross-process event pub/sub must produce ≥1 TOPO-13 finding")

	// GREEN: single group.
	pmGreen := buildTOPO13Project(pub, sub, []string{pub, sub}, oneGroup(pub, sub))
	valGreen := NewValidator(pmGreen, ".", clock.Real())
	greenGot := findByCode(valGreen.validateTOPO13(), codeTOPO13)
	assert.Empty(t, greenGot,
		"anti-vacuity GREEN: single-group pub/sub must produce 0 TOPO-13 findings")
}
