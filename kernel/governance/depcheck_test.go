package governance

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
)

// depCheck runs all PhaseDep rules (DEP-01, DEP-02, DEP-03) in order and
// returns the combined findings. Wraps the PhaseDep rules directly for
// focused DEP tests.
func depCheck(v *Validator) []ValidationResult {
	var r []ValidationResult
	r = append(r, v.checkDEP01()...)
	r = append(r, v.checkDEP02()...)
	r = append(r, v.checkDEP03()...)
	return r
}

// depCheckFailFast runs PhaseDep rules and stops after the first check that
// produces a SeverityError.
func depCheckFailFast(v *Validator) []ValidationResult {
	var r []ValidationResult
	for _, check := range []func() []ValidationResult{
		v.checkDEP01, v.checkDEP02, v.checkDEP03,
	} {
		findings := check()
		r = append(r, findings...)
		if HasErrors(findings) {
			return r
		}
	}
	return r
}

// --- DEP-01: One-slice-one-cell ---

func TestDEP01(t *testing.T) {
	tests := []struct {
		name      string
		project   *metadata.ProjectMeta
		wantCount int
		wantCode  RuleCode
	}{
		{
			name: "belongsToCell matches key cellID — no error",
			project: &metadata.ProjectMeta{
				Cells: map[string]*metadata.CellMeta{
					metadatatest.CellIDAccessCore: {ID: metadatatest.CellIDAccessCore, ConsistencyLevel: "L1"},
				},
				Slices: map[string]*metadata.SliceMeta{
					"accesscore/session-login": {
						ID:            "session-login",
						BelongsToCell: metadatatest.CellIDAccessCore,
					},
				},
				Contracts:  map[string]*metadata.ContractMeta{},
				Assemblies: map[string]*metadata.AssemblyMeta{},
			},
			wantCount: 0,
		},
		{
			name: "belongsToCell mismatches key cellID — error",
			project: &metadata.ProjectMeta{
				Cells: map[string]*metadata.CellMeta{
					metadatatest.CellIDAccessCore: {ID: metadatatest.CellIDAccessCore, ConsistencyLevel: "L1"},
					metadatatest.CellIDAuditCore:  {ID: metadatatest.CellIDAuditCore, ConsistencyLevel: "L1"},
				},
				Slices: map[string]*metadata.SliceMeta{
					"accesscore/session-login": {
						ID:            "session-login",
						BelongsToCell: metadatatest.CellIDAuditCore, // mismatch!
					},
				},
				Contracts:  map[string]*metadata.ContractMeta{},
				Assemblies: map[string]*metadata.AssemblyMeta{},
			},
			wantCount: 1,
			wantCode:  "DEP-01",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dc := NewValidator(tt.project, "", clock.Real())
			results := depCheck(dc)
			dep01 := findByCode(results, codeDEP01)
			assert.Len(t, dep01, tt.wantCount)
			if tt.wantCount > 0 {
				assert.Equal(t, tt.wantCode, dep01[0].Code)
				assert.Equal(t, SeverityError, dep01[0].Severity)
				assert.Equal(t, IssueMismatch, dep01[0].IssueType)
			}
		})
	}
}

// --- DEP-02: No circular dependencies ---

func TestDEP02_CycleDetected(t *testing.T) {
	// A→B→A cycle via contracts:
	// cell-a has slice with provider role on contract-ab, consumer is cell-b
	// cell-b has slice with provider role on contract-ba, consumer is cell-a
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDCellA: {ID: metadatatest.CellIDCellA, ConsistencyLevel: "L2"},
			metadatatest.CellIDCellB: {ID: metadatatest.CellIDCellB, ConsistencyLevel: "L2"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"cella/slice-a": {
				ID:            "slice-a",
				BelongsToCell: metadatatest.CellIDCellA,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.a-to-b.v1", Role: "serve"},
				},
			},
			"cellb/slice-b": {
				ID:            "slice-b",
				BelongsToCell: metadatatest.CellIDCellB,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.b-to-a.v1", Role: "serve"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.a-to-b.v1": {
				ID:        "http.a-to-b.v1",
				Kind:      "http",
				OwnerCell: metadatatest.CellIDCellA,
				Endpoints: metadata.EndpointsMeta{
					Server:  metadatatest.CellIDCellA,
					Clients: []string{metadatatest.CellIDCellB},
				},
			},
			"http.b-to-a.v1": {
				ID:        "http.b-to-a.v1",
				Kind:      "http",
				OwnerCell: metadatatest.CellIDCellB,
				Endpoints: metadata.EndpointsMeta{
					Server:  metadatatest.CellIDCellB,
					Clients: []string{metadatatest.CellIDCellA},
				},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewValidator(project, "", clock.Real())
	results := depCheck(dc)
	dep02 := findByCode(results, "DEP-02")
	require.Len(t, dep02, 1, "expected exactly 1 cycle error")
	assert.Equal(t, SeverityError, dep02[0].Severity)
	assert.Contains(t, dep02[0].Message, "circular dependency detected")
}

func TestDEP02_NoCycle(t *testing.T) {
	// A→B (no cycle): cell-a provides, cell-b consumes
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDCellA: {ID: metadatatest.CellIDCellA, ConsistencyLevel: "L2"},
			metadatatest.CellIDCellB: {ID: metadatatest.CellIDCellB, ConsistencyLevel: "L2"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"cella/slice-a": {
				ID:            "slice-a",
				BelongsToCell: metadatatest.CellIDCellA,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.a-to-b.v1", Role: "serve"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.a-to-b.v1": {
				ID:        "http.a-to-b.v1",
				Kind:      "http",
				OwnerCell: metadatatest.CellIDCellA,
				Endpoints: metadata.EndpointsMeta{
					Server:  metadatatest.CellIDCellA,
					Clients: []string{metadatatest.CellIDCellB},
				},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewValidator(project, "", clock.Real())
	results := depCheck(dc)
	dep02 := findByCode(results, "DEP-02")
	assert.Empty(t, dep02, "acyclic graph should produce no DEP-02 errors")
}

func TestDEP02_SingleCellNoExternalDeps(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.NewCellID("lonely"): {ID: metadatatest.NewCellID("lonely"), ConsistencyLevel: "L1"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"lonely/only-slice": {
				ID:            "only-slice",
				BelongsToCell: metadatatest.NewCellID("lonely"),
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewValidator(project, "", clock.Real())
	results := depCheck(dc)
	dep02 := findByCode(results, "DEP-02")
	assert.Empty(t, dep02, "single cell with no deps should produce no DEP-02 errors")
}

func TestDEP02_UnknownKindWarning(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDCellA: {ID: metadatatest.CellIDCellA, ConsistencyLevel: "L2"},
			metadatatest.CellIDCellB: {ID: metadatatest.CellIDCellB, ConsistencyLevel: "L2"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"cella/slice-x": {
				ID:            "slice-x",
				BelongsToCell: metadatatest.CellIDCellA,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "bad.kind.v1", Role: "serve"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"bad.kind.v1": {
				ID:   "bad.kind.v1",
				Kind: "websocket", // unknown kind
				Endpoints: metadata.EndpointsMeta{
					Server: metadatatest.CellIDCellA,
				},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewValidator(project, "", clock.Real())
	results := depCheck(dc)
	dep02 := findByCode(results, "DEP-02")
	require.Len(t, dep02, 1)
	assert.Equal(t, SeverityError, dep02[0].Severity)
	assert.Equal(t, IssueInvalid, dep02[0].IssueType)
	assert.Contains(t, dep02[0].Message, "bad.kind.v1")
	assert.Contains(t, dep02[0].Message, "dependency graph may be incomplete")
}

// --- DEP-03: L0 dependencies in same assembly ---

func TestDEP03_SameAssembly(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDAppCore: {
				ID:               metadatatest.CellIDAppCore,
				ConsistencyLevel: "L2",
				L0Dependencies: []metadata.L0DepMeta{
					{Cell: metadatatest.CellIDSharedCrypto, Reason: "hashing"},
				},
			},
			metadatatest.CellIDSharedCrypto: {
				ID:               metadatatest.CellIDSharedCrypto,
				ConsistencyLevel: "L0",
			},
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"main-bundle": {
				ID:    "main-bundle",
				Cells: []metadata.AssemblyCellRef{{ID: metadatatest.CellIDAppCore}, {ID: metadatatest.CellIDSharedCrypto}},
			},
		},
	}

	dc := NewValidator(project, "", clock.Real())
	results := depCheck(dc)
	dep03 := findByCode(results, "DEP-03")
	assert.Empty(t, dep03, "L0 dep in same assembly should produce no DEP-03 errors")
}

func TestDEP03_DifferentAssembly(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDAppCore: {
				ID:               metadatatest.CellIDAppCore,
				ConsistencyLevel: "L2",
				L0Dependencies: []metadata.L0DepMeta{
					{Cell: metadatatest.CellIDSharedCrypto, Reason: "hashing"},
				},
			},
			metadatatest.CellIDSharedCrypto: {
				ID:               metadatatest.CellIDSharedCrypto,
				ConsistencyLevel: "L0",
			},
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"bundle-a": {
				ID:    "bundle-a",
				Cells: []metadata.AssemblyCellRef{{ID: metadatatest.CellIDAppCore}},
			},
			"bundle-b": {
				ID:    "bundle-b",
				Cells: []metadata.AssemblyCellRef{{ID: metadatatest.CellIDSharedCrypto}},
			},
		},
	}

	dc := NewValidator(project, "", clock.Real())
	results := depCheck(dc)
	dep03 := findByCode(results, "DEP-03")
	require.Len(t, dep03, 1, "L0 dep in different assembly should produce 1 DEP-03 error")
	assert.Equal(t, SeverityError, dep03[0].Severity)
	assert.Contains(t, dep03[0].Message, "same assembly")
}

func TestDEP03_NoAssemblies(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDAppCore: {
				ID:               metadatatest.CellIDAppCore,
				ConsistencyLevel: "L2",
				L0Dependencies: []metadata.L0DepMeta{
					{Cell: metadatatest.CellIDSharedCrypto, Reason: "hashing"},
				},
			},
			metadatatest.CellIDSharedCrypto: {
				ID:               metadatatest.CellIDSharedCrypto,
				ConsistencyLevel: "L0",
			},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewValidator(project, "", clock.Real())
	results := depCheck(dc)
	dep03 := findByCode(results, "DEP-03")
	assert.Empty(t, dep03, "no assemblies should skip DEP-03 check")
}

// --- CheckFailFast ---

// TestCheckFailFast_StopsOnFirstError verifies CheckFailFast returns as soon
// as a SeverityError check fires without running subsequent checks.
func TestCheckFailFast_StopsOnFirstError(t *testing.T) {
	// DEP-01 will fire (mismatched belongsToCell), which should prevent DEP-02
	// from running far enough to produce its own error.
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDAccessCore: {ID: metadatatest.CellIDAccessCore, ConsistencyLevel: "L1"},
			metadatatest.CellIDAuditCore:  {ID: metadatatest.CellIDAuditCore, ConsistencyLevel: "L1"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/session-login": {
				ID:            "session-login",
				BelongsToCell: metadatatest.CellIDAuditCore, // mismatch triggers DEP-01 error
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewValidator(project, "", clock.Real())
	results := depCheckFailFast(dc)

	dep01 := findByCode(results, "DEP-01")
	require.NotEmpty(t, dep01, "DEP-01 must fire on belongsToCell mismatch")
	// Since DEP-01 produced an error, CheckFailFast should have returned early.
	// Verify the total finding count is small (just DEP-01).
	assert.Len(t, results, len(dep01), "CheckFailFast must stop after first error check")
}

// TestCheckFailFast_PassesWhenNoErrors verifies CheckFailFast runs all checks
// when none produce errors, returning the same result set as Check.
func TestCheckFailFast_PassesWhenNoErrors(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDAccessCore: {ID: metadatatest.CellIDAccessCore, ConsistencyLevel: "L1"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/session-login": {
				ID:            "session-login",
				BelongsToCell: metadatatest.CellIDAccessCore,
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewValidator(project, "", clock.Real())
	results := depCheckFailFast(dc)
	assert.Empty(t, results, "clean project must produce no findings from CheckFailFast")
}

// TestDEP03_CellNotInAnyAssembly verifies that a cell with L0 dependencies
// that is not assigned to any assembly triggers DEP-03.
func TestDEP03_CellNotInAnyAssembly(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDAppCore: {
				ID:               metadatatest.CellIDAppCore,
				ConsistencyLevel: "L2",
				L0Dependencies: []metadata.L0DepMeta{
					{Cell: metadatatest.CellIDSharedCrypto, Reason: "hashing"},
				},
			},
			metadatatest.CellIDSharedCrypto: {
				ID:               metadatatest.CellIDSharedCrypto,
				ConsistencyLevel: "L0",
			},
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"bundle-a": {
				ID:    "bundle-a",
				Cells: []metadata.AssemblyCellRef{{ID: metadatatest.CellIDSharedCrypto}}, // app-core not in any assembly
			},
		},
	}

	dc := NewValidator(project, "", clock.Real())
	results := depCheck(dc)
	dep03 := findByCode(results, "DEP-03")
	require.Len(t, dep03, 1, "cell with L0 deps not in any assembly should produce 1 DEP-03 error")
	assert.Equal(t, SeverityError, dep03[0].Severity)
	assert.Contains(t, dep03[0].Message, "not assigned to any assembly")
}

// --- Graph() ---

func TestValidatorGraph_Empty(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	dc := NewValidator(project, "", clock.Real())
	g, errs := dc.Graph()
	assert.Empty(t, errs)
	assert.NotNil(t, g.Nodes, "Nodes must not be nil")
	assert.Empty(t, g.Nodes)
	assert.Nil(t, g.Edges)
}

func TestValidatorGraph_Acyclic(t *testing.T) {
	// cell-a → cell-b (cell-a depends on cell-b as L0)
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDCellA: {
				ID:               metadatatest.CellIDCellA,
				ConsistencyLevel: "L2",
				L0Dependencies:   nil,
			},
			metadatatest.CellIDCellB: {
				ID:               metadatatest.CellIDCellB,
				ConsistencyLevel: "L0",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"cella/slice-a": {
				ID:            "slice-a",
				BelongsToCell: metadatatest.CellIDCellA,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.a-calls-b.v1", Role: "call"},
				},
			},
			"cellb/slice-b": {
				ID:            "slice-b",
				BelongsToCell: metadatatest.CellIDCellB,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.a-calls-b.v1", Role: "serve"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.a-calls-b.v1": {
				ID:        "http.a-calls-b.v1",
				Kind:      "http",
				OwnerCell: metadatatest.CellIDCellB,
				Endpoints: metadata.EndpointsMeta{
					Server:  metadatatest.CellIDCellB,
					Clients: []string{metadatatest.CellIDCellA},
				},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	dc := NewValidator(project, "", clock.Real())
	g, errs := dc.Graph()
	assert.Empty(t, errs)

	// Nodes sorted
	assert.True(t, sort.StringsAreSorted(g.Nodes), "Nodes must be sorted")
	assert.Contains(t, g.Nodes, metadatatest.CellIDCellA)
	assert.Contains(t, g.Nodes, metadatatest.CellIDCellB)

	// Should have at least one edge (cell-a → cell-b)
	found := false
	for _, e := range g.Edges {
		if e.From == metadatatest.CellIDCellA && e.To == metadatatest.CellIDCellB {
			found = true
		}
	}
	assert.True(t, found, "expected edge cell-a → cell-b")
}

func TestValidatorGraph_IsolatedCells(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.NewCellID("alone"): {ID: metadatatest.NewCellID("alone"), ConsistencyLevel: "L1"},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	dc := NewValidator(project, "", clock.Real())
	g, errs := dc.Graph()
	assert.Empty(t, errs)
	assert.Contains(t, g.Nodes, metadatatest.NewCellID("alone"), "isolated cell must appear in Nodes")
}

func TestValidatorGraph_DeterministicOrder(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDCellA: {ID: metadatatest.CellIDCellA, ConsistencyLevel: "L1"},
			metadatatest.CellIDCellB: {ID: metadatatest.CellIDCellB, ConsistencyLevel: "L1"},
			metadatatest.CellIDCellC: {ID: metadatatest.CellIDCellC, ConsistencyLevel: "L1"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"cella/slice-a": {
				ID:            "slice-a",
				BelongsToCell: metadatatest.CellIDCellA,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.a-to-b.v1", Role: "serve"},
				},
			},
			"cellb/slice-b": {
				ID:            "slice-b",
				BelongsToCell: metadatatest.CellIDCellB,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.b-to-c.v1", Role: "serve"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.a-to-b.v1": {
				ID:   "http.a-to-b.v1",
				Kind: "http",
				Endpoints: metadata.EndpointsMeta{
					Server:  metadatatest.CellIDCellA,
					Clients: []string{metadatatest.CellIDCellB},
				},
			},
			"http.b-to-c.v1": {
				ID:   "http.b-to-c.v1",
				Kind: "http",
				Endpoints: metadata.EndpointsMeta{
					Server:  metadatatest.CellIDCellB,
					Clients: []string{metadatatest.CellIDCellC},
				},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewValidator(project, "", clock.Real())
	first, errs := dc.Graph()
	require.Empty(t, errs)

	firstBytes, err := json.Marshal(first)
	require.NoError(t, err)

	for i := 0; i < 50; i++ {
		g, errs := dc.Graph()
		require.Empty(t, errs)
		b, err := json.Marshal(g)
		require.NoError(t, err)
		assert.Equal(t, string(firstBytes), string(b), "Graph must be byte-equal on iteration %d", i)
	}
}

func TestValidatorGraph_PropagatesValidationErrors(t *testing.T) {
	// Construct ProjectMeta with a slice using a serve role on a contract that
	// has an unknown kind — this triggers buildDependencyGraph's error path
	// (cannot resolve consumers for unknown contract kind).
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.NewCellID("cellx"): {ID: metadatatest.NewCellID("cellx"), ConsistencyLevel: "L2"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"cellx/slice-x": {
				ID:            "slice-x",
				BelongsToCell: metadatatest.NewCellID("cellx"),
				ContractUsages: []metadata.ContractUsage{
					{Contract: "websocket.unknown.v1", Role: "serve"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"websocket.unknown.v1": {
				ID:   "websocket.unknown.v1",
				Kind: "websocket", // unknown kind → consumers resolution fails
				Endpoints: metadata.EndpointsMeta{
					Server: metadatatest.NewCellID("cellx"),
				},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	dc := NewValidator(project, "", clock.Real())
	_, errs := dc.Graph()
	assert.NotEmpty(t, errs, "should propagate validation errors from buildDependencyGraph")
}

// findByCode returns all results matching the given code (helper shared with existing tests).

// --- empty project ---

func TestValidator_EmptyProject(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewValidator(project, "", clock.Real())
	results := depCheck(dc)
	assert.Empty(t, results, "empty project should produce no findings")
}

func TestValidator_NilProject(t *testing.T) {
	dc := NewValidator(nil, "", clock.Real())
	results := depCheck(dc)
	assert.Empty(t, results, "nil project should produce no findings")
}

// TestGraph_ActorNodesFiltered verifies that Graph() does not include actor IDs
// (from actors.yaml) as nodes or edge endpoints. Actors participate in contracts
// via endpoints.clients but are not cells and must not appear in the cell dep graph.
func TestGraph_ActorNodesFiltered(t *testing.T) {
	// Setup: cell-a provides a contract that lists "webclient" (actor) as a client.
	// Without the filter, "webclient" would be added as a consumerCell node.
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDCellA: {ID: metadatatest.CellIDCellA, ConsistencyLevel: "L1"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"cella/slice-a": {
				ID:            "slice-a",
				BelongsToCell: metadatatest.CellIDCellA,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.api.v1", Role: "serve"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.api.v1": {
				ID:        "http.api.v1",
				Kind:      "http",
				OwnerCell: metadatatest.CellIDCellA,
				Endpoints: metadata.EndpointsMeta{
					Server:  metadatatest.CellIDCellA,
					Clients: []string{metadatatest.NewCellID("webclient")}, // actor, not a cell
				},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{},
		Actors:     []metadata.ActorMeta{{ID: "webclient", MaxConsistencyLevel: "L0"}},
	}

	dc := NewValidator(project, "", clock.Real())
	g, errs := dc.Graph()

	require.Empty(t, errs, "no resolution errors expected")

	// Nodes must only contain cell IDs — actor "webclient" must be absent.
	for _, n := range g.Nodes {
		assert.NotEqual(t, "webclient", n, "actor ID must not appear as a graph node")
	}
	// Edges must not reference the actor.
	for _, e := range g.Edges {
		assert.NotEqual(t, "webclient", e.From, "actor must not appear as edge From")
		assert.NotEqual(t, "webclient", e.To, "actor must not appear as edge To")
	}
}
