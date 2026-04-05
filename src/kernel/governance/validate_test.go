package governance

import (
	"os"
	"path/filepath"
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
					Unit:     []string{"unit.audit-write.handler"},
					Contract: []string{"contract.event.session.created.v1.subscribe"},
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
			"projection.session.active.v1": {
				ID:               "projection.session.active.v1",
				Kind:             "projection",
				OwnerCell:        "access-core",
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Provider: "access-core",
					Readers:  []string{"audit-core"},
				},
				Replayable: &replayable,
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
	// Use empty root to skip filesystem checks (REF-11, REF-12).
	val := NewValidator(pm, "")
	results := val.Validate()
	errs := FilterErrors(results)
	assert.Empty(t, errs, "valid project should have 0 errors, got: %v", errs)
}

// --- test: HasErrors / Errors / Warnings ---

func TestFilterFunctions(t *testing.T) {
	results := []ValidationResult{
		{Code: "ERR-1", Severity: SeverityError},
		{Code: "WARN-1", Severity: SeverityWarning},
		{Code: "ERR-2", Severity: SeverityError},
		{Code: "WARN-2", Severity: SeverityWarning},
	}

	assert.True(t, HasErrors(results))
	assert.Len(t, FilterErrors(results), 2)
	assert.Len(t, FilterWarnings(results), 2)

	assert.False(t, HasErrors(FilterWarnings(results)))
	assert.False(t, HasErrors(nil))
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
		{
			name: "wildcard consumer allows any cell",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["event.session.created.v1"].Endpoints.Subscribers = []string{"*"}
				// audit-core/audit-write subscribes to this contract; "*" should match
			},
			wantCount: 0,
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
			name: "waiver with empty expiresAt does not cover",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Contract = []string{
					"contract.event.session.created.v1.publish",
				}
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{
						Contract:  "http.auth.login.v1",
						Owner:     "platform",
						Reason:    "missing-expiry",
						ExpiresAt: "",
					},
				}
			},
			wantCount: 1,
		},
		{
			name: "waiver with unparseable expiresAt does not cover",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Contract = []string{
					"contract.event.session.created.v1.publish",
				}
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{
						Contract:  "http.auth.login.v1",
						Owner:     "platform",
						Reason:    "bad-date",
						ExpiresAt: "not-a-date",
					},
				}
			},
			wantCount: 1,
		},
		{
			name: "consumer role without verify entry triggers error",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["audit-core/audit-write"].Verify.Contract = nil
			},
			wantCount: 1,
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
			name: "valid future waiver with all fields",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", Owner: "platform", Reason: "testing", ExpiresAt: "2099-12-31"},
				}
			},
			wantCount: 0,
		},
		{
			name: "expired waiver",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", Owner: "platform", Reason: "old", ExpiresAt: "2020-01-01"},
				}
			},
			wantCount: 1, // expired
		},
		{
			name: "invalid date format",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", Owner: "platform", Reason: "test", ExpiresAt: "not-a-date"},
				}
			},
			wantCount: 1, // invalid date
		},
		{
			name: "missing contract field",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "", Owner: "platform", Reason: "test", ExpiresAt: "2099-12-31"},
				}
			},
			wantCount: 1, // contract required
		},
		{
			name: "missing owner field",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", Owner: "", Reason: "test", ExpiresAt: "2099-12-31"},
				}
			},
			wantCount: 1, // owner required
		},
		{
			name: "missing reason field",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", Owner: "platform", Reason: "", ExpiresAt: "2099-12-31"},
				}
			},
			wantCount: 1, // reason required
		},
		{
			name: "missing expiresAt field",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", Owner: "platform", Reason: "test", ExpiresAt: ""},
				}
			},
			wantCount: 1, // expiresAt required
		},
		{
			name: "all fields missing",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{},
				}
			},
			wantCount: 4, // contract + owner + reason + expiresAt
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "")
			got := findByCode(val.validateVERIFY02(), "VERIFY-02")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestVERIFY02_TimeOverride(t *testing.T) {
	t.Parallel()

	pm := validProject()
	pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
		{Contract: "http.auth.login.v1", Owner: "platform", Reason: "test", ExpiresAt: "2026-04-04"}, // yesterday
	}

	val := NewValidator(pm, "")
	val.now = func() time.Time {
		return time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)
	}
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
		{
			name: "projection contract missing replayable",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["projection.session.active.v1"].Replayable = nil
			},
			wantCount: 1,
		},
		{
			name: "projection contract with replayable set",
			setup: func(pm *metadata.ProjectMeta) {
				// projection contract already has replayable in validProject
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

// --- FMT-09: contract.kind must be in {http, event, command, projection} ---

func TestFMT09(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "valid kinds",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "invalid kind grpc",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["grpc.test.v1"] = &metadata.ContractMeta{
					ID:               "grpc.test.v1",
					Kind:             "grpc",
					OwnerCell:        "access-core",
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Server: "access-core"},
				}
			},
			wantCount: 1,
		},
		{
			name: "empty kind",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["empty.kind.v1"] = &metadata.ContractMeta{
					ID:               "empty.kind.v1",
					Kind:             "",
					OwnerCell:        "access-core",
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{},
				}
			},
			wantCount: 1,
		},
		{
			name: "valid kind command",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["command.test.v1"] = &metadata.ContractMeta{
					ID:               "command.test.v1",
					Kind:             "command",
					OwnerCell:        "access-core",
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Handler: "access-core"},
				}
			},
			wantCount: 0,
		},
		{
			name: "valid kind projection",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["projection.test.v1"] = &metadata.ContractMeta{
					ID:               "projection.test.v1",
					Kind:             "projection",
					OwnerCell:        "access-core",
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Provider: "access-core"},
				}
			},
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "")
			got := findByCode(val.validateFMT09(), "FMT-09")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				assert.Equal(t, IssueInvalid, r.IssueType)
			}
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

	val := NewValidator(pm, "")
	results := val.Validate()

	// Should have at least one REF-01, FMT-01, ADV-01
	assert.NotEmpty(t, findByCode(results, "REF-01"))
	assert.NotEmpty(t, findByCode(results, "FMT-01"))
	assert.NotEmpty(t, findByCode(results, "ADV-01"))

	assert.True(t, HasErrors(results))
	assert.NotEmpty(t, FilterErrors(results))
	assert.NotEmpty(t, FilterWarnings(results))
}

func TestValidate_EmptyProject(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells:      make(map[string]*metadata.CellMeta),
		Slices:     make(map[string]*metadata.SliceMeta),
		Contracts:  make(map[string]*metadata.ContractMeta),
		Journeys:   make(map[string]*metadata.JourneyMeta),
		Assemblies: make(map[string]*metadata.AssemblyMeta),
	}
	val := NewValidator(pm, "")
	results := val.Validate()
	assert.Empty(t, results, "empty project should produce no validation results")
}

// --- FMT-06: non-L0 cell must have schema.primary ---

func TestFMT06(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "all non-L0 cells have schema.primary",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "L0 cell without schema.primary is ok",
			setup: func(pm *metadata.ProjectMeta) {
				// shared-crypto is L0 with no schema.primary — should be fine
			},
			wantCount: 0,
		},
		{
			name: "non-L0 cell without schema.primary",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["access-core"].Schema.Primary = ""
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "")
			got := findByCode(val.validateFMT06(), "FMT-06")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				assert.Equal(t, IssueRequired, r.IssueType)
			}
		})
	}
}

// --- FMT-07: contract provider endpoint required ---

func TestFMT07(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "all contracts have providers",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "http contract missing server",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.Server = ""
			},
			wantCount: 1,
		},
		{
			name: "event contract missing publisher",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["event.session.created.v1"].Endpoints.Publisher = ""
			},
			wantCount: 1,
		},
		{
			name: "command contract missing handler",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["cmd.test.v1"] = &metadata.ContractMeta{
					ID:               "cmd.test.v1",
					Kind:             "command",
					OwnerCell:        "access-core",
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Handler: ""},
				}
			},
			wantCount: 1,
		},
		{
			name: "projection contract missing provider",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["projection.test.v1"] = &metadata.ContractMeta{
					ID:               "projection.test.v1",
					Kind:             "projection",
					OwnerCell:        "access-core",
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Provider: ""},
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "")
			got := findByCode(val.validateFMT07(), "FMT-07")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				assert.Equal(t, IssueRequired, r.IssueType)
			}
		})
	}
}

// --- FMT-08: contract kind matches ID prefix ---

func TestFMT08(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(*metadata.ProjectMeta)
		wantCount     int
		wantIssueType IssueType
	}{
		{
			name:      "kind matches ID prefix",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "kind does not match ID prefix",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Kind = "event" // ID starts with "http"
			},
			wantCount:     1,
			wantIssueType: IssueMismatch,
		},
		{
			name: "contract with matching kind",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["command.test.v1"] = &metadata.ContractMeta{
					ID:               "command.test.v1",
					Kind:             "command",
					OwnerCell:        "access-core",
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Handler: "access-core"},
				}
			},
			wantCount: 0,
		},
		{
			name: "contract ID without dot separator",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["nodot"] = &metadata.ContractMeta{
					ID:               "nodot",
					Kind:             "http",
					OwnerCell:        "access-core",
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Server: "access-core"},
				}
			},
			wantCount:     1,
			wantIssueType: IssueInvalid,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "")
			got := findByCode(val.validateFMT08(), "FMT-08")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				assert.Equal(t, tt.wantIssueType, r.IssueType)
			}
		})
	}
}

// --- REF-10: assembly.build.entrypoint required ---

func TestREF10(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "assembly has entrypoint",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "assembly missing entrypoint",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Assemblies["core-bundle"].Build.Entrypoint = ""
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "")
			got := findByCode(val.validateREF10(), "REF-10")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				assert.Equal(t, IssueRequired, r.IssueType)
			}
		})
	}
}

// --- REF-11: assembly.build.entrypoint file exists ---

func TestREF11(t *testing.T) {
	t.Run("skipped when root is empty", func(t *testing.T) {
		pm := validProject()
		val := NewValidator(pm, "")
		got := findByCode(val.validateREF11(), "REF-11")
		assert.Empty(t, got)
	})

	t.Run("entrypoint file exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create the entrypoint file. repositoryRoot logic: if base is "src", go up one level.
		// So we create root as tmpDir/src, and entrypoint relative to tmpDir.
		srcDir := filepath.Join(tmpDir, "src")
		require.NoError(t, os.MkdirAll(srcDir, 0o755))
		entryDir := filepath.Join(tmpDir, "src", "cmd", "core-bundle")
		require.NoError(t, os.MkdirAll(entryDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(entryDir, "main.go"), []byte("package main"), 0o644))

		pm := validProject()
		val := NewValidator(pm, srcDir)
		got := findByCode(val.validateREF11(), "REF-11")
		assert.Empty(t, got)
	})

	t.Run("entrypoint file does not exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		srcDir := filepath.Join(tmpDir, "src")
		require.NoError(t, os.MkdirAll(srcDir, 0o755))

		pm := validProject()
		val := NewValidator(pm, srcDir)
		got := findByCode(val.validateREF11(), "REF-11")
		assert.Len(t, got, 1)
		assert.Equal(t, SeverityError, got[0].Severity)
		assert.Equal(t, IssueRefNotFound, got[0].IssueType)
	})
}

// --- REF-12: contract.schemaRefs files exist ---

func TestREF12(t *testing.T) {
	t.Run("skipped when root is empty", func(t *testing.T) {
		pm := validProject()
		pm.Contracts["http.auth.login.v1"].SchemaRefs = metadata.SchemaRefsMeta{
			Request: "request.json",
		}
		val := NewValidator(pm, "")
		got := findByCode(val.validateREF12(), "REF-12")
		assert.Empty(t, got)
	})

	t.Run("no schemaRefs is ok", func(t *testing.T) {
		tmpDir := t.TempDir()
		pm := validProject()
		val := NewValidator(pm, tmpDir)
		got := findByCode(val.validateREF12(), "REF-12")
		assert.Empty(t, got)
	})

	t.Run("schemaRef file exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create contract directory and schema file.
		// Contract "http.auth.login.v1" -> contracts/http/auth/login/v1/
		contractDir := filepath.Join(tmpDir, "contracts", "http", "auth", "login", "v1")
		require.NoError(t, os.MkdirAll(contractDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(contractDir, "request.json"), []byte("{}"), 0o644))

		pm := validProject()
		pm.Contracts["http.auth.login.v1"].SchemaRefs = metadata.SchemaRefsMeta{
			Request: "request.json",
		}
		val := NewValidator(pm, tmpDir)
		got := findByCode(val.validateREF12(), "REF-12")
		assert.Empty(t, got)
	})

	t.Run("schemaRef file missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create contract directory but not the schema file.
		contractDir := filepath.Join(tmpDir, "contracts", "http", "auth", "login", "v1")
		require.NoError(t, os.MkdirAll(contractDir, 0o755))

		pm := validProject()
		pm.Contracts["http.auth.login.v1"].SchemaRefs = metadata.SchemaRefsMeta{
			Request:  "request.json",
			Response: "response.json",
		}
		val := NewValidator(pm, tmpDir)
		got := findByCode(val.validateREF12(), "REF-12")
		assert.Len(t, got, 2) // both missing
		for _, r := range got {
			assert.Equal(t, SeverityError, r.Severity)
			assert.Equal(t, IssueRefNotFound, r.IssueType)
		}
	})

	t.Run("payload schemaRef missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		contractDir := filepath.Join(tmpDir, "contracts", "event", "session", "created", "v1")
		require.NoError(t, os.MkdirAll(contractDir, 0o755))

		pm := validProject()
		pm.Contracts["event.session.created.v1"].SchemaRefs = metadata.SchemaRefsMeta{
			Payload: "payload.json",
		}
		val := NewValidator(pm, tmpDir)
		got := findByCode(val.validateREF12(), "REF-12")
		assert.Len(t, got, 1)
		assert.Contains(t, got[0].Field, "payload")
	})
}

// --- REF-13: contract provider actor exists ---

func TestREF13(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "provider is known cell",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "provider is known actor",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.ext.v1"] = &metadata.ContractMeta{
					ID:               "http.ext.v1",
					Kind:             "http",
					OwnerCell:        "access-core",
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Server: "edge-bff"},
				}
			},
			wantCount: 0,
		},
		{
			name: "provider is unknown",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.unknown.v1"] = &metadata.ContractMeta{
					ID:               "http.unknown.v1",
					Kind:             "http",
					OwnerCell:        "access-core",
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Server: "nonexistent-service"},
				}
			},
			wantCount: 1,
		},
		{
			name: "empty provider skipped",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.noprov.v1"] = &metadata.ContractMeta{
					ID:               "http.noprov.v1",
					Kind:             "http",
					OwnerCell:        "access-core",
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Server: ""},
				}
			},
			wantCount: 0, // FMT-07 handles empty provider
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "")
			got := findByCode(val.validateREF13(), "REF-13")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				assert.Equal(t, IssueRefNotFound, r.IssueType)
			}
		})
	}
}

// --- REF-14: contract consumer actors exist ---

func TestREF14(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "all consumers are known",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "wildcard consumer skipped",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{"*"}
			},
			wantCount: 0,
		},
		{
			name: "unknown consumer",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{"unknown-service"}
			},
			wantCount: 1,
		},
		{
			name: "consumer is known actor",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{"edge-bff"}
			},
			wantCount: 0,
		},
		{
			name: "multiple unknown consumers",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{"unknown1", "unknown2"}
			},
			wantCount: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "")
			got := findByCode(val.validateREF14(), "REF-14")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				assert.Equal(t, IssueRefNotFound, r.IssueType)
			}
		})
	}
}

// --- REF-15: assembly.id == map key ---

func TestREF15(t *testing.T) {
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
				pm.Assemblies["wrong-key"] = &metadata.AssemblyMeta{
					ID:    "actual-id",
					Cells: []string{"access-core"},
					Build: metadata.BuildMeta{Entrypoint: "cmd/main.go"},
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "")
			got := findByCode(val.validateREF15(), "REF-15")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				assert.Equal(t, IssueMismatch, r.IssueType)
			}
		})
	}
}

// --- ADV-03: waiver without matching contractUsage ---

func TestADV03(t *testing.T) {
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
			name: "waiver matches contractUsage",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", Owner: "platform", Reason: "test", ExpiresAt: "2099-12-31"},
				}
			},
			wantCount: 0,
		},
		{
			name: "waiver has no matching contractUsage",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.nonexistent.v1", Owner: "platform", Reason: "orphan", ExpiresAt: "2099-12-31"},
				}
			},
			wantCount: 1,
		},
		{
			name: "waiver with empty contract skipped",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["access-core/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "", Owner: "platform", Reason: "empty", ExpiresAt: "2099-12-31"},
				}
			},
			wantCount: 0, // empty contract is skipped (VERIFY-02 catches it)
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "")
			got := findByCode(val.validateADV03(), "ADV-03")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityWarning, r.Severity)
				assert.Equal(t, IssueRefNotFound, r.IssueType)
			}
		})
	}
}

// --- ADV-04: status-board references unknown journey ---

func TestADV04(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "all status-board entries reference known journeys",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "status-board references unknown journey",
			setup: func(pm *metadata.ProjectMeta) {
				pm.StatusBoard = append(pm.StatusBoard, metadata.StatusBoardEntry{
					JourneyID: "J-nonexistent",
					State:     "doing",
				})
			},
			wantCount: 1,
		},
		{
			name: "empty status-board",
			setup: func(pm *metadata.ProjectMeta) {
				pm.StatusBoard = nil
			},
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "")
			got := findByCode(val.validateADV04(), "ADV-04")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityWarning, r.Severity)
				assert.Equal(t, IssueRefNotFound, r.IssueType)
			}
		})
	}
}

// --- helper function tests for new utilities ---

func TestContractDirFromID(t *testing.T) {
	assert.Equal(t, filepath.Join("contracts", "http", "auth", "login", "v1"), contractDirFromID("http.auth.login.v1"))
	assert.Equal(t, filepath.Join("contracts", "event", "session", "created", "v1"), contractDirFromID("event.session.created.v1"))
}

func TestRepositoryRoot(t *testing.T) {
	t.Run("root ending in src", func(t *testing.T) {
		tmpDir := t.TempDir()
		srcDir := filepath.Join(tmpDir, "src")
		require.NoError(t, os.MkdirAll(srcDir, 0o755))
		got := repositoryRoot(srcDir)
		assert.Equal(t, tmpDir, got)
	})

	t.Run("root not ending in src", func(t *testing.T) {
		tmpDir := t.TempDir()
		got := repositoryRoot(tmpDir)
		assert.Equal(t, tmpDir, got)
	})
}

func TestActorExists(t *testing.T) {
	pm := validProject()
	val := NewValidator(pm, "")

	assert.True(t, val.actorExists("access-core"), "cell should be a known actor")
	assert.True(t, val.actorExists("edge-bff"), "external actor should be known")
	assert.False(t, val.actorExists("nonexistent"), "unknown ID should not exist")
}

// --- S1: isWithinRoot path traversal guard ---

func TestIsWithinRoot(t *testing.T) {
	tests := []struct {
		name   string
		root   string
		target string
		want   bool
	}{
		{"inside root", "/project/src", "/project/src/cmd/main.go", true},
		{"equals root", "/project/src", "/project/src", true},
		{"escapes root", "/project/src", "/project/etc/passwd", false},
		{"dot-dot escapes", "/project/src", "/project/src/../etc/passwd", false},
		{"different tree", "/project/src", "/other/place", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWithinRoot(tt.root, tt.target)
			assert.Equal(t, tt.want, got)
		})
	}
}

// --- S1: REF-11 path traversal ---

func TestREF11_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src")
	require.NoError(t, os.MkdirAll(srcDir, 0o755))

	pm := validProject()
	pm.Assemblies["core-bundle"].Build.Entrypoint = "../../../etc/passwd"
	val := NewValidator(pm, srcDir)
	got := findByCode(val.validateREF11(), "REF-11")
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Message, "path escapes project root")
	assert.Equal(t, IssueInvalid, got[0].IssueType)
}

// --- S1: REF-12 path traversal ---

func TestREF12_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	contractDir := filepath.Join(tmpDir, "contracts", "http", "auth", "login", "v1")
	require.NoError(t, os.MkdirAll(contractDir, 0o755))

	pm := validProject()
	pm.Contracts["http.auth.login.v1"].SchemaRefs = metadata.SchemaRefsMeta{
		Request: "../../evil.json",
	}
	val := NewValidator(pm, tmpDir)
	got := findByCode(val.validateREF12(), "REF-12")
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Message, "path escapes project root")
	assert.Equal(t, IssueInvalid, got[0].IssueType)
}

// --- S2: NewValidator with nil project ---

func TestNewValidator_NilProject(t *testing.T) {
	val := NewValidator(nil, ".")
	require.NotNil(t, val)

	// Should not panic and should return empty results.
	results := val.Validate()
	assert.Empty(t, results)
}
