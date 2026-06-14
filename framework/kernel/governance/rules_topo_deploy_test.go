package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
)

// --- helpers ---

// topoTestCell builds a minimal CellMeta with L1 consistency for topology tests.
func topoTestCell(id string) *metadata.CellMeta {
	return &metadata.CellMeta{
		ID:               id,
		Type:             "core",
		ConsistencyLevel: "L1",
		Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
		Schema:           metadata.SchemaMeta{Primary: "cell_" + id},
		Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke." + id + ".startup"}},
		Dir:              id,
		File:             "cells/" + id + "/cell.yaml",
	}
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
		OwnerCell:        ownerCell,
		File:             "contracts/" + kind + "/" + id + "/contract.yaml",
	}
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
	return &metadata.SliceMeta{
		ID:             id,
		BelongsToCell:  belongsToCell,
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
}

// =============================================================================
// TOPO-10: topology structural validation (delegates to ValidateTopologyStructure)
// =============================================================================

// TestTOPO10_EmptyTopology: empty topology is always valid → 0 findings.
func TestTOPO10_EmptyTopology(t *testing.T) {
	cellA := metadatatest.NewCellID("cella")
	pm := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{cellA: topoTestCell(cellA)},
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
	cellA := metadatatest.NewCellID("cella")
	cellB := metadatatest.NewCellID("cellb")
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			cellA: topoTestCell(cellA),
			cellB: topoTestCell(cellB),
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{cellA, cellB}, metadata.TopologyMeta{
				Colocated: []string{cellA},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: cellB, Endpoint: "remote.svc:9090"},
				},
			}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO10(), codeTOPO10)
	assert.Empty(t, got, "valid exhaustive topology should produce 0 TOPO-10 findings")
}

// TestTOPO10_MutualExclusionViolation: cell in both colocated and remote → TOPO-10 fires.
func TestTOPO10_MutualExclusionViolation(t *testing.T) {
	cellA := metadatatest.NewCellID("cella")
	pm := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{cellA: topoTestCell(cellA)},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{cellA}, metadata.TopologyMeta{
				Colocated: []string{cellA},
				Remote:    []metadata.TopologyRemoteEntry{{CellID: cellA, Endpoint: "host:9090"}},
			}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO10(), codeTOPO10)
	require.Len(t, got, 1, "mutual exclusion violation should produce 1 TOPO-10 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueInvalid, got[0].IssueType)
	assert.NotEmpty(t, got[0].Fix)
}

// TestTOPO10_NonExhaustiveTopology: topology that doesn't cover all cells → TOPO-10 fires.
func TestTOPO10_NonExhaustiveTopology(t *testing.T) {
	cellA := metadatatest.NewCellID("cella")
	cellB := metadatatest.NewCellID("cellb")
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			cellA: topoTestCell(cellA),
			cellB: topoTestCell(cellB),
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			// topology only lists cellA, missing cellB → non-exhaustive
			"testasm": topoTestAssembly([]string{cellA, cellB}, metadata.TopologyMeta{
				Colocated: []string{cellA},
			}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO10(), codeTOPO10)
	require.Len(t, got, 1, "non-exhaustive topology should produce 1 TOPO-10 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueInvalid, got[0].IssueType)
	assert.NotEmpty(t, got[0].Fix)
}

// TestTOPO10_MalformedEndpoint: remote entry with invalid endpoint → TOPO-10 fires.
func TestTOPO10_MalformedEndpoint(t *testing.T) {
	cellA := metadatatest.NewCellID("cella")
	cellB := metadatatest.NewCellID("cellb")
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			cellA: topoTestCell(cellA),
			cellB: topoTestCell(cellB),
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{cellA, cellB}, metadata.TopologyMeta{
				Colocated: []string{cellA},
				Remote: []metadata.TopologyRemoteEntry{
					// malformed endpoint — not a valid host:port or URL
					{CellID: cellB, Endpoint: "not-a-valid-endpoint-!!!"},
				},
			}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO10(), codeTOPO10)
	require.Len(t, got, 1, "malformed endpoint should produce 1 TOPO-10 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueInvalid, got[0].IssueType)
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
	cells := map[string]*metadata.CellMeta{
		consumerCellID: topoTestCell(consumerCellID),
	}
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
	consumer := metadatatest.NewCellID("consumercell")
	provider := metadatatest.NewCellID("providercell")

	// assembly only has consumer; provider is NOT in assembly
	pm := buildTOPO11Project(consumer, provider, []string{consumer}, metadata.TopologyMeta{})
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO11(), codeTOPO11)
	require.Len(t, got, 1, "provider missing from assembly should produce 1 TOPO-11 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueRefNotFound, got[0].IssueType)
	assert.NotEmpty(t, got[0].Fix)
}

// TestTOPO11_ProviderMissing_SplitTopology: assembly topology has consumer in
// colocated and a bystander in remote, but provider is not in the assembly →
// ClassifyCell returns Missing → TOPO-11 fires.
func TestTOPO11_ProviderMissing_SplitTopology(t *testing.T) {
	consumer := metadatatest.NewCellID("consumercell")
	provider := metadatatest.NewCellID("providercell")
	bystander := metadatatest.NewCellID("bystandercell")

	cells := map[string]*metadata.CellMeta{
		consumer:  topoTestCell(consumer),
		provider:  topoTestCell(provider),
		bystander: topoTestCell(bystander),
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
	// Assembly topology covers {consumer, bystander} exhaustively;
	// provider is not in the assembly → ClassifyCell returns Missing.
	pm := &metadata.ProjectMeta{
		Cells:     cells,
		Slices:    slices,
		Contracts: contracts,
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{consumer, bystander}, metadata.TopologyMeta{
				Colocated: []string{consumer},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: bystander, Endpoint: "host:9090"},
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
	consumer := metadatatest.NewCellID("consumercell")
	provider := metadatatest.NewCellID("providercell")

	// both in assembly, empty topology → both are Local
	pm := buildTOPO11Project(consumer, provider, []string{consumer, provider}, metadata.TopologyMeta{})
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO11(), codeTOPO11)
	assert.Empty(t, got, "co-located provider should produce 0 TOPO-11 findings")
}

// TestTOPO11_ProviderRemote: provider cell is declared in remote topology → no TOPO-11 error.
func TestTOPO11_ProviderRemote(t *testing.T) {
	consumer := metadatatest.NewCellID("consumercell")
	provider := metadatatest.NewCellID("providercell")

	pm := buildTOPO11Project(consumer, provider, []string{consumer, provider},
		metadata.TopologyMeta{
			Colocated: []string{consumer},
			Remote:    []metadata.TopologyRemoteEntry{{CellID: provider, Endpoint: "remote.svc:9090"}},
		})
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO11(), codeTOPO11)
	assert.Empty(t, got, "remote provider should produce 0 TOPO-11 findings")
}

// TestTOPO11_FrameworkOwnedProvider: framework-owned contract (ownerCell == _framework)
// → skipped (c.Owner().Cell() returns ok=false).
func TestTOPO11_FrameworkOwnedProvider(t *testing.T) {
	consumer := metadatatest.NewCellID("consumercell")
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			consumer: topoTestCell(consumer),
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
	consumer := metadatatest.NewCellID("consumercell")
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			consumer: topoTestCell(consumer),
		},
		Slices: map[string]*metadata.SliceMeta{
			consumer + "/consumeslice": topoTestSlice("consumeslice", consumer,
				[]metadata.ContractUsage{{Contract: "event.external.v1", Role: "subscribe"}}),
		},
		Contracts: map[string]*metadata.ContractMeta{
			"event.external.v1": {
				ID:               "event.external.v1",
				Kind:             "event",
				Lifecycle:        "active",
				ConsistencyLevel: "L1",
				OwnerCell:        "externalactor", // actor ID, not a cell
				File:             "contracts/event/external/v1/contract.yaml",
			},
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
	consumer := metadatatest.NewCellID("consumercell")
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			consumer: topoTestCell(consumer),
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
	providerCell := metadatatest.NewCellID("providercell")
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			providerCell: topoTestCell(providerCell),
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
	cellA := metadatatest.NewCellID("cella")

	// Red: mutual-exclusion violation → TOPO-10 must fire.
	pmRed := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{cellA: topoTestCell(cellA)},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{cellA}, metadata.TopologyMeta{
				Colocated: []string{cellA},
				Remote:    []metadata.TopologyRemoteEntry{{CellID: cellA, Endpoint: "host:9090"}},
			}),
		},
	}
	valRed := NewValidator(pmRed, ".", clock.Real())
	redGot := findByCode(valRed.validateTOPO10(), codeTOPO10)
	require.NotEmpty(t, redGot, "anti-vacuity: red fixture (mutual exclusion) must produce ≥1 TOPO-10 finding")

	// Green: valid exhaustive topology → TOPO-10 must NOT fire.
	pmGreen := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{cellA: topoTestCell(cellA)},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{cellA}, metadata.TopologyMeta{
				Colocated: []string{cellA},
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
	consumer := metadatatest.NewCellID("consumercell")
	provider := metadatatest.NewCellID("providercell")

	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			consumer: topoTestCell(consumer),
			provider: topoTestCell(provider),
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
// TOPO-12: topology.remote INTERIM fail-close gate (US4 #1963)
// =============================================================================

// TestTOPO12_RemotePresent_Fires: assembly with non-empty topology.remote →
// TOPO-12 must fire (fail-closed until US4 #1963).
func TestTOPO12_RemotePresent_Fires(t *testing.T) {
	cellA := metadatatest.NewCellID("cella")
	cellB := metadatatest.NewCellID("cellb")
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			cellA: topoTestCell(cellA),
			cellB: topoTestCell(cellB),
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{cellA, cellB}, metadata.TopologyMeta{
				Colocated: []string{cellA},
				Remote: []metadata.TopologyRemoteEntry{
					{CellID: cellB, Endpoint: "remote.svc:9090"},
				},
			}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO12(), codeTOPO12)
	require.Len(t, got, 1, "assembly with topology.remote should produce 1 TOPO-12 finding")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueForbidden, got[0].IssueType)
	assert.NotEmpty(t, got[0].Fix)
	assert.Equal(t, "topology.remote", got[0].Field)
}

// TestTOPO12_ColocatedOnly_NoFire: colocated-only topology → TOPO-12 must NOT fire.
func TestTOPO12_ColocatedOnly_NoFire(t *testing.T) {
	cellA := metadatatest.NewCellID("cella")
	pm := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{cellA: topoTestCell(cellA)},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{cellA}, metadata.TopologyMeta{
				Colocated: []string{cellA},
			}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO12(), codeTOPO12)
	assert.Empty(t, got, "colocated-only topology should produce 0 TOPO-12 findings")
}

// TestTOPO12_EmptyTopology_NoFire: empty topology → TOPO-12 must NOT fire.
func TestTOPO12_EmptyTopology_NoFire(t *testing.T) {
	cellA := metadatatest.NewCellID("cella")
	pm := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{cellA: topoTestCell(cellA)},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{cellA}, metadata.TopologyMeta{}),
		},
	}
	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateTOPO12(), codeTOPO12)
	assert.Empty(t, got, "empty topology should produce 0 TOPO-12 findings")
}

// TestTOPO12_AntiVacuity: the same assembly WITH remote → TOPO-12 fires;
// WITHOUT remote → does not. Confirms the rule is not vacuously passing.
func TestTOPO12_AntiVacuity(t *testing.T) {
	cellA := metadatatest.NewCellID("cella")
	cellB := metadatatest.NewCellID("cellb")

	// Red: has topology.remote → must fire.
	pmRed := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			cellA: topoTestCell(cellA),
			cellB: topoTestCell(cellB),
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{cellA, cellB}, metadata.TopologyMeta{
				Colocated: []string{cellA},
				Remote:    []metadata.TopologyRemoteEntry{{CellID: cellB, Endpoint: "host:9090"}},
			}),
		},
	}
	valRed := NewValidator(pmRed, ".", clock.Real())
	redGot := findByCode(valRed.validateTOPO12(), codeTOPO12)
	require.NotEmpty(t, redGot, "anti-vacuity: assembly with topology.remote must produce ≥1 TOPO-12 finding")

	// Green: colocated-only → must NOT fire.
	pmGreen := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			cellA: topoTestCell(cellA),
			cellB: topoTestCell(cellB),
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"testasm": topoTestAssembly([]string{cellA, cellB}, metadata.TopologyMeta{
				Colocated: []string{cellA, cellB},
			}),
		},
	}
	valGreen := NewValidator(pmGreen, ".", clock.Real())
	greenGot := findByCode(valGreen.validateTOPO12(), codeTOPO12)
	assert.Empty(t, greenGot, "anti-vacuity: colocated-only assembly must produce 0 TOPO-12 findings")
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
	consumer := metadatatest.NewCellID("consumercell")
	owner := metadatatest.NewCellID("ownercell")      // contract's ownerCell — in assembly
	provider := metadatatest.NewCellID("svcprovider") // contract's endpoints.server — NOT in assembly

	cells := map[string]*metadata.CellMeta{
		consumer: topoTestCell(consumer),
		owner:    topoTestCell(owner),
		provider: topoTestCell(provider),
	}
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
	contracts := map[string]*metadata.ContractMeta{
		"http.svc.v1": {
			ID:               "http.svc.v1",
			Kind:             "http",
			Lifecycle:        "active",
			ConsistencyLevel: "L1",
			OwnerCell:        owner,
			// endpoints.server is the provider endpoint — distinct from ownerCell.
			Endpoints: metadata.EndpointsMeta{Server: provider},
			File:      "contracts/http/svc/v1/contract.yaml",
		},
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
	consumer := metadatatest.NewCellID("consumercell")
	provider := metadatatest.NewCellID("providercell")

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
