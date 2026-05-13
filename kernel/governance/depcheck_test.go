package governance

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// --- DEP-01: One-slice-one-cell ---

func TestDEP01(t *testing.T) {
	tests := []struct {
		name      string
		project   *metadata.ProjectMeta
		wantCount int
		wantCode  string
	}{
		{
			name: "belongsToCell matches key cellID — no error",
			project: &metadata.ProjectMeta{
				Cells: map[string]*metadata.CellMeta{
					"accesscore": {ID: "accesscore", ConsistencyLevel: "L1"},
				},
				Slices: map[string]*metadata.SliceMeta{
					"accesscore/session-login": {
						ID:            "session-login",
						BelongsToCell: "accesscore",
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
					"accesscore": {ID: "accesscore", ConsistencyLevel: "L1"},
					"auditcore":  {ID: "auditcore", ConsistencyLevel: "L1"},
				},
				Slices: map[string]*metadata.SliceMeta{
					"accesscore/session-login": {
						ID:            "session-login",
						BelongsToCell: "auditcore", // mismatch!
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
			dc := NewDependencyChecker(tt.project)
			results := dc.Check()
			dep01 := findByCode(results, "DEP-01")
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
			"cell-a": {ID: "cell-a", ConsistencyLevel: "L2"},
			"cell-b": {ID: "cell-b", ConsistencyLevel: "L2"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"cell-a/slice-a": {
				ID:            "slice-a",
				BelongsToCell: "cell-a",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.a-to-b.v1", Role: "serve"},
				},
			},
			"cell-b/slice-b": {
				ID:            "slice-b",
				BelongsToCell: "cell-b",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.b-to-a.v1", Role: "serve"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.a-to-b.v1": {
				ID:        "http.a-to-b.v1",
				Kind:      "http",
				OwnerCell: "cell-a",
				Endpoints: metadata.EndpointsMeta{
					Server:  "cell-a",
					Clients: []string{"cell-b"},
				},
			},
			"http.b-to-a.v1": {
				ID:        "http.b-to-a.v1",
				Kind:      "http",
				OwnerCell: "cell-b",
				Endpoints: metadata.EndpointsMeta{
					Server:  "cell-b",
					Clients: []string{"cell-a"},
				},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewDependencyChecker(project)
	results := dc.Check()
	dep02 := findByCode(results, "DEP-02")
	require.Len(t, dep02, 1, "expected exactly 1 cycle error")
	assert.Equal(t, SeverityError, dep02[0].Severity)
	assert.Contains(t, dep02[0].Message, "circular dependency detected")
}

func TestDEP02_NoCycle(t *testing.T) {
	// A→B (no cycle): cell-a provides, cell-b consumes
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"cell-a": {ID: "cell-a", ConsistencyLevel: "L2"},
			"cell-b": {ID: "cell-b", ConsistencyLevel: "L2"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"cell-a/slice-a": {
				ID:            "slice-a",
				BelongsToCell: "cell-a",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.a-to-b.v1", Role: "serve"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.a-to-b.v1": {
				ID:        "http.a-to-b.v1",
				Kind:      "http",
				OwnerCell: "cell-a",
				Endpoints: metadata.EndpointsMeta{
					Server:  "cell-a",
					Clients: []string{"cell-b"},
				},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewDependencyChecker(project)
	results := dc.Check()
	dep02 := findByCode(results, "DEP-02")
	assert.Empty(t, dep02, "acyclic graph should produce no DEP-02 errors")
}

func TestDEP02_SingleCellNoExternalDeps(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"lonely": {ID: "lonely", ConsistencyLevel: "L1"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"lonely/only-slice": {
				ID:            "only-slice",
				BelongsToCell: "lonely",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewDependencyChecker(project)
	results := dc.Check()
	dep02 := findByCode(results, "DEP-02")
	assert.Empty(t, dep02, "single cell with no deps should produce no DEP-02 errors")
}

func TestDEP02_UnknownKindWarning(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"cell-a": {ID: "cell-a", ConsistencyLevel: "L2"},
			"cell-b": {ID: "cell-b", ConsistencyLevel: "L2"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"cell-a/slice-x": {
				ID:            "slice-x",
				BelongsToCell: "cell-a",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "bad.kind.v1", Role: "serve"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"bad.kind.v1": {
				ID:   "bad.kind.v1",
				Kind: "grpc", // unknown kind
				Endpoints: metadata.EndpointsMeta{
					Server: "cell-a",
				},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewDependencyChecker(project)
	results := dc.Check()
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
			"app-core": {
				ID:               "app-core",
				ConsistencyLevel: "L2",
				L0Dependencies: []metadata.L0DepMeta{
					{Cell: "sharedcrypto", Reason: "hashing"},
				},
			},
			"sharedcrypto": {
				ID:               "sharedcrypto",
				ConsistencyLevel: "L0",
			},
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"main-bundle": {
				ID:    "main-bundle",
				Cells: []string{"app-core", "sharedcrypto"},
			},
		},
	}

	dc := NewDependencyChecker(project)
	results := dc.Check()
	dep03 := findByCode(results, "DEP-03")
	assert.Empty(t, dep03, "L0 dep in same assembly should produce no DEP-03 errors")
}

func TestDEP03_DifferentAssembly(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"app-core": {
				ID:               "app-core",
				ConsistencyLevel: "L2",
				L0Dependencies: []metadata.L0DepMeta{
					{Cell: "sharedcrypto", Reason: "hashing"},
				},
			},
			"sharedcrypto": {
				ID:               "sharedcrypto",
				ConsistencyLevel: "L0",
			},
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"bundle-a": {
				ID:    "bundle-a",
				Cells: []string{"app-core"},
			},
			"bundle-b": {
				ID:    "bundle-b",
				Cells: []string{"sharedcrypto"},
			},
		},
	}

	dc := NewDependencyChecker(project)
	results := dc.Check()
	dep03 := findByCode(results, "DEP-03")
	require.Len(t, dep03, 1, "L0 dep in different assembly should produce 1 DEP-03 error")
	assert.Equal(t, SeverityError, dep03[0].Severity)
	assert.Contains(t, dep03[0].Message, "same assembly")
}

func TestDEP03_NoAssemblies(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"app-core": {
				ID:               "app-core",
				ConsistencyLevel: "L2",
				L0Dependencies: []metadata.L0DepMeta{
					{Cell: "sharedcrypto", Reason: "hashing"},
				},
			},
			"sharedcrypto": {
				ID:               "sharedcrypto",
				ConsistencyLevel: "L0",
			},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewDependencyChecker(project)
	results := dc.Check()
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
			"accesscore": {ID: "accesscore", ConsistencyLevel: "L1"},
			"auditcore":  {ID: "auditcore", ConsistencyLevel: "L1"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/session-login": {
				ID:            "session-login",
				BelongsToCell: "auditcore", // mismatch triggers DEP-01 error
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewDependencyChecker(project)
	results := dc.CheckFailFast()

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
			"accesscore": {ID: "accesscore", ConsistencyLevel: "L1"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/session-login": {
				ID:            "session-login",
				BelongsToCell: "accesscore",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewDependencyChecker(project)
	results := dc.CheckFailFast()
	assert.Empty(t, results, "clean project must produce no findings from CheckFailFast")
}

// TestDEP03_CellNotInAnyAssembly verifies that a cell with L0 dependencies
// that is not assigned to any assembly triggers DEP-03.
func TestDEP03_CellNotInAnyAssembly(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"app-core": {
				ID:               "app-core",
				ConsistencyLevel: "L2",
				L0Dependencies: []metadata.L0DepMeta{
					{Cell: "sharedcrypto", Reason: "hashing"},
				},
			},
			"sharedcrypto": {
				ID:               "sharedcrypto",
				ConsistencyLevel: "L0",
			},
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"bundle-a": {
				ID:    "bundle-a",
				Cells: []string{"sharedcrypto"}, // app-core not in any assembly
			},
		},
	}

	dc := NewDependencyChecker(project)
	results := dc.Check()
	dep03 := findByCode(results, "DEP-03")
	require.Len(t, dep03, 1, "cell with L0 deps not in any assembly should produce 1 DEP-03 error")
	assert.Equal(t, SeverityError, dep03[0].Severity)
	assert.Contains(t, dep03[0].Message, "not assigned to any assembly")
}

// --- Graph() ---

func TestDependencyChecker_Graph_Empty(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	dc := NewDependencyChecker(project)
	g, errs := dc.Graph()
	assert.Empty(t, errs)
	assert.NotNil(t, g.Nodes, "Nodes must not be nil")
	assert.Empty(t, g.Nodes)
	assert.Nil(t, g.Edges)
}

func TestDependencyChecker_Graph_Acyclic(t *testing.T) {
	// cell-a → cell-b (cell-a depends on cell-b as L0)
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"cell-a": {
				ID:               "cell-a",
				ConsistencyLevel: "L2",
				L0Dependencies:   nil,
			},
			"cell-b": {
				ID:               "cell-b",
				ConsistencyLevel: "L0",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"cell-a/slice-a": {
				ID:            "slice-a",
				BelongsToCell: "cell-a",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.a-calls-b.v1", Role: "call"},
				},
			},
			"cell-b/slice-b": {
				ID:            "slice-b",
				BelongsToCell: "cell-b",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.a-calls-b.v1", Role: "serve"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.a-calls-b.v1": {
				ID:        "http.a-calls-b.v1",
				Kind:      "http",
				OwnerCell: "cell-b",
				Endpoints: metadata.EndpointsMeta{
					Server:  "cell-b",
					Clients: []string{"cell-a"},
				},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	dc := NewDependencyChecker(project)
	g, errs := dc.Graph()
	assert.Empty(t, errs)

	// Nodes sorted
	assert.True(t, sort.StringsAreSorted(g.Nodes), "Nodes must be sorted")
	assert.Contains(t, g.Nodes, "cell-a")
	assert.Contains(t, g.Nodes, "cell-b")

	// Should have at least one edge (cell-a → cell-b)
	found := false
	for _, e := range g.Edges {
		if e.From == "cell-a" && e.To == "cell-b" {
			found = true
		}
	}
	assert.True(t, found, "expected edge cell-a → cell-b")
}

func TestDependencyChecker_Graph_IsolatedCells(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"alone": {ID: "alone", ConsistencyLevel: "L1"},
		},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	dc := NewDependencyChecker(project)
	g, errs := dc.Graph()
	assert.Empty(t, errs)
	assert.Contains(t, g.Nodes, "alone", "isolated cell must appear in Nodes")
}

func TestDependencyChecker_Graph_DeterministicOrder(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"cell-a": {ID: "cell-a", ConsistencyLevel: "L1"},
			"cell-b": {ID: "cell-b", ConsistencyLevel: "L1"},
			"cell-c": {ID: "cell-c", ConsistencyLevel: "L1"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"cell-a/slice-a": {
				ID:            "slice-a",
				BelongsToCell: "cell-a",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.a-to-b.v1", Role: "serve"},
				},
			},
			"cell-b/slice-b": {
				ID:            "slice-b",
				BelongsToCell: "cell-b",
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
					Server:  "cell-a",
					Clients: []string{"cell-b"},
				},
			},
			"http.b-to-c.v1": {
				ID:   "http.b-to-c.v1",
				Kind: "http",
				Endpoints: metadata.EndpointsMeta{
					Server:  "cell-b",
					Clients: []string{"cell-c"},
				},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewDependencyChecker(project)
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

func TestDependencyChecker_Graph_PropagatesValidationErrors(t *testing.T) {
	// Construct ProjectMeta with a slice using a serve role on a contract that
	// has an unknown kind — this triggers buildDependencyGraph's error path
	// (cannot resolve consumers for unknown contract kind).
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"cell-x": {ID: "cell-x", ConsistencyLevel: "L2"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"cell-x/slice-x": {
				ID:            "slice-x",
				BelongsToCell: "cell-x",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "grpc.unknown.v1", Role: "serve"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"grpc.unknown.v1": {
				ID:   "grpc.unknown.v1",
				Kind: "grpc", // unknown kind → consumers resolution fails
				Endpoints: metadata.EndpointsMeta{
					Server: "cell-x",
				},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	dc := NewDependencyChecker(project)
	_, errs := dc.Graph()
	assert.NotEmpty(t, errs, "should propagate validation errors from buildDependencyGraph")
}

// findByCode returns all results matching the given code (helper shared with existing tests).

// --- empty project ---

func TestDependencyChecker_EmptyProject(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	dc := NewDependencyChecker(project)
	results := dc.Check()
	assert.Empty(t, results, "empty project should produce no findings")
}

func TestDependencyChecker_NilProject(t *testing.T) {
	dc := NewDependencyChecker(nil)
	results := dc.Check()
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
			"cell-a": {ID: "cell-a", ConsistencyLevel: "L1"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"cell-a/slice-a": {
				ID:            "slice-a",
				BelongsToCell: "cell-a",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.api.v1", Role: "serve"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.api.v1": {
				ID:        "http.api.v1",
				Kind:      "http",
				OwnerCell: "cell-a",
				Endpoints: metadata.EndpointsMeta{
					Server:  "cell-a",
					Clients: []string{"webclient"}, // actor, not a cell
				},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{},
		Actors:     []metadata.ActorMeta{{ID: "webclient", MaxConsistencyLevel: "L0"}},
	}

	dc := NewDependencyChecker(project)
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
