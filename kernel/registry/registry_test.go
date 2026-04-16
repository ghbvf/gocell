package registry_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/registry"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// testProject returns a ProjectMeta with realistic test data:
// 2 cells, 3 slices, 4 contracts (one per kind).
func testProject() *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"access-core": {
				ID:               "access-core",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "identity", Role: "backend"},
			},
			"audit-core": {
				ID:               "audit-core",
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "compliance", Role: "backend"},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"access-core/session-create": {
				ID:            "session-create",
				BelongsToCell: "access-core",
			},
			"access-core/session-refresh": {
				ID:            "session-refresh",
				BelongsToCell: "access-core",
			},
			"audit-core/audit-write": {
				ID:            "audit-write",
				BelongsToCell: "audit-core",
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http-auth-login-v1": {
				ID:        "http-auth-login-v1",
				Kind:      "http",
				OwnerCell: "access-core",
				Endpoints: metadata.EndpointsMeta{
					Server:  "access-core",
					Clients: []string{"edge-gateway", "admin-bff"},
				},
			},
			"event-session-created-v1": {
				ID:        "event-session-created-v1",
				Kind:      "event",
				OwnerCell: "access-core",
				Endpoints: metadata.EndpointsMeta{
					Publisher:   "access-core",
					Subscribers: []string{"audit-core", "config-core"},
				},
			},
			"command-audit-archive-v1": {
				ID:        "command-audit-archive-v1",
				Kind:      "command",
				OwnerCell: "audit-core",
				Endpoints: metadata.EndpointsMeta{
					Handler:  "audit-core",
					Invokers: []string{"access-core"},
				},
			},
			"projection-audit-summary-v1": {
				ID:        "projection-audit-summary-v1",
				Kind:      "projection",
				OwnerCell: "audit-core",
				Endpoints: metadata.EndpointsMeta{
					Provider: "audit-core",
					Readers:  []string{"access-core", "config-core"},
				},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// ContractRegistry tests
// ---------------------------------------------------------------------------

func TestContractRegistry_Get(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		wantID string
		found  bool
	}{
		{"existing contract", "http-auth-login-v1", "http-auth-login-v1", true},
		{"another existing", "event-session-created-v1", "event-session-created-v1", true},
		{"not found", "nonexistent", "", false},
	}
	reg := registry.NewContractRegistry(testProject())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reg.Get(tt.id)
			if tt.found {
				require.NotNil(t, got)
				assert.Equal(t, tt.wantID, got.ID)
			} else {
				assert.Nil(t, got)
			}
		})
	}
}

func TestContractRegistry_ByKind(t *testing.T) {
	tests := []struct {
		name  string
		kind  string
		count int
	}{
		{"http contracts", "http", 1},
		{"event contracts", "event", 1},
		{"command contracts", "command", 1},
		{"projection contracts", "projection", 1},
		{"unknown kind", "grpc", 0},
	}
	reg := registry.NewContractRegistry(testProject())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reg.ByKind(tt.kind)
			assert.Len(t, got, tt.count)
		})
	}
}

func TestContractRegistry_ByOwner(t *testing.T) {
	tests := []struct {
		name    string
		cellID  string
		count   int
	}{
		{"access-core owns 2", "access-core", 2},
		{"audit-core owns 2", "audit-core", 2},
		{"unknown cell", "config-core", 0},
	}
	reg := registry.NewContractRegistry(testProject())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reg.ByOwner(tt.cellID)
			assert.Len(t, got, tt.count)
		})
	}
}

func TestContractRegistry_Provider(t *testing.T) {
	tests := []struct {
		name       string
		contractID string
		want       string
	}{
		{"http provider is server", "http-auth-login-v1", "access-core"},
		{"event provider is publisher", "event-session-created-v1", "access-core"},
		{"command provider is handler", "command-audit-archive-v1", "audit-core"},
		{"projection provider is provider", "projection-audit-summary-v1", "audit-core"},
	}
	reg := registry.NewContractRegistry(testProject())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := reg.Provider(tt.contractID)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestContractRegistry_Provider_NotFound(t *testing.T) {
	reg := registry.NewContractRegistry(testProject())
	got, err := reg.Provider("nonexistent")
	require.Error(t, err)
	assert.Equal(t, "", got)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrContractNotFound, ec.Code)
}

func TestContractRegistry_Consumers(t *testing.T) {
	tests := []struct {
		name       string
		contractID string
		want       []string
	}{
		{"http consumers are clients", "http-auth-login-v1", []string{"edge-gateway", "admin-bff"}},
		{"event consumers are subscribers", "event-session-created-v1", []string{"audit-core", "config-core"}},
		{"command consumers are invokers", "command-audit-archive-v1", []string{"access-core"}},
		{"projection consumers are readers", "projection-audit-summary-v1", []string{"access-core", "config-core"}},
	}
	reg := registry.NewContractRegistry(testProject())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := reg.Consumers(tt.contractID)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestContractRegistry_Consumers_NotFound(t *testing.T) {
	reg := registry.NewContractRegistry(testProject())
	got, err := reg.Consumers("nonexistent")
	require.Error(t, err)
	assert.Nil(t, got)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrContractNotFound, ec.Code)
}

func TestContractRegistry_AllIDs(t *testing.T) {
	reg := registry.NewContractRegistry(testProject())
	ids := reg.AllIDs()
	expected := []string{
		"command-audit-archive-v1",
		"event-session-created-v1",
		"http-auth-login-v1",
		"projection-audit-summary-v1",
	}
	assert.Equal(t, expected, ids)
}

func TestContractRegistry_Count(t *testing.T) {
	reg := registry.NewContractRegistry(testProject())
	assert.Equal(t, 4, reg.Count())
}

func TestContractRegistry_EmptyProject(t *testing.T) {
	tests := []struct {
		name    string
		project *metadata.ProjectMeta
	}{
		{"nil project", nil},
		{"empty project", &metadata.ProjectMeta{}},
		{"nil contracts map", &metadata.ProjectMeta{Contracts: nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := registry.NewContractRegistry(tt.project)
			assert.Equal(t, 0, reg.Count())
			assert.Nil(t, reg.Get("any"))
			assert.Empty(t, reg.ByKind("http"))
			assert.Empty(t, reg.ByOwner("any"))
			_, providerErr := reg.Provider("any")
			require.Error(t, providerErr)
			_, consumersErr := reg.Consumers("any")
			require.Error(t, consumersErr)
			assert.Empty(t, reg.AllIDs())
		})
	}
}

// ---------------------------------------------------------------------------
// CellRegistry tests
// ---------------------------------------------------------------------------

func TestCellRegistry_Get(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		wantID string
		found  bool
	}{
		{"existing cell", "access-core", "access-core", true},
		{"another existing", "audit-core", "audit-core", true},
		{"not found", "nonexistent", "", false},
	}
	reg := registry.NewCellRegistry(testProject())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reg.Get(tt.id)
			if tt.found {
				require.NotNil(t, got)
				assert.Equal(t, tt.wantID, got.ID)
			} else {
				assert.Nil(t, got)
			}
		})
	}
}

func TestCellRegistry_SlicesFor(t *testing.T) {
	tests := []struct {
		name   string
		cellID string
		count  int
	}{
		{"access-core has 2 slices", "access-core", 2},
		{"audit-core has 1 slice", "audit-core", 1},
		{"unknown cell has 0", "config-core", 0},
	}
	reg := registry.NewCellRegistry(testProject())
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reg.SlicesFor(tt.cellID)
			assert.Len(t, got, tt.count)
		})
	}
}

func TestCellRegistry_AllIDs(t *testing.T) {
	reg := registry.NewCellRegistry(testProject())
	ids := reg.AllIDs()
	expected := []string{"access-core", "audit-core"}
	assert.Equal(t, expected, ids)
}

func TestCellRegistry_Count(t *testing.T) {
	reg := registry.NewCellRegistry(testProject())
	assert.Equal(t, 2, reg.Count())
}

func TestCellRegistry_EmptyProject(t *testing.T) {
	tests := []struct {
		name    string
		project *metadata.ProjectMeta
	}{
		{"nil project", nil},
		{"empty project", &metadata.ProjectMeta{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := registry.NewCellRegistry(tt.project)
			assert.Equal(t, 0, reg.Count())
			assert.Nil(t, reg.Get("any"))
			assert.Empty(t, reg.SlicesFor("any"))
			assert.Empty(t, reg.AllIDs())
		})
	}
}

// ---------------------------------------------------------------------------
// Edge-case tests for coverage
// ---------------------------------------------------------------------------

func TestContractRegistry_Provider_UnknownKind(t *testing.T) {
	proj := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"grpc-unknown-v1": {
				ID:   "grpc-unknown-v1",
				Kind: "grpc",
			},
		},
	}
	reg := registry.NewContractRegistry(proj)
	got, err := reg.Provider("grpc-unknown-v1")
	require.Error(t, err)
	assert.Equal(t, "", got)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, err.Error(), "grpc")
}

func TestContractRegistry_Consumers_UnknownKind(t *testing.T) {
	proj := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"grpc-unknown-v1": {
				ID:   "grpc-unknown-v1",
				Kind: "grpc",
			},
		},
	}
	reg := registry.NewContractRegistry(proj)
	got, err := reg.Consumers("grpc-unknown-v1")
	require.Error(t, err)
	assert.Nil(t, got)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, err.Error(), "grpc")
}

func TestContractRegistry_NilContractInMap(t *testing.T) {
	proj := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"valid": {ID: "valid", Kind: "http"},
			"nil":   nil,
		},
	}
	reg := registry.NewContractRegistry(proj)
	assert.Equal(t, 1, reg.Count())
	assert.Nil(t, reg.Get("nil"))
}

func TestCellRegistry_NilEntries(t *testing.T) {
	proj := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"valid": {ID: "valid"},
			"nil":   nil,
		},
		Slices: map[string]*metadata.SliceMeta{
			"valid/s1": {ID: "s1", BelongsToCell: "valid"},
			"valid/nil": nil,
		},
	}
	reg := registry.NewCellRegistry(proj)
	assert.Equal(t, 1, reg.Count())
	assert.Nil(t, reg.Get("nil"))
	assert.Len(t, reg.SlicesFor("valid"), 1)
}

func TestCellRegistry_SliceFallbackCellID(t *testing.T) {
	// Slice with empty BelongsToCell should fall back to parsing composite key.
	proj := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"fallback-cell": {ID: "fallback-cell"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"fallback-cell/orphan-slice": {
				ID:            "orphan-slice",
				BelongsToCell: "", // empty on purpose
			},
		},
	}
	reg := registry.NewCellRegistry(proj)
	assert.Len(t, reg.SlicesFor("fallback-cell"), 1)
}

// --- Deep-copy mutation tests ---

func TestContractRegistry_Get_DeepCopy(t *testing.T) {
	reg := registry.NewContractRegistry(testProject())
	got := reg.Get("http-auth-login-v1")
	require.NotNil(t, got)

	// Mutate the returned copy.
	got.Endpoints.Clients[0] = "MUTATED"
	got.ID = "MUTATED"

	// Original must be unchanged.
	original := reg.Get("http-auth-login-v1")
	assert.Equal(t, "http-auth-login-v1", original.ID)
	assert.NotEqual(t, "MUTATED", original.Endpoints.Clients[0])
}

func TestContractRegistry_ByKind_DeepCopy(t *testing.T) {
	reg := registry.NewContractRegistry(testProject())
	got := reg.ByKind("http")
	require.Len(t, got, 1)

	got[0].Endpoints.Clients[0] = "MUTATED"

	fresh := reg.ByKind("http")
	assert.NotEqual(t, "MUTATED", fresh[0].Endpoints.Clients[0])
}

func TestContractRegistry_Consumers_DeepCopy(t *testing.T) {
	reg := registry.NewContractRegistry(testProject())
	got, err := reg.Consumers("http-auth-login-v1")
	require.NoError(t, err)
	require.NotEmpty(t, got)

	got[0] = "MUTATED"

	fresh, err := reg.Consumers("http-auth-login-v1")
	require.NoError(t, err)
	assert.NotEqual(t, "MUTATED", fresh[0])
}

func TestCellRegistry_Get_DeepCopy(t *testing.T) {
	proj := testProject()
	proj.Cells["access-core"].Verify.Smoke = []string{"smoke.startup"}
	reg := registry.NewCellRegistry(proj)
	got := reg.Get("access-core")
	require.NotNil(t, got)

	got.Verify.Smoke[0] = "MUTATED"
	got.ID = "MUTATED"

	original := reg.Get("access-core")
	assert.Equal(t, "access-core", original.ID)
	assert.NotEqual(t, "MUTATED", original.Verify.Smoke[0])
}

func TestCellRegistry_SlicesFor_DeepCopy(t *testing.T) {
	proj := testProject()
	proj.Slices["access-core/session-create"].ContractUsages = []metadata.ContractUsage{
		{Contract: "http-auth-login-v1", Role: "serve"},
	}
	reg := registry.NewCellRegistry(proj)
	got := reg.SlicesFor("access-core")
	require.NotEmpty(t, got)

	// Find the slice with contract usages.
	var target *metadata.SliceMeta
	for _, s := range got {
		if len(s.ContractUsages) > 0 {
			target = s
			break
		}
	}
	require.NotNil(t, target)

	target.ContractUsages[0].Role = "MUTATED"

	fresh := reg.SlicesFor("access-core")
	for _, s := range fresh {
		for _, cu := range s.ContractUsages {
			assert.NotEqual(t, "MUTATED", cu.Role)
		}
	}
}
