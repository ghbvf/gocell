package governance

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
					"access-core": {ID: "access-core", ConsistencyLevel: "L1"},
				},
				Slices: map[string]*metadata.SliceMeta{
					"access-core/session-login": {
						ID:            "session-login",
						BelongsToCell: "access-core",
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
					"access-core": {ID: "access-core", ConsistencyLevel: "L1"},
					"audit-core":  {ID: "audit-core", ConsistencyLevel: "L1"},
				},
				Slices: map[string]*metadata.SliceMeta{
					"access-core/session-login": {
						ID:            "session-login",
						BelongsToCell: "audit-core", // mismatch!
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
					{Cell: "shared-crypto", Reason: "hashing"},
				},
			},
			"shared-crypto": {
				ID:               "shared-crypto",
				ConsistencyLevel: "L0",
			},
		},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"main-bundle": {
				ID:    "main-bundle",
				Cells: []string{"app-core", "shared-crypto"},
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
					{Cell: "shared-crypto", Reason: "hashing"},
				},
			},
			"shared-crypto": {
				ID:               "shared-crypto",
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
				Cells: []string{"shared-crypto"},
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
					{Cell: "shared-crypto", Reason: "hashing"},
				},
			},
			"shared-crypto": {
				ID:               "shared-crypto",
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
