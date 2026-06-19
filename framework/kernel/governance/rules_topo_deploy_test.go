package governance

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
)

// TestBrokerBackedSplit_NoStaticBrokerGate is the #2196 blind-spot ② green: a
// split topology with a cross-process active amqp event no longer trips a static
// broker gate. The old static TOPO-13 rule fired here (over-constraining legal
// broker-backed splits because a static rule cannot see the runtime-injected
// broker); it has been removed, leaving the bootstrap runtime gate
// (validateSplitTopologyBroker, keyed off the codegen-derived
// RequiresBrokerForCrossProcessEvents signal) as the sole broker-mandatory
// enforcement point.
func TestBrokerBackedSplit_NoStaticBrokerGate(t *testing.T) {
	pub := metadatatest.CellIDCellA
	sub := metadatatest.CellIDCellB
	contract := topoTestContract("event.data.v1", "event", pub)
	contract.Transports = []string{"amqp"}
	contract.Endpoints.Subscribers = []string{sub}
	pm := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{pub: topoTestCell(pub), sub: topoTestCell(sub)},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{"event.data.v1": contract},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{pub, sub}, metadata.TopologyMeta{Groups: []metadata.TopologyGroup{
				{Role: "core", Cells: []string{pub}, Endpoint: "core.svc:9090"},
				{Role: "edge", Cells: []string{sub}, Endpoint: "edge.svc:9090"},
			}}),
		},
	}
	results, err := NewValidator(pm, ".", clock.Real()).ValidateStrict(context.Background(), false, false)
	require.NoError(t, err)
	assert.Empty(t, findByCode(results, RuleCode("TOPO-13")),
		"broker-backed split must not trip a static broker gate (TOPO-13 removed, #2196)")
}

// --- helpers ---

// topoTestCell builds a minimal CellMeta with L1 consistency for topology tests.
func topoTestCell(id string) *metadata.CellMeta {
	c := &metadata.CellMeta{
		Type:             "core",
		ConsistencyLevel: "L1",
		Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
		Schema:           metadata.SchemaMeta{Primary: "cell_" + id},
		Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke." + id + ".startup"}},
		Dir:              id,
		File:             "cells/" + id + "/cell.yaml",
	}
	c.ID = id
	return c
}

// topoTestAssembly builds a minimal AssemblyMeta with id "testasm".
func topoTestAssembly(cellIDs []string, topo metadata.TopologyMeta) *metadata.AssemblyMeta {
	refs := metadata.CellRefs(cellIDs...)
	return &metadata.AssemblyMeta{
		ID:                  "testasm",
		Cells:               refs,
		MaxConsistencyLevel: "L1",
		Topology:            topo,
		Owner:               metadata.OwnerMeta{Team: "platform", Role: "assembly-owner"},
		Dir:                 "testasm",
		File:                "assemblies/testasm/assembly.yaml",
	}
}

// topoTestContract builds a minimal ContractMeta with a cell as ownerCell and
// sets the kind-appropriate Endpoints field so contractProvider(c) / ProviderEndpoint()
// returns the ownerCell ID. Without this, TOPO-11 would skip the contract because
// ProviderEndpoint() reads Endpoints.Publisher/Server/etc., not OwnerCell.
func topoTestContract(id, kind, ownerCell string) *metadata.ContractMeta {
	c := &metadata.ContractMeta{
		ID:               id,
		Kind:             kind,
		Lifecycle:        "active",
		ConsistencyLevel: "L1",
		File:             "contracts/" + kind + "/" + id + "/contract.yaml",
	}
	c.OwnerCell = ownerCell
	// Set the kind-appropriate provider endpoint so contractProvider(c) returns ownerCell.
	switch kind {
	case "http", "grpc", "saga":
		c.Endpoints.Server = ownerCell
	case "event":
		c.Endpoints.Publisher = ownerCell
	case "command":
		c.Endpoints.Handler = ownerCell
	case "projection":
		c.Endpoints.Provider = ownerCell
	case "webhook":
		// webhook: ProviderEndpoint returns OwnerCell directly — already set above.
	}
	return c
}

// topoTestSlice builds a minimal SliceMeta for topology tests.
func topoTestSlice(id, belongsToCell string, usages []metadata.ContractUsage) *metadata.SliceMeta {
	s := &metadata.SliceMeta{
		ID:             id,
		ContractUsages: usages,
		Verify: metadata.SliceVerifyMeta{
			Unit:     []string{"unit." + id + ".service"},
			Contract: []string{},
		},
		AllowedFiles: []string{"cells/" + belongsToCell + "/slices/" + id + "/**"},
		Dir:          id,
		CellDir:      belongsToCell,
		File:         "cells/" + belongsToCell + "/slices/" + id + "/slice.yaml",
	}
	s.BelongsToCell = belongsToCell
	return s
}

// =============================================================================
// TOPO-10: topology structural validation (delegates to ValidateTopologyStructure)
// =============================================================================

// TestTOPO10_EmptyTopology: empty topology is always valid → 0 findings.
func TestTOPO10_EmptyTopology(t *testing.T) {
	cellA := metadatatest.CellIDCellA
	pm := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{metadatatest.CellIDCellA: topoTestCell(cellA)},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{cellA}, metadata.TopologyMeta{}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO10(), codeTOPO10)
	assert.Empty(t, got, "empty topology should produce 0 TOPO-10 findings")
}

// TestTOPO10_ValidExhaustiveTopology: valid exhaustive topology → 0 findings.
func TestTOPO10_ValidExhaustiveTopology(t *testing.T) {
	cellA := metadatatest.CellIDCellA
	cellB := metadatatest.CellIDCellB
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDCellA: topoTestCell(cellA),
			metadatatest.CellIDCellB: topoTestCell(cellB),
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{cellA, cellB}, metadata.TopologyMeta{
				Groups: []metadata.TopologyGroup{
					{Role: "core", Cells: []string{cellA}, Endpoint: "core.svc:9090"},
					{Role: "edge", Cells: []string{cellB}, Endpoint: "remote.svc:9090"},
				},
			}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO10(), codeTOPO10)
	assert.Empty(t, got, "valid exhaustive topology should produce 0 TOPO-10 findings")
}

// TestTOPO10_MutualExclusionViolation: cell assigned to two groups → TOPO-10 fires.
func TestTOPO10_MutualExclusionViolation(t *testing.T) {
	cellA := metadatatest.CellIDCellA
	pm := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{metadatatest.CellIDCellA: topoTestCell(cellA)},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{cellA}, metadata.TopologyMeta{
				Groups: []metadata.TopologyGroup{
					{Role: "core", Cells: []string{cellA}, Endpoint: "core.svc:9090"},
					{Role: "edge", Cells: []string{cellA}, Endpoint: "edge.svc:9090"},
				},
			}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO10(), codeTOPO10)
	require.Len(t, got, 1, "mutual exclusion violation should produce 1 TOPO-10 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueInvalid, got[0].IssueType)
	// The anchor must point at the precise duplicate cell within groups, not a
	// coarse "topology" — the second group's cell entry that re-declares cellA.
	assert.Equal(t, "topology.groups[1].cells[0]", got[0].Field,
		"duplicate-cell finding must anchor at the re-declaring group's cell index")
	assert.NotEmpty(t, got[0].Fix)
}

// TestTOPO10_NonExhaustiveTopology: topology that doesn't cover all cells → TOPO-10 fires.
func TestTOPO10_NonExhaustiveTopology(t *testing.T) {
	cellA := metadatatest.CellIDCellA
	cellB := metadatatest.CellIDCellB
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDCellA: topoTestCell(cellA),
			metadatatest.CellIDCellB: topoTestCell(cellB),
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			// group only lists cellA, missing cellB → non-exhaustive
			"testasm": topoTestAssembly([]string{cellA, cellB}, metadata.TopologyMeta{
				Groups: []metadata.TopologyGroup{
					{Role: "core", Cells: []string{cellA}, Endpoint: "core.svc:9090"},
				},
			}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO10(), codeTOPO10)
	require.Len(t, got, 1, "non-exhaustive topology should produce 1 TOPO-10 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueInvalid, got[0].IssueType)
	// Exhaustiveness is a whole-section invariant (no single group owns the gap),
	// so the anchor is the "topology" section, not a per-group index.
	assert.Equal(t, "topology", got[0].Field,
		"non-exhaustive finding must anchor at the topology section")
	assert.NotEmpty(t, got[0].Fix)
}

// TestTOPO10_MalformedEndpoint: remote entry with invalid endpoint → TOPO-10 fires.
func TestTOPO10_MalformedEndpoint(t *testing.T) {
	cellA := metadatatest.CellIDCellA
	cellB := metadatatest.CellIDCellB
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDCellA: topoTestCell(cellA),
			metadatatest.CellIDCellB: topoTestCell(cellB),
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{cellA, cellB}, metadata.TopologyMeta{
				Groups: []metadata.TopologyGroup{
					{Role: "core", Cells: []string{cellA}, Endpoint: "core.svc:9090"},
					// malformed endpoint — not a valid host:port or URL
					{Role: "edge", Cells: []string{cellB}, Endpoint: "not-a-valid-endpoint-!!!"},
				},
			}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO10(), codeTOPO10)
	require.Len(t, got, 1, "malformed endpoint should produce 1 TOPO-10 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueInvalid, got[0].IssueType)
	// The anchor must point at the offending group's endpoint field, not a coarse
	// "topology" — the second group carries the malformed endpoint.
	assert.Equal(t, "topology.groups[1].endpoint", got[0].Field,
		"malformed-endpoint finding must anchor at the offending group's endpoint")
	assert.NotEmpty(t, got[0].Fix)
}

// =============================================================================
// TOPO-11: contract provider reachability per assembly
// =============================================================================

// buildTOPO11Project builds a ProjectMeta for TOPO-11 tests.
// consumerCell is in the assembly; providerCell may or may not be.
// The assembly topology decides placement.
func buildTOPO11Project(
	consumerCellID, providerCellID string,
	asmCellIDs []string,
	topo metadata.TopologyMeta,
) *metadata.ProjectMeta {
	cells := map[string]*metadata.CellMeta{}
	cells[consumerCellID] = topoTestCell(consumerCellID)
	if providerCellID != "" && providerCellID != consumerCellID {
		cells[providerCellID] = topoTestCell(providerCellID)
	}

	sliceKey := consumerCellID + "/consumeslice"
	slices := map[string]*metadata.SliceMeta{
		sliceKey: topoTestSlice("consumeslice", consumerCellID, []metadata.ContractUsage{
			{Contract: "event.data.v1", Role: "subscribe"},
		}),
	}

	contracts := map[string]*metadata.ContractMeta{}
	if providerCellID != "" {
		contracts["event.data.v1"] = topoTestContract("event.data.v1", "event", providerCellID)
	}

	return &metadata.ProjectMeta{
		Cells:     cells,
		Slices:    slices,
		Contracts: contracts,
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly(asmCellIDs, topo),
		},
	}
}

// TestTOPO11_ProviderMissing_EmptyTopology: consumer cell in assembly consumes a
// contract whose provider cell is NOT in the assembly (empty topology → Missing)
// → TOPO-11 fires.
func TestTOPO11_ProviderMissing_EmptyTopology(t *testing.T) {
	consumer := metadatatest.CellIDConsumerCell
	provider := metadatatest.CellIDProviderCell

	// assembly only has consumer; provider is NOT in assembly
	pm := buildTOPO11Project(consumer, provider, []string{consumer}, metadata.TopologyMeta{})
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO11(), codeTOPO11)
	require.Len(t, got, 1, "provider missing from assembly should produce 1 TOPO-11 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueRefNotFound, got[0].IssueType)
	assert.NotEmpty(t, got[0].Fix)
}

// TestTOPO11_ProviderMissing_SplitTopology: assembly topology partitions consumer
// and a bystander into two groups, but provider is not a member of the assembly →
// TOPO-11 fires (provider ∉ assembly cells).
func TestTOPO11_ProviderMissing_SplitTopology(t *testing.T) {
	consumer := metadatatest.CellIDConsumerCell
	provider := metadatatest.CellIDProviderCell
	bystander := metadatatest.NewCellID("bystandercell")

	cells := map[string]*metadata.CellMeta{
		metadatatest.CellIDConsumerCell:         topoTestCell(consumer),
		metadatatest.CellIDProviderCell:         topoTestCell(provider),
		metadatatest.NewCellID("bystandercell"): topoTestCell(bystander),
	}
	sliceKey := consumer + "/consumeslice"
	slices := map[string]*metadata.SliceMeta{
		sliceKey: topoTestSlice("consumeslice", consumer, []metadata.ContractUsage{
			{Contract: "event.data.v1", Role: "subscribe"},
		}),
	}
	contracts := map[string]*metadata.ContractMeta{
		"event.data.v1": topoTestContract("event.data.v1", "event", provider),
	}
	// Assembly topology partitions {consumer, bystander} into two groups;
	// provider is not a member of the assembly → TOPO-11 fires.
	pm := &metadata.ProjectMeta{
		Cells:     cells,
		Slices:    slices,
		Contracts: contracts,
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{consumer, bystander}, metadata.TopologyMeta{
				Groups: []metadata.TopologyGroup{
					{Role: "core", Cells: []string{consumer}, Endpoint: "core.svc:9090"},
					{Role: "edge", Cells: []string{bystander}, Endpoint: "host:9090"},
				},
			}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO11(), codeTOPO11)
	require.Len(t, got, 1, "provider missing from topology should produce 1 TOPO-11 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueRefNotFound, got[0].IssueType)
	assert.NotEmpty(t, got[0].Fix)
}

// TestTOPO11_ProviderColocated: provider cell is in the assembly (empty topology
// → Local) → no TOPO-11 error.
func TestTOPO11_ProviderColocated(t *testing.T) {
	consumer := metadatatest.CellIDConsumerCell
	provider := metadatatest.CellIDProviderCell

	// both in assembly, empty topology → both are Local
	pm := buildTOPO11Project(consumer, provider, []string{consumer, provider}, metadata.TopologyMeta{})
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO11(), codeTOPO11)
	assert.Empty(t, got, "co-located provider should produce 0 TOPO-11 findings")
}

// TestTOPO11_ProviderRemote: provider cell is in a remote group of the assembly
// → reachable → no TOPO-11 error.
func TestTOPO11_ProviderRemote(t *testing.T) {
	consumer := metadatatest.CellIDConsumerCell
	provider := metadatatest.CellIDProviderCell

	pm := buildTOPO11Project(consumer, provider, []string{consumer, provider},
		metadata.TopologyMeta{
			Groups: []metadata.TopologyGroup{
				{Role: "core", Cells: []string{consumer}, Endpoint: "core.svc:9090"},
				{Role: "edge", Cells: []string{provider}, Endpoint: "remote.svc:9090"},
			},
		})
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO11(), codeTOPO11)
	assert.Empty(t, got, "remote-group provider should produce 0 TOPO-11 findings")
}

// TestTOPO11_FrameworkOwnedProvider: framework-owned contract (ownerCell == _framework)
// → skipped (c.Owner().Cell() returns ok=false).
func TestTOPO11_FrameworkOwnedProvider(t *testing.T) {
	consumer := metadatatest.CellIDConsumerCell
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDConsumerCell: topoTestCell(consumer),
		},
		Slices: map[string]*metadata.SliceMeta{
			consumer + "/consumeslice": topoTestSlice("consumeslice", consumer,
				[]metadata.ContractUsage{{Contract: "event.framework.v1", Role: "subscribe"}}),
		},
		Contracts: map[string]*metadata.ContractMeta{
			"event.framework.v1": {
				ID:               "event.framework.v1",
				Kind:             "event",
				Lifecycle:        "draft",
				ConsistencyLevel: "L1",
				OwnerCell:        metadata.FrameworkOwnerSentinel,
				File:             "contracts/event/framework/v1/contract.yaml",
			},
		},
		Journeys: map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{consumer}, metadata.TopologyMeta{}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO11(), codeTOPO11)
	assert.Empty(t, got, "framework-owned contract provider should be skipped by TOPO-11")
}

// TestTOPO11_ExternalActorProvider: provider is an external actor (not a cell) → skipped.
func TestTOPO11_ExternalActorProvider(t *testing.T) {
	consumer := metadatatest.CellIDConsumerCell
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDConsumerCell: topoTestCell(consumer),
		},
		Slices: map[string]*metadata.SliceMeta{
			consumer + "/consumeslice": topoTestSlice("consumeslice", consumer,
				[]metadata.ContractUsage{{Contract: "event.external.v1", Role: "subscribe"}}),
		},
		Contracts: map[string]*metadata.ContractMeta{
			"event.external.v1": func() *metadata.ContractMeta {
				c := &metadata.ContractMeta{
					ID:               "event.external.v1",
					Kind:             "event",
					Lifecycle:        "active",
					ConsistencyLevel: "L1",
					File:             "contracts/event/external/v1/contract.yaml",
				}
				c.OwnerCell = metadatatest.NewCellID("externalactor") // actor ID, not a cell
				return c
			}(),
		},
		Journeys: map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{consumer}, metadata.TopologyMeta{}),
		},
		Actors: []metadata.ActorMeta{
			{ID: "externalactor"},
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO11(), codeTOPO11)
	assert.Empty(t, got, "external actor provider should be skipped by TOPO-11")
}

// TestTOPO11_MissingContract: contractUsage references a non-existent contract
// → skipped (REF-02 owns it).
func TestTOPO11_MissingContract(t *testing.T) {
	consumer := metadatatest.CellIDConsumerCell
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDConsumerCell: topoTestCell(consumer),
		},
		Slices: map[string]*metadata.SliceMeta{
			consumer + "/consumeslice": topoTestSlice("consumeslice", consumer,
				[]metadata.ContractUsage{{Contract: "event.nonexistent.v1", Role: "subscribe"}}),
		},
		Contracts: map[string]*metadata.ContractMeta{}, // no contract registered
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{consumer}, metadata.TopologyMeta{}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO11(), codeTOPO11)
	assert.Empty(t, got, "missing contract should be skipped by TOPO-11 (REF-02 owns it)")
}

// TestTOPO11_ProviderRole_NotConsumer: slice with provider role is skipped
// (only consumer roles trigger TOPO-11).
func TestTOPO11_ProviderRole_NotConsumer(t *testing.T) {
	providerCell := metadatatest.CellIDProviderCell
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDProviderCell: topoTestCell(providerCell),
		},
		Slices: map[string]*metadata.SliceMeta{
			providerCell + "/provideslice": topoTestSlice("provideslice", providerCell,
				[]metadata.ContractUsage{{Contract: "event.data.v1", Role: "publish"}}),
		},
		Contracts: map[string]*metadata.ContractMeta{
			"event.data.v1": topoTestContract("event.data.v1", "event", providerCell),
		},
		Journeys: map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{providerCell}, metadata.TopologyMeta{}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO11(), codeTOPO11)
	assert.Empty(t, got, "provider-role usage should not trigger TOPO-11")
}

// TestTOPO10_AntiVacuity verifies that TOPO-10 is not vacuously passing (F6).
// A project with a mutual-exclusion violation (red) must produce ≥1 TOPO-10
// findings; the same project with the violation removed (valid exhaustive)
// must produce 0 TOPO-10 findings.
func TestTOPO10_AntiVacuity(t *testing.T) {
	cellA := metadatatest.CellIDCellA

	// Red: cell assigned to two groups → TOPO-10 must fire.
	pmRed := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{metadatatest.CellIDCellA: topoTestCell(cellA)},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{cellA}, metadata.TopologyMeta{
				Groups: []metadata.TopologyGroup{
					{Role: "core", Cells: []string{cellA}, Endpoint: "core.svc:9090"},
					{Role: "edge", Cells: []string{cellA}, Endpoint: "edge.svc:9090"},
				},
			}),
		},
	}
	valRed := NewValidator(pmRed, ".", clock.Real())
	redGot := findByCode(valRed.validateTOPO10(), codeTOPO10)
	require.NotEmpty(t, redGot, "anti-vacuity: red fixture (cell in two groups) must produce ≥1 TOPO-10 finding")

	// Green: valid single-group topology → TOPO-10 must NOT fire.
	pmGreen := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{metadatatest.CellIDCellA: topoTestCell(cellA)},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{cellA}, metadata.TopologyMeta{
				Groups: []metadata.TopologyGroup{
					{Role: "core", Cells: []string{cellA}, Endpoint: "core.svc:9090"},
				},
			}),
		},
	}
	valGreen := NewValidator(pmGreen, ".", clock.Real())
	greenGot := findByCode(valGreen.validateTOPO10(), codeTOPO10)
	assert.Empty(t, greenGot, "anti-vacuity: green fixture (valid exhaustive) must produce 0 TOPO-10 findings")
}

// TestTOPO11_HTTPKindCallConsumer confirms that TOPO-11 also fires for http-kind
// contracts consumed via role "call" (not just event/subscribe), verifying
// consumer-role detection is not event-only (F11).
func TestTOPO11_HTTPKindCallConsumer(t *testing.T) {
	consumer := metadatatest.CellIDConsumerCell
	provider := metadatatest.CellIDProviderCell

	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDConsumerCell: topoTestCell(consumer),
			metadatatest.CellIDProviderCell: topoTestCell(provider),
		},
		Slices: map[string]*metadata.SliceMeta{
			consumer + "/callslice": topoTestSlice("callslice", consumer, []metadata.ContractUsage{
				{Contract: "http.data.v1", Role: "call"},
			}),
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.data.v1": topoTestContract("http.data.v1", "http", provider),
		},
		Journeys: map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			// Assembly only has the consumer cell; provider is absent → TOPO-11 must fire.
			"testasm": topoTestAssembly([]string{consumer}, metadata.TopologyMeta{}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO11(), codeTOPO11)
	require.Len(t, got, 1, "http-kind call consumer with missing provider must produce 1 TOPO-11 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueRefNotFound, got[0].IssueType)
	assert.NotEmpty(t, got[0].Fix)
}

// =============================================================================
// TOPO-11 synthetic-red (F3): owner ≠ endpoint-provider
// =============================================================================

// TestTOPO11_OwnerDiffersFromProvider: provider via ProviderEndpoint differs
// from ownerCell. TOPO-11 must fire on the PROVIDER (endpoint), not the owner,
// proving it uses contractProvider(c) not c.Owner().Cell(). The provider cell
// is NOT in the assembly (→ Missing); the owner cell IS in the assembly (→ Local).
// If TOPO-11 used owner, it would skip this — only checking provider catches it.
func TestTOPO11_OwnerDiffersFromProvider(t *testing.T) {
	consumer := metadatatest.CellIDConsumerCell
	owner := metadatatest.NewCellID("ownercell")      // contract's ownerCell — in assembly
	provider := metadatatest.NewCellID("svcprovider") // contract's endpoints.server — NOT in assembly

	cells := map[string]*metadata.CellMeta{}
	cells[metadatatest.CellIDConsumerCell] = topoTestCell(consumer)
	cells[metadatatest.NewCellID("ownercell")] = topoTestCell(owner)
	cells[metadatatest.NewCellID("svcprovider")] = topoTestCell(provider)

	slices := map[string]*metadata.SliceMeta{
		consumer + "/consumeslice": topoTestSlice("consumeslice", consumer, []metadata.ContractUsage{
			{Contract: "http.svc.v1", Role: "call"},
		}),
	}
	// The contract has ownerCell=owner but endpoints.server=provider (different).
	// When TOPO-11 uses contractProvider (ProviderEndpoint = endpoints.server),
	// it finds "provider" which is NOT in the assembly → Missing → error.
	// If it used c.Owner().Cell(), it would find "owner" which IS in the assembly
	// → Local → no error (the bug F3 fixes).
	svcContract := &metadata.ContractMeta{
		ID:               "http.svc.v1",
		Kind:             "http",
		Lifecycle:        "active",
		ConsistencyLevel: "L1",
		// endpoints.server is the provider endpoint — distinct from ownerCell.
		Endpoints: metadata.EndpointsMeta{},
		File:      "contracts/http/svc/v1/contract.yaml",
	}
	svcContract.OwnerCell = owner
	svcContract.Endpoints.Server = provider
	contracts := map[string]*metadata.ContractMeta{
		"http.svc.v1": svcContract,
	}
	// Assembly contains consumer and owner, but NOT provider.
	pm := &metadata.ProjectMeta{
		Cells:     cells,
		Slices:    slices,
		Contracts: contracts,
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{consumer, owner}, metadata.TopologyMeta{}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO11(), codeTOPO11)
	require.Len(t, got, 1,
		"TOPO-11 must fire on the PROVIDER cell (not the owner) when provider is not in assembly")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueRefNotFound, got[0].IssueType)
	// The finding message must name the provider cell (proving it's not using owner).
	assert.Contains(t, got[0].Message, provider,
		"TOPO-11 finding must name the provider cell (not the owner cell)")
	assert.NotEmpty(t, got[0].Fix)
}

// TestTOPO11_AntiVacuity: verify the rule is not vacuously passing — the same
// project WITH the provider in the assembly produces 0 TOPO-11 errors, but
// WITHOUT it produces ≥ 1. This fixture confirms the happy path is
// structurally distinguishable from the red path.
func TestTOPO11_AntiVacuity(t *testing.T) {
	consumer := metadatatest.CellIDConsumerCell
	provider := metadatatest.CellIDProviderCell

	// Red: provider not in assembly → must fire.
	pmRed := buildTOPO11Project(
		consumer, provider,
		[]string{consumer}, // provider absent
		metadata.TopologyMeta{},
	)
	valRed := NewValidator(pmRed, ".", clock.Real())
	redGot := findByCode(valRed.validateTOPO11(), codeTOPO11)
	require.NotEmpty(t, redGot, "anti-vacuity: red fixture (provider absent) must produce ≥1 TOPO-11 finding")

	// Green: same setup but provider in assembly → must NOT fire.
	pmGreen := buildTOPO11Project(
		consumer, provider,
		[]string{consumer, provider}, // provider present
		metadata.TopologyMeta{},
	)
	valGreen := NewValidator(pmGreen, ".", clock.Real())
	greenGot := findByCode(valGreen.validateTOPO11(), codeTOPO11)
	assert.Empty(t, greenGot, "anti-vacuity: green fixture (provider present) must produce 0 TOPO-11 findings")
}
