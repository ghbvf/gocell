package governance

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- helpers ---

// validProject returns a minimal but fully valid ProjectMeta for testing.
// All references are consistent, roles match kinds, consistency levels are valid,
// and verify/waiver entries exist.
func validProject() *metadata.ProjectMeta {
	replayable := true
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"access-core": {
				ID:               "access-core",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.access-core.startup"}},
			},
			"audit-core": {
				ID:               "audit-core",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_audit_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.audit-core.startup"}},
			},
			"shared-crypto": {
				ID:               "shared-crypto",
				Type:             "support",
				ConsistencyLevel: "L0",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.shared-crypto"}},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"access-core/session-login": {
				ID:            "session-login",
				BelongsToCell: "access-core",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
					{Contract: "event.session.created.v1", Role: "publish"},
				},
				Verify: metadata.SliceVerifyMeta{
					Unit: []string{"unit.session-login.service"},
					Contract: []string{
						"contract.http.auth.login.v1.serve",
						"contract.event.session.created.v1.publish",
					},
				},
			},
			"audit-core/audit-write": {
				ID:            "audit-write",
				BelongsToCell: "audit-core",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "event.session.created.v1", Role: "subscribe"},
				},
				Verify: metadata.SliceVerifyMeta{
					Unit: []string{"unit.audit-write.handler"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.login.v1": {
				ID:               "http.auth.login.v1",
				Kind:             "http",
				OwnerCell:        "access-core",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  "access-core",
					Clients: []string{"audit-core"},
				},
			},
			"event.session.created.v1": {
				ID:               "event.session.created.v1",
				Kind:             "event",
				OwnerCell:        "access-core",
				ConsistencyLevel: "L2",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Publisher:   "access-core",
					Subscribers: []string{"audit-core"},
				},
				Replayable:        &replayable,
				IdempotencyKey:    "session-id",
				DeliverySemantics: "at-least-once",
			},
		},
		Journeys: map[string]*metadata.JourneyMeta{
			"J-sso-login": {
				ID:    "J-sso-login",
				Goal:  "User completes SSO login",
				Owner: metadata.OwnerMeta{Team: "platform", Role: "journey-owner"},
				Cells: []string{"access-core", "audit-core"},
				Contracts: []string{
					"http.auth.login.v1",
					"event.session.created.v1",
				},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"core-bundle": {
				ID:    "core-bundle",
				Cells: []string{"access-core", "audit-core", "shared-crypto"},
				Build: metadata.BuildMeta{
					Entrypoint:     "src/cmd/core-bundle/main.go",
					Binary:         "core-bundle",
					DeployTemplate: "k8s",
				},
			},
		},
		StatusBoard: []metadata.StatusBoardEntry{
			{JourneyID: "J-sso-login", State: "doing", Risk: "low", UpdatedAt: "2026-04-04"},
		},
		Actors: []metadata.ActorMeta{
			{ID: "edge-bff", Type: "external", MaxConsistencyLevel: "L1"},
		},
	}
}

// findByCode returns all results matching the given code.
func findByCode(results []ValidationResult, code string) []ValidationResult {
	var out []ValidationResult
	for _, r := range results {
		if r.Code == code {
			out = append(out, r)
		}
	}
	return out
}

// --- test: full valid project ---

func TestValidProject_ZeroErrors(t *testing.T) {
	pm := validProject()
	val := NewValidator(pm, ".")
	results := val.Validate()
	errs := val.Errors(results)
	assert.Empty(t, errs, "valid project should have 0 errors, got: %v", errs)
}

// --- test: HasErrors / Errors / Warnings ---

func TestFilterFunctions(t *testing.T) {
	pm := validProject()
	val := NewValidator(pm, ".")

	results := []ValidationResult{
		{Code: "ERR-1", Severity: SeverityError},
		{Code: "WARN-1", Severity: SeverityWarning},
		{Code: "ERR-2", Severity: SeverityError},
		{Code: "WARN-2", Severity: SeverityWarning},
	}

	assert.True(t, val.HasErrors(results))
	assert.Len(t, val.Errors(results), 2)
	assert.Len(t, val.Warnings(results), 2)

	assert.False(t, val.HasErrors(val.Warnings(results)))
	assert.False(t, val.HasErrors(nil))
}

// --- REF rules ---

func TestREF01(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "valid belongsToCell",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "missing cell",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["ghost/some-slice"] = &metadata.SliceMeta{
					ID:            "some-slice",
					BelongsToCell: "ghost",
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateREF01(), "REF-01")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				assert.Equal(t, IssueRefNotFound, r.IssueType)
			}
		})
	}
}

func TestREF02(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "valid contract references",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "missing contract",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/bad-slice"] = &metadata.SliceMeta{
					ID:            "bad-slice",
					BelongsToCell: "access-core",
					ContractUsages: []metadata.ContractUsage{
						{Contract: "http.nonexistent.v1", Role: "serve"},
					},
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateREF02(), "REF-02")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestREF03(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "valid ownerCell",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "ownerCell is actor not cell",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.external.v1"] = &metadata.ContractMeta{
					ID:               "http.external.v1",
					Kind:             "http",
					OwnerCell:        "edge-bff", // actor, not a cell
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Server: "edge-bff"},
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateREF03(), "REF-03")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestREF04(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "id matches key",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "id mismatch",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["wrong-key"] = &metadata.CellMeta{
					ID:               "actual-id",
					Type:             "core",
					ConsistencyLevel: "L1",
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateREF04(), "REF-04")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestREF05(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "id matches directory",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "id mismatch",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/wrong-name"] = &metadata.SliceMeta{
					ID:            "actual-name",
					BelongsToCell: "access-core",
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateREF05(), "REF-05")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestREF06(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "valid journey cells",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "journey references missing cell",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Journeys["J-sso-login"].Cells = append(pm.Journeys["J-sso-login"].Cells, "nonexistent")
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateREF06(), "REF-06")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestREF07(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "valid journey contracts",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "journey references missing contract",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Journeys["J-sso-login"].Contracts = append(pm.Journeys["J-sso-login"].Contracts, "nonexistent.v1")
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateREF07(), "REF-07")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestREF08(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "valid assembly cells",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "assembly references missing cell",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Assemblies["core-bundle"].Cells = append(pm.Assemblies["core-bundle"].Cells, "nonexistent")
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateREF08(), "REF-08")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestREF09(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "no l0Dependencies",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "valid l0 dependency",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["access-core"].L0Dependencies = []metadata.L0DepMeta{
					{Cell: "shared-crypto", Reason: "hashing"},
				}
			},
			wantCount: 0,
		},
		{
			name: "missing l0 dependency target",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["access-core"].L0Dependencies = []metadata.L0DepMeta{
					{Cell: "nonexistent", Reason: "missing"},
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateREF09(), "REF-09")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

// --- TOPO rules ---

func TestTOPO01(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "valid roles for kinds",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "wrong role for http contract",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/bad-role"] = &metadata.SliceMeta{
					ID:            "bad-role",
					BelongsToCell: "access-core",
					ContractUsages: []metadata.ContractUsage{
						{Contract: "http.auth.login.v1", Role: "publish"}, // publish is event role
					},
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateTOPO01(), "TOPO-01")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestTOPO02(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "provider matches contract provider",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "provider mismatch",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["audit-core/wrong-provider"] = &metadata.SliceMeta{
					ID:            "wrong-provider",
					BelongsToCell: "audit-core",
					ContractUsages: []metadata.ContractUsage{
						{Contract: "http.auth.login.v1", Role: "serve"}, // server is access-core
					},
					Verify: metadata.SliceVerifyMeta{
						Contract: []string{"contract.http.auth.login.v1.serve"},
					},
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateTOPO02(), "TOPO-02")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestTOPO03(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "consumer in consumers list",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "consumer not in consumers list",
			setup: func(pm *metadata.ProjectMeta) {
				// access-core is not in the subscribers list for event.session.created.v1
				pm.Slices["access-core/wrong-consumer"] = &metadata.SliceMeta{
					ID:            "wrong-consumer",
					BelongsToCell: "access-core",
					ContractUsages: []metadata.ContractUsage{
						{Contract: "event.session.created.v1", Role: "subscribe"}, // access-core is publisher, not subscriber
					},
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateTOPO03(), "TOPO-03")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestTOPO04(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "contract level within cell level",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "contract level exceeds cell level",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].ConsistencyLevel = "L4"
				// ownerCell access-core is L2
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateTOPO04(), "TOPO-04")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestTOPO05(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "no L0 cells in contracts",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "L0 cell as provider in contract",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.crypto.v1"] = &metadata.ContractMeta{
					ID:               "http.crypto.v1",
					Kind:             "http",
					OwnerCell:        "access-core",
					ConsistencyLevel: "L0",
					Lifecycle:        "active",
					Endpoints: metadata.EndpointsMeta{
						Server:  "shared-crypto", // L0 cell
						Clients: []string{"access-core"},
					},
				}
			},
			wantCount: 1,
		},
		{
			name: "L0 cell as consumer in contract",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.crypto.v1"] = &metadata.ContractMeta{
					ID:               "http.crypto.v1",
					Kind:             "http",
					OwnerCell:        "access-core",
					ConsistencyLevel: "L0",
					Lifecycle:        "active",
					Endpoints: metadata.EndpointsMeta{
						Server:  "access-core",
						Clients: []string{"shared-crypto"}, // L0 cell
					},
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateTOPO05(), "TOPO-05")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestTOPO06(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "each cell in one assembly",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "cell in two assemblies",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Assemblies["edge-bundle"] = &metadata.AssemblyMeta{
					ID:    "edge-bundle",
					Cells: []string{"access-core"}, // also in core-bundle
					Build: metadata.BuildMeta{
						Entrypoint:     "src/cmd/edge-bundle/main.go",
						Binary:         "edge-bundle",
						DeployTemplate: "k8s",
					},
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateTOPO06(), "TOPO-06")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

// --- VERIFY rules ---

func TestVERIFY01(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "all provider usages have verify entries",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "missing verify entry for provider",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Contract = []string{
					// removed "contract.http.auth.login.v1.serve"
					"contract.event.session.created.v1.publish",
				}
			},
			wantCount: 1,
		},
		{
			name: "waiver covers missing verify entry",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Contract = []string{
					// removed "contract.http.auth.login.v1.serve"
					"contract.event.session.created.v1.publish",
				}
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{
						Contract:  "http.auth.login.v1",
						Owner:     "platform",
						Reason:    "waiver",
						ExpiresAt: "2099-12-31",
					},
				}
			},
			wantCount: 0,
		},
		{
			name: "expired waiver does not cover",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Contract = []string{
					"contract.event.session.created.v1.publish",
				}
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{
						Contract:  "http.auth.login.v1",
						Owner:     "platform",
						Reason:    "expired",
						ExpiresAt: "2020-01-01",
					},
				}
			},
			wantCount: 1,
		},
		{
			name: "consumer role does not require verify entry",
			setup: func(pm *metadata.ProjectMeta) {
				// audit-core/audit-write has subscribe (consumer) role with no verify.contract entries
				// This should NOT trigger VERIFY-01
			},
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateVERIFY01(), "VERIFY-01")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestVERIFY02(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "no waivers",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "valid future waiver",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", ExpiresAt: "2099-12-31"},
				}
			},
			wantCount: 0,
		},
		{
			name: "expired waiver",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", ExpiresAt: "2020-01-01"},
				}
			},
			wantCount: 1,
		},
		{
			name: "invalid date format",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", ExpiresAt: "not-a-date"},
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateVERIFY02(), "VERIFY-02")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestVERIFY02_TimeOverride(t *testing.T) {
	// Verify that nowFunc override works correctly.
	original := nowFunc
	defer func() { nowFunc = original }()
	nowFunc = func() time.Time {
		return time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)
	}

	pm := validProject()
	pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
		{Contract: "http.auth.login.v1", ExpiresAt: "2026-04-04"}, // yesterday
	}

	val := NewValidator(pm, ".")
	got := findByCode(val.validateVERIFY02(), "VERIFY-02")
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Message, "expired")
}

func TestVERIFY03(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "no l0Dependencies",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "l0 dependency targets L0 cell",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["access-core"].L0Dependencies = []metadata.L0DepMeta{
					{Cell: "shared-crypto", Reason: "hashing"},
				}
			},
			wantCount: 0,
		},
		{
			name: "l0 dependency targets non-L0 cell",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["access-core"].L0Dependencies = []metadata.L0DepMeta{
					{Cell: "audit-core", Reason: "wrong"}, // audit-core is L2
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateVERIFY03(), "VERIFY-03")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

// --- FMT rules ---

func TestFMT01(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "valid lifecycle",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "invalid lifecycle",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Lifecycle = "unknown"
			},
			wantCount: 1,
		},
		{
			name: "empty lifecycle",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Lifecycle = ""
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateFMT01(), "FMT-01")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestFMT02(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "valid cell types",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "invalid cell type",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["access-core"].Type = "invalid"
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateFMT02(), "FMT-02")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestFMT03(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "valid consistency levels",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "invalid cell consistency level",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["access-core"].ConsistencyLevel = "L9"
			},
			wantCount: 1,
		},
		{
			name: "invalid contract consistency level",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].ConsistencyLevel = "bad"
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateFMT03(), "FMT-03")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestFMT04(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "event contract with all required fields",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "event contract missing replayable",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["event.session.created.v1"].Replayable = nil
			},
			wantCount: 1,
		},
		{
			name: "event contract missing idempotencyKey",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["event.session.created.v1"].IdempotencyKey = ""
			},
			wantCount: 1,
		},
		{
			name: "event contract missing deliverySemantics",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["event.session.created.v1"].DeliverySemantics = ""
			},
			wantCount: 1,
		},
		{
			name: "event contract missing all three",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["event.session.created.v1"].Replayable = nil
				pm.Contracts["event.session.created.v1"].IdempotencyKey = ""
				pm.Contracts["event.session.created.v1"].DeliverySemantics = ""
			},
			wantCount: 3,
		},
		{
			name: "http contract does not require event fields",
			setup: func(pm *metadata.ProjectMeta) {
				// http contract has no replayable/idempotencyKey/deliverySemantics and that's fine
			},
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateFMT04(), "FMT-04")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestFMT05(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "valid roles",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "invalid role string",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/bad-role"] = &metadata.SliceMeta{
					ID:            "bad-role",
					BelongsToCell: "access-core",
					ContractUsages: []metadata.ContractUsage{
						{Contract: "http.auth.login.v1", Role: "consume"}, // not a valid role
					},
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateFMT05(), "FMT-05")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

// --- ADV rules ---

func TestADV01(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "journey in status board",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "journey missing from status board",
			setup: func(pm *metadata.ProjectMeta) {
				pm.StatusBoard = nil
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateADV01(), "ADV-01")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityWarning, r.Severity)
			}
		})
	}
}

func TestADV02(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "no deprecated contracts",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "deprecated contract still used",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Lifecycle = "deprecated"
			},
			wantCount: 1, // session-login uses it
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".")
			got := findByCode(val.validateADV02(), "ADV-02")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityWarning, r.Severity)
			}
		})
	}
}

// --- helper function tests ---

func TestContractProviderAndConsumers(t *testing.T) {
	tests := []struct {
		kind          string
		endpoints     metadata.EndpointsMeta
		wantProvider  string
		wantConsumers []string
	}{
		{
			kind:          "http",
			endpoints:     metadata.EndpointsMeta{Server: "svc-a", Clients: []string{"svc-b", "svc-c"}},
			wantProvider:  "svc-a",
			wantConsumers: []string{"svc-b", "svc-c"},
		},
		{
			kind:          "event",
			endpoints:     metadata.EndpointsMeta{Publisher: "svc-a", Subscribers: []string{"svc-b"}},
			wantProvider:  "svc-a",
			wantConsumers: []string{"svc-b"},
		},
		{
			kind:          "command",
			endpoints:     metadata.EndpointsMeta{Handler: "svc-a", Invokers: []string{"svc-b"}},
			wantProvider:  "svc-a",
			wantConsumers: []string{"svc-b"},
		},
		{
			kind:          "projection",
			endpoints:     metadata.EndpointsMeta{Provider: "svc-a", Readers: []string{"svc-b"}},
			wantProvider:  "svc-a",
			wantConsumers: []string{"svc-b"},
		},
		{
			kind:          "unknown",
			endpoints:     metadata.EndpointsMeta{},
			wantProvider:  "",
			wantConsumers: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			c := &metadata.ContractMeta{Kind: tt.kind, Endpoints: tt.endpoints}
			assert.Equal(t, tt.wantProvider, contractProvider(c))
			assert.Equal(t, tt.wantConsumers, contractConsumers(c))
		})
	}
}

func TestFilePathHelpers(t *testing.T) {
	assert.Equal(t, "cells/access-core/cell.yaml", cellFile("access-core"))
	assert.Equal(t, "cells/access-core/slices/session-login/slice.yaml", sliceFile("access-core/session-login"))
	assert.Equal(t, "contracts/http/auth/login/v1/contract.yaml", contractFile("http.auth.login.v1"))
	assert.Equal(t, "journeys/J-sso-login.yaml", journeyFile("J-sso-login"))
	assert.Equal(t, "assemblies/core-bundle/assembly.yaml", assemblyFile("core-bundle"))
}

func TestSliceFileFallback(t *testing.T) {
	// If key has no slash, fallback to key itself
	assert.Equal(t, "no-slash", sliceFile("no-slash"))
}

// --- Integration: Validate() aggregation ---

func TestValidate_AggregatesAllRules(t *testing.T) {
	pm := validProject()
	// Introduce multiple issues across rule groups:
	// REF-01: bad belongsToCell
	pm.Slices["ghost/bad"] = &metadata.SliceMeta{
		ID: "bad", BelongsToCell: "ghost",
	}
	// FMT-01: bad lifecycle
	pm.Contracts["http.auth.login.v1"].Lifecycle = "unknown"
	// ADV-01: remove status board
	pm.StatusBoard = nil

	val := NewValidator(pm, ".")
	results := val.Validate()

	// Should have at least one REF-01, FMT-01, ADV-01
	assert.NotEmpty(t, findByCode(results, "REF-01"))
	assert.NotEmpty(t, findByCode(results, "FMT-01"))
	assert.NotEmpty(t, findByCode(results, "ADV-01"))

	assert.True(t, val.HasErrors(results))
	assert.NotEmpty(t, val.Errors(results))
	assert.NotEmpty(t, val.Warnings(results))
}

func TestValidate_EmptyProject(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells:      make(map[string]*metadata.CellMeta),
		Slices:     make(map[string]*metadata.SliceMeta),
		Contracts:  make(map[string]*metadata.ContractMeta),
		Journeys:   make(map[string]*metadata.JourneyMeta),
		Assemblies: make(map[string]*metadata.AssemblyMeta),
	}
	val := NewValidator(pm, ".")
	results := val.Validate()
	assert.Empty(t, results, "empty project should produce no validation results")
}
