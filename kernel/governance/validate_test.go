package governance

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
	"github.com/ghbvf/gocell/kernel/verify"
)

// --- helpers ---

// validProject returns a minimal but fully valid ProjectMeta for testing.
// All references are consistent, roles match kinds, consistency levels are valid,
// and verify/waiver entries exist.
func validProject() *metadata.ProjectMeta {
	replayable := true
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDAccessCore: {
				ID:               metadatatest.CellIDAccessCore,
				Type:             "core",
				ConsistencyLevel: "L3",
				DurabilityMode:   "durable",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_access_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.accesscore.startup"}},
				Dir:              "accesscore",
				File:             "cells/accesscore/cell.yaml",
			},
			metadatatest.CellIDAuditCore: {
				ID:               metadatatest.CellIDAuditCore,
				Type:             "core",
				ConsistencyLevel: "L2",
				DurabilityMode:   "durable",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_audit_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.auditcore.startup"}},
				Dir:              "auditcore",
				File:             "cells/auditcore/cell.yaml",
			},
			metadatatest.CellIDSharedCrypto: {
				ID:               metadatatest.CellIDSharedCrypto,
				Type:             "support",
				ConsistencyLevel: "L0",
				DurabilityMode:   "demo",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.sharedcrypto.startup"}},
				Dir:              "sharedcrypto",
				File:             "cells/sharedcrypto/cell.yaml",
			},
			metadatatest.CellIDConfigCore: {
				ID:               metadatatest.CellIDConfigCore,
				Type:             "core",
				ConsistencyLevel: "L3",
				DurabilityMode:   "durable",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Schema:           metadata.SchemaMeta{Primary: "cell_config_core"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.configcore.startup"}},
				Dir:              "configcore",
				File:             "cells/configcore/cell.yaml",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/session-login": {
				ID:               "session-login",
				BelongsToCell:    metadatatest.CellIDAccessCore,
				ConsistencyLevel: "L2",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
					{Contract: "event.session.created.v1", Role: "publish"},
					{Contract: "projection.session.active.v1", Role: "provide"},
				},
				Verify: metadata.SliceVerifyMeta{
					Unit: []string{"unit.session-login.service"},
					Contract: []string{
						"contract.http.auth.login.v1.serve",
						"contract.event.session.created.v1.publish",
						"contract.projection.session.active.v1.provide",
					},
				},
				AllowedFiles: []string{
					"cells/accesscore/slices/session-login/**",
					"cells/accesscore/slices/sessionlogin/**",
				},
				Dir:     "session-login",
				CellDir: "accesscore",
				File:    "cells/accesscore/slices/session-login/slice.yaml",
			},
			"auditcore/audit-write": {
				ID:            "audit-write",
				BelongsToCell: metadatatest.CellIDAuditCore,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "event.session.created.v1", Role: "subscribe", Handler: "HandleSessionCreated"},
				},
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.audit-write.handler"},
					Contract: []string{"contract.event.session.created.v1.subscribe"},
				},
				AllowedFiles: []string{
					"cells/auditcore/slices/audit-write/**",
					"cells/auditcore/slices/auditwrite/**",
				},
				Dir:     "audit-write",
				CellDir: "auditcore",
				File:    "cells/auditcore/slices/audit-write/slice.yaml",
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.login.v1": {
				ID:               "http.auth.login.v1",
				Kind:             "http",
				OwnerCell:        metadatatest.CellIDAccessCore,
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  metadatatest.CellIDAccessCore,
					Clients: []string{metadatatest.CellIDAuditCore},
					// FMT-13 now requires endpoints.http on all HTTP contracts.
					// Use NoContent:true / 204 so no response schema file is needed,
					// keeping validProject self-contained (no filesystem deps).
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "DELETE",
						Path:          "/api/v1/access/sessions/login",
						SuccessStatus: 204,
						NoContent:     true,
					},
				},
				Dir:  "contracts/http/auth/login/v1",
				File: "contracts/http/auth/login/v1/contract.yaml",
			},
			"event.session.created.v1": {
				ID:               "event.session.created.v1",
				Kind:             "event",
				OwnerCell:        metadatatest.CellIDAccessCore,
				ConsistencyLevel: "L2",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Publisher:   metadatatest.CellIDAccessCore,
					Subscribers: []string{metadatatest.CellIDAuditCore},
				},
				Replayable:        &replayable,
				IdempotencyKey:    "session-id",
				DeliverySemantics: "at-least-once",
				Dir:               "contracts/event/session/created/v1",
				File:              "contracts/event/session/created/v1/contract.yaml",
			},
			"projection.session.active.v1": {
				ID:               "projection.session.active.v1",
				Kind:             "projection",
				OwnerCell:        metadatatest.CellIDAccessCore,
				ConsistencyLevel: "L3",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Provider: metadatatest.CellIDAccessCore,
					Readers:  []string{metadatatest.CellIDAuditCore},
				},
				Replayable: &replayable,
				Dir:        "contracts/projection/session/active/v1",
				File:       "contracts/projection/session/active/v1/contract.yaml",
			},
		},
		Journeys: map[string]*metadata.JourneyMeta{
			"J-ssologin": {
				ID:        "J-ssologin",
				Goal:      "User completes SSO login",
				Lifecycle: "active",
				Owner:     metadata.OwnerMeta{Team: "platform", Role: "journey-owner"},
				Cells:     []string{metadatatest.CellIDAccessCore, metadatatest.CellIDAuditCore},
				// All three active platform contracts (http + event + projection)
				// must be journey-referenced to satisfy JOURNEY-CONTRACT-EXISTENCE-01
				// (inverse direction of REF-07).
				Contracts: []string{
					"http.auth.login.v1",
					"event.session.created.v1",
					"projection.session.active.v1",
				},
				PassCriteria: []metadata.PassCriterion{
					{Text: "login returns token", Mode: verify.ModeAuto, CheckRef: "journey.J-ssologin.login-returns-token"},
					{Text: "manual review", Mode: verify.ModeManual},
				},
				File: "journeys/J-ssologin.yaml",
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"corebundle": {
				ID:                  "corebundle",
				Cells:               []string{metadatatest.CellIDAccessCore, metadatatest.CellIDAuditCore, metadatatest.CellIDSharedCrypto},
				MaxConsistencyLevel: "L3", // derived: max of L3, L2, L0
				Owner:               metadata.OwnerMeta{Team: "platform", Role: "assembly-owner"},
				Build: metadata.BuildMeta{
					Entrypoint:     "cmd/corebundle/main.go",
					Binary:         "corebundle",
					DeployTemplate: "k8s",
				},
				Dir:  "corebundle",
				File: "assemblies/corebundle/assembly.yaml",
			},
		},
		StatusBoard: []metadata.StatusBoardEntry{
			{JourneyID: "J-ssologin", State: "doing", Risk: "low", UpdatedAt: "2026-04-04"},
		},
		Actors: []metadata.ActorMeta{
			{ID: metadatatest.CellIDEdgeBFF, MaxConsistencyLevel: "L1"},
		},
	}
}

func verifiedJourneyRefVerifier(
	_ context.Context,
	_ *metadata.JourneyMeta,
	ref string,
) (verify.TestResult, []error) {
	return verify.TestResult{Name: ref, Passed: true}, nil
}

// findByCode returns all results matching the given code.
func findByCode(results []ValidationResult, code RuleCode) []ValidationResult {
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
	val := NewValidator(pm, "", clock.Real())
	results, err := val.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
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
					BelongsToCell: metadatatest.NewCellID("ghost"),
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
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
				pm.Slices["accesscore/bad-slice"] = &metadata.SliceMeta{
					ID:            "bad-slice",
					BelongsToCell: metadatatest.CellIDAccessCore,
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
			val := NewValidator(pm, ".", clock.Real())
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
				c := &metadata.ContractMeta{
					ID:               "http.external.v1",
					Kind:             "http",
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Server: metadatatest.CellIDEdgeBFF},
				}
				c.OwnerCell = metadatatest.CellIDEdgeBFF // actor, not a cell — non-compliant ID used to trigger REF rule
				pm.Contracts["http.external.v1"] = c
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
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
			name:      "id matches directory",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "id mismatch directory",
			setup: func(pm *metadata.ProjectMeta) {
				// Dir captures the walked filesystem name; a disagreement
				// between Dir and ID is exactly what REF-04 must flag.
				cell := &metadata.CellMeta{
					Type:             "core",
					ConsistencyLevel: "L1",
					Dir:              "wrong-dir",
				}
				cell.ID = "actual-id"
				pm.Cells["actual-id"] = cell
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
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
			name: "id mismatch directory",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/actual-name"] = &metadata.SliceMeta{
					ID:            "actual-name",
					BelongsToCell: metadatatest.CellIDAccessCore,
					Dir:           "wrong-dir",
					CellDir:       "accesscore",
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
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
				pm.Journeys["J-ssologin"].Cells = append(pm.Journeys["J-ssologin"].Cells, "nonexistent")
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
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
				pm.Journeys["J-ssologin"].Contracts = append(pm.Journeys["J-ssologin"].Contracts, "nonexistent.v1")
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
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
				pm.Assemblies["corebundle"].Cells = append(pm.Assemblies["corebundle"].Cells, "nonexistent")
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
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
				pm.Cells["accesscore"].L0Dependencies = []metadata.L0DepMeta{
					{Cell: metadatatest.CellIDSharedCrypto, Reason: "hashing"},
				}
			},
			wantCount: 0,
		},
		{
			name: "missing l0 dependency target",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].L0Dependencies = []metadata.L0DepMeta{
					{Cell: metadatatest.NewCellID("nonexistent"), Reason: "missing"},
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
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
				pm.Slices["accesscore/bad-role"] = &metadata.SliceMeta{
					ID:            "bad-role",
					BelongsToCell: metadatatest.CellIDAccessCore,
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
			val := NewValidator(pm, ".", clock.Real())
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
				pm.Slices["auditcore/wrong-provider"] = &metadata.SliceMeta{
					ID:            "wrong-provider",
					BelongsToCell: metadatatest.CellIDAuditCore,
					ContractUsages: []metadata.ContractUsage{
						{Contract: "http.auth.login.v1", Role: "serve"}, // server is accesscore
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
			val := NewValidator(pm, ".", clock.Real())
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
				// accesscore is not in the subscribers list for event.session.created.v1
				pm.Slices["accesscore/wrong-consumer"] = &metadata.SliceMeta{
					ID:            "wrong-consumer",
					BelongsToCell: metadatatest.CellIDAccessCore,
					ContractUsages: []metadata.ContractUsage{
						{Contract: "event.session.created.v1", Role: "subscribe"}, // accesscore is publisher, not subscriber
					},
				}
			},
			wantCount: 1,
		},
		{
			name: "wildcard consumer allows any cell",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["event.session.created.v1"].Endpoints.Subscribers = []string{"*"}
				// auditcore/audit-write subscribes to this contract; "*" should match
			},
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
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
				// ownerCell accesscore is L2
			},
			wantCount: 1,
		},
		{
			name: "contract level within external actor max level",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Actors = []metadata.ActorMeta{
					{ID: metadatatest.CellIDExtGateway, MaxConsistencyLevel: "L3"},
				}
				// OwnerCell is set for REF-03; TOPO-04 uses endpoints.server as provider.
				pm.Contracts["http.ext.gw.v1"] = &metadata.ContractMeta{
					ID:               "http.ext.gw.v1",
					Kind:             "http",
					OwnerCell:        metadatatest.CellIDExtGateway,
					ConsistencyLevel: "L2",
					Lifecycle:        "active",
					Endpoints: metadata.EndpointsMeta{
						Server:  metadatatest.CellIDExtGateway,
						Clients: []string{metadatatest.CellIDAccessCore},
					},
				}
			},
			wantCount: 0,
		},
		{
			name: "contract level exceeds external actor max level",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Actors = []metadata.ActorMeta{
					{ID: metadatatest.CellIDExtGateway, MaxConsistencyLevel: "L1"},
				}
				// OwnerCell is set for REF-03; TOPO-04 uses endpoints.server as provider.
				pm.Contracts["http.ext.gw.v1"] = &metadata.ContractMeta{
					ID:               "http.ext.gw.v1",
					Kind:             "http",
					OwnerCell:        metadatatest.CellIDExtGateway,
					ConsistencyLevel: "L3",
					Lifecycle:        "active",
					Endpoints: metadata.EndpointsMeta{
						Server:  metadatatest.CellIDExtGateway,
						Clients: []string{metadatatest.CellIDAccessCore},
					},
				}
			},
			wantCount: 1,
		},
		{
			name: "external actor with malformed maxConsistencyLevel reports error",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Actors = []metadata.ActorMeta{
					{ID: metadatatest.CellIDExtGateway, MaxConsistencyLevel: "INVALID"},
				}
				// OwnerCell is set for REF-03; TOPO-04 uses endpoints.server as provider.
				pm.Contracts["http.ext.gw.v1"] = &metadata.ContractMeta{
					ID:               "http.ext.gw.v1",
					Kind:             "http",
					OwnerCell:        metadatatest.CellIDExtGateway,
					ConsistencyLevel: "L2",
					Lifecycle:        "active",
					Endpoints: metadata.EndpointsMeta{
						Server:  metadatatest.CellIDExtGateway,
						Clients: []string{metadatatest.CellIDAccessCore},
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
			val := NewValidator(pm, ".", clock.Real())
			got := findByCode(val.validateTOPO04(), "TOPO-04")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestTOPO04_EmptyMaxConsistencyLevel(t *testing.T) {
	pm := validProject()
	pm.Actors = []metadata.ActorMeta{
		{ID: metadatatest.CellIDExtGateway, MaxConsistencyLevel: ""},
	}
	pm.Contracts["http.ext.gw.v1"] = &metadata.ContractMeta{
		ID:               "http.ext.gw.v1",
		Kind:             "http",
		OwnerCell:        metadatatest.CellIDExtGateway,
		ConsistencyLevel: "L2",
		Lifecycle:        "active",
		Endpoints:        metadata.EndpointsMeta{Server: metadatatest.CellIDExtGateway, Clients: []string{metadatatest.CellIDAccessCore}},
	}

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateTOPO04(), "TOPO-04")
	assert.Empty(t, got, "empty maxConsistencyLevel should mean unconstrained, not malformed")
}

func TestTOPO04_MalformedMessage(t *testing.T) {
	pm := validProject()
	pm.Actors = []metadata.ActorMeta{
		{ID: metadatatest.CellIDExtGateway, MaxConsistencyLevel: "INVALID"},
	}
	pm.Contracts["http.ext.gw.v1"] = &metadata.ContractMeta{
		ID:               "http.ext.gw.v1",
		Kind:             "http",
		OwnerCell:        metadatatest.CellIDExtGateway,
		ConsistencyLevel: "L2",
		Lifecycle:        "active",
		Endpoints:        metadata.EndpointsMeta{Server: metadatatest.CellIDExtGateway, Clients: []string{metadatatest.CellIDAccessCore}},
	}

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateTOPO04(), "TOPO-04")
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Message, "INVALID")
	assert.Contains(t, got[0].Message, "must be L0-L4")
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
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L0",
					Lifecycle:        "active",
					Endpoints: metadata.EndpointsMeta{
						Server:  metadatatest.CellIDSharedCrypto, // L0 cell
						Clients: []string{metadatatest.CellIDAccessCore},
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
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L0",
					Lifecycle:        "active",
					Endpoints: metadata.EndpointsMeta{
						Server:  metadatatest.CellIDAccessCore,
						Clients: []string{metadatatest.CellIDSharedCrypto}, // L0 cell
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
			val := NewValidator(pm, ".", clock.Real())
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
					Cells: []string{metadatatest.CellIDAccessCore}, // also in corebundle
					Build: metadata.BuildMeta{
						Entrypoint:     "cmd/edge-bundle/main.go",
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
			val := NewValidator(pm, ".", clock.Real())
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
				pm.Slices["accesscore/session-login"].Verify.Contract = []string{
					// removed "contract.http.auth.login.v1.serve"
					"contract.event.session.created.v1.publish",
					"contract.projection.session.active.v1.provide",
				}
			},
			wantCount: 1,
		},
		{
			name: "waiver covers missing verify entry",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Contract = []string{
					// removed "contract.http.auth.login.v1.serve"
					"contract.event.session.created.v1.publish",
					"contract.projection.session.active.v1.provide",
				}
				pm.Slices["accesscore/session-login"].Verify.Waivers = []metadata.WaiverMeta{
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
				pm.Slices["accesscore/session-login"].Verify.Contract = []string{
					"contract.event.session.created.v1.publish",
					"contract.projection.session.active.v1.provide",
				}
				pm.Slices["accesscore/session-login"].Verify.Waivers = []metadata.WaiverMeta{
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
				pm.Slices["accesscore/session-login"].Verify.Contract = []string{
					"contract.event.session.created.v1.publish",
					"contract.projection.session.active.v1.provide",
				}
				pm.Slices["accesscore/session-login"].Verify.Waivers = []metadata.WaiverMeta{
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
				pm.Slices["accesscore/session-login"].Verify.Contract = []string{
					"contract.event.session.created.v1.publish",
					"contract.projection.session.active.v1.provide",
				}
				pm.Slices["accesscore/session-login"].Verify.Waivers = []metadata.WaiverMeta{
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
				pm.Slices["auditcore/audit-write"].Verify.Contract = nil
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
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
				pm.Slices["accesscore/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", Owner: "platform", Reason: "testing", ExpiresAt: "2099-12-31"},
				}
			},
			wantCount: 0,
		},
		{
			name: "expired waiver",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", Owner: "platform", Reason: "old", ExpiresAt: "2020-01-01"},
				}
			},
			wantCount: 1, // expired
		},
		{
			name: "invalid date format",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", Owner: "platform", Reason: "test", ExpiresAt: "not-a-date"},
				}
			},
			wantCount: 1, // invalid date
		},
		{
			name: "missing contract field",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "", Owner: "platform", Reason: "test", ExpiresAt: "2099-12-31"},
				}
			},
			wantCount: 1, // contract required
		},
		{
			name: "missing owner field",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", Owner: "", Reason: "test", ExpiresAt: "2099-12-31"},
				}
			},
			wantCount: 1, // owner required
		},
		{
			name: "missing reason field",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", Owner: "platform", Reason: "", ExpiresAt: "2099-12-31"},
				}
			},
			wantCount: 1, // reason required
		},
		{
			name: "missing expiresAt field",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", Owner: "platform", Reason: "test", ExpiresAt: ""},
				}
			},
			wantCount: 1, // expiresAt required
		},
		{
			name: "all fields missing",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Waivers = []metadata.WaiverMeta{
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
			val := NewValidator(pm, "", clock.Real())
			got := findByCode(val.validateVERIFY02(), "VERIFY-02")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestVERIFY02_TimeOverride(t *testing.T) {
	t.Parallel()

	pm := validProject()
	pm.Slices["accesscore/session-login"].Verify.Waivers = []metadata.WaiverMeta{
		{Contract: "http.auth.login.v1", Owner: "platform", Reason: "test", ExpiresAt: "2026-04-04"}, // yesterday
	}

	val := NewValidator(pm, "", clockmock.New(time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC)))
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
				pm.Cells["accesscore"].L0Dependencies = []metadata.L0DepMeta{
					{Cell: metadatatest.CellIDSharedCrypto, Reason: "hashing"},
				}
			},
			wantCount: 0,
		},
		{
			name: "l0 dependency targets non-L0 cell",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].L0Dependencies = []metadata.L0DepMeta{
					{Cell: metadatatest.CellIDAuditCore, Reason: "wrong"}, // auditcore is L2
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
			got := findByCode(val.validateVERIFY03(), "VERIFY-03")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestVERIFY04(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "active contract with provider-role slice passes",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "active contract without provider-role slice fails",
			setup: func(pm *metadata.ProjectMeta) {
				// Remove the provide usage from the only slice that provides this contract.
				s := pm.Slices["accesscore/session-login"]
				s.ContractUsages = []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
					{Contract: "event.session.created.v1", Role: "publish"},
					// removed: projection.session.active.v1 provide
				}
				s.Verify.Contract = []string{
					"contract.http.auth.login.v1.serve",
					"contract.event.session.created.v1.publish",
				}
			},
			wantCount: 1, // projection.session.active.v1 has no provider slice
		},
		{
			name: "draft contract without provider-role slice is OK",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["projection.session.active.v1"].Lifecycle = "draft"
				s := pm.Slices["accesscore/session-login"]
				s.ContractUsages = []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
					{Contract: "event.session.created.v1", Role: "publish"},
				}
				s.Verify.Contract = []string{
					"contract.http.auth.login.v1.serve",
					"contract.event.session.created.v1.publish",
				}
			},
			wantCount: 0,
		},
		{
			name: "active contract with external actor provider is skipped",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Actors = []metadata.ActorMeta{
					{ID: metadatatest.CellIDExtGateway, MaxConsistencyLevel: "L3"},
				}
				pm.Contracts["http.ext.gateway.v1"] = &metadata.ContractMeta{
					ID:               "http.ext.gateway.v1",
					Kind:             "http",
					OwnerCell:        metadatatest.CellIDExtGateway,
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints: metadata.EndpointsMeta{
						Server:  metadatatest.CellIDExtGateway, // actor, not a cell
						Clients: []string{metadatatest.CellIDAccessCore},
					},
				}
			},
			wantCount: 0, // actor-backed contracts are not checked
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
			got := findByCode(val.validateVERIFY04(), "VERIFY-04")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestVERIFY05(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "all refs valid format",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "smoke ref missing third segment",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].Verify.Smoke = []string{"smoke.accesscore"}
			},
			wantCount: 1,
		},
		{
			name: "unknown prefix",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].Verify.Smoke = []string{"integration.accesscore.startup"}
			},
			wantCount: 1,
		},
		{
			name: "smoke ref references non-existent cell",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].Verify.Smoke = []string{"smoke.nonexistent-cell.startup"}
			},
			wantCount: 1,
		},
		{
			name: "unit ref valid format",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Unit = []string{"unit.session-login.service"}
			},
			wantCount: 0,
		},
		{
			name: "unit ref too few segments",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Unit = []string{"unit.service"}
			},
			wantCount: 1,
		},
		{
			name: "contract ref valid format",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Contract = []string{
					"contract.http.auth.login.v1.serve",
					"contract.event.session.created.v1.publish",
					"contract.projection.session.active.v1.provide",
				}
			},
			wantCount: 0,
		},
		{
			name: "journey checkRef valid format",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Journeys["J-ssologin"].PassCriteria = []metadata.PassCriterion{
					{Text: "login ok", Mode: verify.ModeAuto, CheckRef: "journey.J-ssologin.login-ok"},
				}
			},
			wantCount: 0,
		},
		{
			name: "journey checkRef invalid prefix",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Journeys["J-ssologin"].PassCriteria = []metadata.PassCriterion{
					{Text: "login ok", Mode: verify.ModeAuto, CheckRef: "unknown.J-ssologin.login-ok"},
				}
			},
			wantCount: 1,
		},
		{
			name: "journey checkRef too few segments",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Journeys["J-ssologin"].PassCriteria = []metadata.PassCriterion{
					{Text: "login ok", Mode: verify.ModeAuto, CheckRef: "journey.login"},
				}
			},
			wantCount: 1,
		},
		{
			name: "empty scope segment rejected",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Unit = []string{"unit..service"}
			},
			wantCount: 1,
		},
		{
			name: "empty checkRef is skipped",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Journeys["J-ssologin"].PassCriteria = []metadata.PassCriterion{
					{Text: "manual step", Mode: verify.ModeManual, CheckRef: ""},
				}
			},
			wantCount: 0,
		},
		{
			name: "leading dot ref rejected as empty prefix",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].Verify.Smoke = []string{".foo.bar"}
			},
			wantCount: 1,
		},
		{
			name: "multiple invalid refs accumulate",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].Verify.Smoke = []string{"bad.ref"}
				pm.Slices["accesscore/session-login"].Verify.Unit = []string{"also-bad"}
			},
			wantCount: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
			got := findByCode(val.validateVERIFY05(), "VERIFY-05")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestVERIFY06(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		verifier  func(context.Context, *metadata.JourneyMeta, string) (verify.TestResult, []error)
		wantCount int
	}{
		{
			name:      "active journey with auto check passes",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "active journey with stale auto check fails strict",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Journeys["J-ssologin"].PassCriteria = []metadata.PassCriterion{
					{Text: "stale check", Mode: verify.ModeAuto, CheckRef: "journey.J-ssologin.missing"},
				}
			},
			verifier: func(_ context.Context, _ *metadata.JourneyMeta, ref string) (verify.TestResult, []error) {
				return verify.TestResult{Name: ref, Passed: false, ZeroMatch: true}, nil
			},
			wantCount: 1,
		},
		{
			name: "active journey with only manual criteria fails strict",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Journeys["J-ssologin"].PassCriteria = []metadata.PassCriterion{
					{Text: "security signoff", Mode: verify.ModeManual},
				}
			},
			wantCount: 1,
		},
		{
			name: "active journey auto criterion without checkRef does not count",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Journeys["J-ssologin"].PassCriteria = []metadata.PassCriterion{
					{Text: "unwired check", Mode: verify.ModeAuto},
				}
			},
			wantCount: 1,
		},
		{
			name: "active journey cannot borrow another journey checkRef",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Journeys["J-ssologin"].PassCriteria = []metadata.PassCriterion{
					{Text: "other journey check", Mode: verify.ModeAuto, CheckRef: "journey.J-other.session-db"},
				}
			},
			wantCount: 1,
		},
		{
			name: "experimental journey with only manual criteria passes",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Journeys["J-ssologin"].Lifecycle = "experimental"
				pm.Journeys["J-ssologin"].PassCriteria = []metadata.PassCriterion{
					{Text: "explore user flow", Mode: verify.ModeManual},
				}
			},
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
			val.verifyJourneyRef = verifiedJourneyRefVerifier
			if tt.verifier != nil {
				val.verifyJourneyRef = tt.verifier
			}
			got := findByCode(val.validateVERIFY06(t.Context()), "VERIFY-06")
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
			val := NewValidator(pm, ".", clock.Real())
			got := findByCode(val.validateFMT01(), "FMT-01")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

func TestFMT24(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "valid active journey",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "missing journey lifecycle",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Journeys["J-ssologin"].Lifecycle = ""
			},
			wantCount: 1,
		},
		{
			name: "invalid journey lifecycle",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Journeys["J-ssologin"].Lifecycle = "deprecated"
			},
			wantCount: 1,
		},
		{
			name: "invalid pass criterion mode",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Journeys["J-ssologin"].PassCriteria = []metadata.PassCriterion{
					{Text: "broken", Mode: "sometimes", CheckRef: "journey.J-ssologin.broken"},
				}
			},
			wantCount: 1,
		},
		{
			name: "auto criterion requires checkRef",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Journeys["J-ssologin"].PassCriteria = []metadata.PassCriterion{
					{Text: "unwired", Mode: verify.ModeAuto},
				}
			},
			wantCount: 1,
		},
		{
			name: "manual criterion must not carry checkRef",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Journeys["J-ssologin"].PassCriteria = []metadata.PassCriterion{
					{Text: "manual signoff", Mode: verify.ModeManual, CheckRef: "journey.J-ssologin.signoff"},
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
			got := findByCode(val.validateFMT24(), "FMT-24")
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
				pm.Cells["accesscore"].Type = "invalid"
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
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
				pm.Cells["accesscore"].ConsistencyLevel = "L9"
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
			val := NewValidator(pm, ".", clock.Real())
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
			val := NewValidator(pm, ".", clock.Real())
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
				pm.Slices["accesscore/bad-role"] = &metadata.SliceMeta{
					ID:            "bad-role",
					BelongsToCell: metadatatest.CellIDAccessCore,
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
			val := NewValidator(pm, ".", clock.Real())
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
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Server: metadatatest.CellIDAccessCore},
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
					OwnerCell:        metadatatest.CellIDAccessCore,
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
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Handler: metadatatest.CellIDAccessCore},
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
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Provider: metadatatest.CellIDAccessCore},
				}
			},
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "", clock.Real())
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
		{
			name: "examples/ journey exempt from platform board requirement",
			setup: func(pm *metadata.ProjectMeta) {
				// Add an example journey with no status-board entry.
				pm.Journeys["J-ordercreate"] = &metadata.JourneyMeta{
					ID:        "J-ordercreate",
					Lifecycle: "active",
					Owner:     metadata.OwnerMeta{Team: "examples", Role: "journey-owner"},
					File:      "examples/todoorder/journeys/J-ordercreate.yaml",
				}
			},
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
			got := findByCode(val.validateADV01(), "ADV-01")
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
			kind: "http",
			endpoints: metadata.EndpointsMeta{
				Server:  metadatatest.CellIDSvcA,
				Clients: []string{metadatatest.CellIDSvcB, metadatatest.NewCellID("svcc")},
			},
			wantProvider:  metadatatest.CellIDSvcA,
			wantConsumers: []string{metadatatest.CellIDSvcB, metadatatest.NewCellID("svcc")},
		},
		{
			kind:          "event",
			endpoints:     metadata.EndpointsMeta{Publisher: metadatatest.CellIDSvcA, Subscribers: []string{metadatatest.CellIDSvcB}},
			wantProvider:  metadatatest.CellIDSvcA,
			wantConsumers: []string{metadatatest.CellIDSvcB},
		},
		{
			kind:          "command",
			endpoints:     metadata.EndpointsMeta{Handler: metadatatest.CellIDSvcA, Invokers: []string{metadatatest.CellIDSvcB}},
			wantProvider:  metadatatest.CellIDSvcA,
			wantConsumers: []string{metadatatest.CellIDSvcB},
		},
		{
			kind:          "projection",
			endpoints:     metadata.EndpointsMeta{Provider: metadatatest.CellIDSvcA, Readers: []string{metadatatest.CellIDSvcB}},
			wantProvider:  metadatatest.CellIDSvcA,
			wantConsumers: []string{metadatatest.CellIDSvcB},
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
	assert.Equal(t, "cells/accesscore/cell.yaml", cellFile(&metadata.CellMeta{File: "cells/accesscore/cell.yaml"}))
	assert.Equal(t, "cells/accesscore/slices/session-login/slice.yaml",
		sliceFile(&metadata.SliceMeta{File: "cells/accesscore/slices/session-login/slice.yaml"}))
	assert.Equal(t, "contracts/http/auth/login/v1/contract.yaml",
		contractFile(&metadata.ContractMeta{File: "contracts/http/auth/login/v1/contract.yaml"}))
	assert.Equal(t, "journeys/J-ssologin.yaml", journeyFile(&metadata.JourneyMeta{File: "journeys/J-ssologin.yaml"}))
	assert.Equal(t, "assemblies/corebundle/assembly.yaml", assemblyFile(&metadata.AssemblyMeta{File: "assemblies/corebundle/assembly.yaml"}))
}

func TestFilePathHelpersNilSafety(t *testing.T) {
	assert.Equal(t, "", cellFile(nil))
	assert.Equal(t, "", sliceFile(nil))
	assert.Equal(t, "", contractFile(nil))
	assert.Equal(t, "", journeyFile(nil))
	assert.Equal(t, "", assemblyFile(nil))
}

func TestContractFileFromID(t *testing.T) {
	assert.Equal(t, "contracts/http/auth/login/v1/contract.yaml", contractFileFromID("http.auth.login.v1"))
}

// --- Integration: Validate() aggregation ---

func TestValidate_AggregatesAllRules(t *testing.T) {
	pm := validProject()
	// Introduce multiple issues across rule groups:
	// REF-01: bad belongsToCell
	pm.Slices["ghost/bad"] = &metadata.SliceMeta{
		ID: "bad", BelongsToCell: metadatatest.NewCellID("ghost"),
	}
	// FMT-01: bad lifecycle
	pm.Contracts["http.auth.login.v1"].Lifecycle = "unknown"
	// ADV-01: remove status board
	pm.StatusBoard = nil

	val := NewValidator(pm, "", clock.Real())
	results, err := val.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)

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
	val := NewValidator(pm, "", clock.Real())
	results, err := val.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
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
				// sharedcrypto is L0 with no schema.primary — should be fine
			},
			wantCount: 0,
		},
		{
			name: "non-L0 cell without schema.primary",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].Schema.Primary = ""
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "", clock.Real())
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
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{},
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
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{},
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "", clock.Real())
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
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Handler: metadatatest.CellIDAccessCore},
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
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Server: metadatatest.CellIDAccessCore},
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
			val := NewValidator(pm, "", clock.Real())
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
				pm.Assemblies["corebundle"].Build.Entrypoint = ""
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "", clock.Real())
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
		val := NewValidator(pm, "", clock.Real())
		got := findByCode(val.validateREF11(), "REF-11")
		assert.Empty(t, got)
	})

	t.Run("entrypoint file exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create the entrypoint file under the project root.
		srcDir := filepath.Join(tmpDir, "src")
		require.NoError(t, os.MkdirAll(srcDir, 0o755))
		entryDir := filepath.Join(tmpDir, "src", "cmd", "corebundle")
		require.NoError(t, os.MkdirAll(entryDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(entryDir, "main.go"), []byte("package main"), 0o644))

		pm := validProject()
		val := NewValidator(pm, srcDir, clock.Real())
		got := findByCode(val.validateREF11(), "REF-11")
		assert.Empty(t, got)
	})

	t.Run("entrypoint file does not exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		srcDir := filepath.Join(tmpDir, "src")
		require.NoError(t, os.MkdirAll(srcDir, 0o755))

		pm := validProject()
		val := NewValidator(pm, srcDir, clock.Real())
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
		val := NewValidator(pm, "", clock.Real())
		got := findByCode(val.validateREF12(), "REF-12")
		assert.Empty(t, got)
	})

	t.Run("no schemaRefs is ok", func(t *testing.T) {
		tmpDir := t.TempDir()
		pm := validProject()
		val := NewValidator(pm, tmpDir, clock.Real())
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
		val := NewValidator(pm, tmpDir, clock.Real())
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
		val := NewValidator(pm, tmpDir, clock.Real())
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
		val := NewValidator(pm, tmpDir, clock.Real())
		got := findByCode(val.validateREF12(), "REF-12")
		assert.Len(t, got, 1)
		assert.Contains(t, got[0].Field, "payload")
	})

	t.Run("extra schemaRef key missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		contractDir := filepath.Join(tmpDir, "contracts", "http", "auth", "login", "v1")
		require.NoError(t, os.MkdirAll(contractDir, 0o755))

		pm := validProject()
		pm.Contracts["http.auth.login.v1"].SchemaRefs = metadata.SchemaRefsMeta{
			Extra: map[string]string{"custom": "custom-schema.json"},
		}
		val := NewValidator(pm, tmpDir, clock.Real())
		got := findByCode(val.validateREF12(), "REF-12")
		require.Len(t, got, 1)
		assert.Equal(t, "schemaRefs.custom", got[0].Field)
		assert.Equal(t, IssueRefNotFound, got[0].IssueType)
	})

	t.Run("extra schemaRef key exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		contractDir := filepath.Join(tmpDir, "contracts", "http", "auth", "login", "v1")
		require.NoError(t, os.MkdirAll(contractDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(contractDir, "custom-schema.json"), []byte("{}"), 0o644))

		pm := validProject()
		pm.Contracts["http.auth.login.v1"].SchemaRefs = metadata.SchemaRefsMeta{
			Extra: map[string]string{"custom": "custom-schema.json"},
		}
		val := NewValidator(pm, tmpDir, clock.Real())
		got := findByCode(val.validateREF12(), "REF-12")
		assert.Empty(t, got)
	})

	t.Run("responses schemaRef missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		contractDir := filepath.Join(tmpDir, "contracts", "http", "auth", "login", "v1")
		require.NoError(t, os.MkdirAll(contractDir, 0o755))
		// Directory exists but nonexistent-401.schema.json is not written.

		pm := validProject()
		pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
			Method:        "POST",
			Path:          "/api/v1/auth/login",
			SuccessStatus: 200,
			Responses: map[int]metadata.HTTPResponseMeta{
				401: {Description: "unauthorized", SchemaRef: "nonexistent-401.schema.json"},
			},
		}
		val := NewValidator(pm, tmpDir, clock.Real())
		got := findByCode(val.validateREF12(), "REF-12")
		require.Len(t, got, 1)
		assert.Equal(t, "endpoints.http.responses[401].schemaRef", got[0].Field)
		assert.Equal(t, IssueRefNotFound, got[0].IssueType)
		assert.Equal(t, SeverityError, got[0].Severity)
	})

	t.Run("responses schemaRef empty is skipped", func(t *testing.T) {
		tmpDir := t.TempDir()
		contractDir := filepath.Join(tmpDir, "contracts", "http", "auth", "login", "v1")
		require.NoError(t, os.MkdirAll(contractDir, 0o755))

		pm := validProject()
		pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
			Method:        "POST",
			Path:          "/api/v1/auth/login",
			SuccessStatus: 200,
			Responses: map[int]metadata.HTTPResponseMeta{
				401: {Description: "unauthorized", SchemaRef: ""},
			},
		}
		val := NewValidator(pm, tmpDir, clock.Real())
		got := findByCode(val.validateREF12(), "REF-12")
		assert.Empty(t, got)
	})

	t.Run("responses schemaRef file exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		contractDir := filepath.Join(tmpDir, "contracts", "http", "auth", "login", "v1")
		require.NoError(t, os.MkdirAll(contractDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(contractDir, "error-401.schema.json"), []byte("{}"), 0o644))

		pm := validProject()
		pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
			Method:        "POST",
			Path:          "/api/v1/auth/login",
			SuccessStatus: 200,
			Responses: map[int]metadata.HTTPResponseMeta{
				401: {Description: "unauthorized", SchemaRef: "error-401.schema.json"},
			},
		}
		val := NewValidator(pm, tmpDir, clock.Real())
		got := findByCode(val.validateREF12(), "REF-12")
		assert.Empty(t, got)
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
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Server: metadatatest.CellIDEdgeBFF},
				}
			},
			wantCount: 0,
		},
		{
			name: "provider is unknown",
			setup: func(pm *metadata.ProjectMeta) {
				c := &metadata.ContractMeta{
					ID:               "http.unknown.v1",
					Kind:             "http",
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
				}
				c.Endpoints.Server = "nonexistent-service"
				pm.Contracts["http.unknown.v1"] = c
			},
			wantCount: 1,
		},
		{
			name: "empty provider skipped",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.noprov.v1"] = &metadata.ContractMeta{
					ID:               "http.noprov.v1",
					Kind:             "http",
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{},
				}
			},
			wantCount: 0, // FMT-07 handles empty provider
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "", clock.Real())
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
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{metadatatest.CellIDEdgeBFF}
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
			val := NewValidator(pm, "", clock.Real())
			got := findByCode(val.validateREF14(), "REF-14")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				assert.Equal(t, IssueRefNotFound, r.IssueType)
			}
		})
	}
}

// --- REF-17: contract clients audience (internal path → no external actor) ---

func TestREF17(t *testing.T) {
	// Helper: switch http.auth.login.v1 to internal path so we can drive
	// the audience check from validProject() without standing up a new contract.
	withInternalPath := func(pm *metadata.ProjectMeta) {
		pm.Contracts["http.auth.login.v1"].Endpoints.HTTP.Path = "/internal/v1/access/sessions/login"
	}
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "public path with external client passes",
			setup:     func(_ *metadata.ProjectMeta) {}, // validProject default: /api/v1/... + clients=[auditcore]
			wantCount: 0,
		},
		{
			name: "public path with external client still passes",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{metadatatest.CellIDEdgeBFF}
			},
			wantCount: 0,
		},
		{
			name: "internal path with cell client passes",
			setup: func(pm *metadata.ProjectMeta) {
				withInternalPath(pm)
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{"auditcore"}
			},
			wantCount: 0,
		},
		{
			name: "internal path with wildcard client errors (fail-closed)",
			setup: func(pm *metadata.ProjectMeta) {
				withInternalPath(pm)
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{"*"}
			},
			wantCount: 1, // wildcard admits external actors → forbidden on internal
		},
		{
			name: "internal path with empty clients passes",
			setup: func(pm *metadata.ProjectMeta) {
				withInternalPath(pm)
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = nil
			},
			wantCount: 0,
		},
		{
			name: "internal path with single external client errors",
			setup: func(pm *metadata.ProjectMeta) {
				withInternalPath(pm)
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{metadatatest.CellIDEdgeBFF}
			},
			wantCount: 1,
		},
		{
			name: "internal path with multiple external clients errors per client",
			setup: func(pm *metadata.ProjectMeta) {
				withInternalPath(pm)
				pm.Actors = append(pm.Actors, metadata.ActorMeta{
					ID: "second-bff", MaxConsistencyLevel: "L2",
				})
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{metadatatest.CellIDEdgeBFF, "second-bff"}
			},
			wantCount: 2,
		},
		{
			name: "internal path mixing external and cell clients reports only external",
			setup: func(pm *metadata.ProjectMeta) {
				withInternalPath(pm)
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{"auditcore", metadatatest.CellIDEdgeBFF}
			},
			wantCount: 1,
		},
		{
			name: "non-http contract with internal-looking client is skipped",
			setup: func(pm *metadata.ProjectMeta) {
				// Event contract has no HTTP; REF-17 must not touch it even if
				// subscribers/clients shape happens to include external actors.
				pm.Contracts["event.session.created.v1"].Endpoints.Subscribers = []string{metadatatest.CellIDEdgeBFF}
			},
			wantCount: 0,
		},
		{
			name: "http contract without HTTP transport is skipped",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = nil
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{metadatatest.CellIDEdgeBFF}
			},
			wantCount: 0,
		},
		{
			name: "internal path with newly-registered actor errors (membership = external)",
			setup: func(pm *metadata.ProjectMeta) {
				withInternalPath(pm)
				// actors.yaml membership IS the type declaration; any new entry
				// is external by construction (see ActorMeta godoc).
				pm.Actors = append(pm.Actors, metadata.ActorMeta{ID: "new-actor"})
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{"new-actor"}
			},
			wantCount: 1,
		},
		{
			name: "internal path with wildcard and external client errors on both",
			setup: func(pm *metadata.ProjectMeta) {
				withInternalPath(pm)
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{"*", metadatatest.CellIDEdgeBFF}
			},
			wantCount: 2, // both fail-closed: wildcard + external actor
		},
		{
			name: "non-v1 internal-looking path is not flagged (uses metadata.IsInternalHTTPPath SoR)",
			setup: func(pm *metadata.ProjectMeta) {
				// /internal/foo (no /v1/) is NOT routed to InternalListener by
				// runtime — REF-17 must align with the runtime SoR and not
				// flag such paths as internal.
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP.Path = "/internal/foo"
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{metadatatest.CellIDEdgeBFF}
			},
			wantCount: 0,
		},
		{
			name: "bare /internal/v1 with external client errors (no trailing slash)",
			setup: func(pm *metadata.ProjectMeta) {
				// metadata.IsInternalHTTPPath matches both "/internal/v1" and
				// "/internal/v1/...". Earlier REF-17 inlined
				// strings.HasPrefix(path, cellvocab.InternalPathPrefix) which
				// only matched the trailing-slash form — a bare root
				// declaration could ship an external actor as a client and
				// silently bypass the audience check.
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP.Path = "/internal/v1"
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{metadatatest.CellIDEdgeBFF}
			},
			wantCount: 1,
		},
		{
			name: "/internal/v10 substring trap is not flagged",
			setup: func(pm *metadata.ProjectMeta) {
				// IsInternalHTTPPath must not over-match version-extension
				// paths like /internal/v10/...; mirrors FMT-31's substring
				// trap test so the predicate semantics are double-anchored.
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP.Path = "/internal/v10/foo"
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{metadatatest.CellIDEdgeBFF}
			},
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "", clock.Real())
			got := findByCode(val.validateREF17(), "REF-17")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				assert.Equal(t, IssueForbidden, r.IssueType)
				assert.Contains(t, r.Field, "endpoints.clients")
				assert.Contains(t, r.Message, "internal")
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
					Cells: []string{metadatatest.CellIDAccessCore},
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
			val := NewValidator(pm, "", clock.Real())
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
				pm.Slices["accesscore/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.auth.login.v1", Owner: "platform", Reason: "test", ExpiresAt: "2099-12-31"},
				}
			},
			wantCount: 0,
		},
		{
			name: "waiver has no matching contractUsage",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Waivers = []metadata.WaiverMeta{
					{Contract: "http.nonexistent.v1", Owner: "platform", Reason: "orphan", ExpiresAt: "2099-12-31"},
				}
			},
			wantCount: 1,
		},
		{
			name: "waiver with empty contract skipped",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Waivers = []metadata.WaiverMeta{
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
			val := NewValidator(pm, "", clock.Real())
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
			val := NewValidator(pm, "", clock.Real())
			got := findByCode(val.validateADV04(), "ADV-04")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityWarning, r.Severity)
				assert.Equal(t, IssueRefNotFound, r.IssueType)
			}
		})
	}
}

// --- ADV-05: active event contract with no subscribers ---

func TestADV05(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
		wantSev   Severity
	}{
		{
			name: "event contract active with empty subscribers → 1 warning",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["event.dead.nosubscribers.v1"] = &metadata.ContractMeta{
					ID:               "event.dead.nosubscribers.v1",
					Kind:             "event",
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L2",
					Lifecycle:        "active",
					Endpoints: metadata.EndpointsMeta{
						Publisher:   metadatatest.CellIDAccessCore,
						Subscribers: []string{},
					},
				}
			},
			wantCount: 1,
			wantSev:   SeverityWarning,
		},
		{
			name: "event contract active with nil subscribers → 1 warning",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["event.dead.nilsubs.v1"] = &metadata.ContractMeta{
					ID:               "event.dead.nilsubs.v1",
					Kind:             "event",
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L2",
					Lifecycle:        "active",
					Endpoints: metadata.EndpointsMeta{
						Publisher:   metadatatest.CellIDAccessCore,
						Subscribers: nil,
					},
				}
			},
			wantCount: 1,
			wantSev:   SeverityWarning,
		},
		{
			name: "event contract active with subscribers → 0 findings",
			setup: func(pm *metadata.ProjectMeta) {
				// event.session.created.v1 already has subscribers in validProject()
			},
			wantCount: 0,
		},
		{
			name: "event contract deprecated with empty subscribers → 0 findings (exempt)",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["event.old.deprecated.v1"] = &metadata.ContractMeta{
					ID:               "event.old.deprecated.v1",
					Kind:             "event",
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L2",
					Lifecycle:        "deprecated",
					Endpoints: metadata.EndpointsMeta{
						Publisher:   metadatatest.CellIDAccessCore,
						Subscribers: []string{},
					},
				}
			},
			wantCount: 0,
		},
		{
			name: "http contract with no clients field → 0 findings (not event kind)",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.some.endpoint.v1"] = &metadata.ContractMeta{
					ID:               "http.some.endpoint.v1",
					Kind:             "http",
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints: metadata.EndpointsMeta{
						Server: metadatatest.CellIDAccessCore,
					},
				}
			},
			wantCount: 0,
		},
		{
			name: "draft lifecycle with empty subscribers → 0 findings (draft exempt)",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["event.draft.nosubscribers.v1"] = &metadata.ContractMeta{
					ID:               "event.draft.nosubscribers.v1",
					Kind:             "event",
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L2",
					Lifecycle:        "draft",
					Endpoints: metadata.EndpointsMeta{
						Publisher:   metadatatest.CellIDAccessCore,
						Subscribers: []string{},
					},
				}
			},
			wantCount: 0,
		},
		{
			name: "draft lifecycle with subscribers → 0 findings (draft exempt)",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["event.draft.withsubs.v1"] = &metadata.ContractMeta{
					ID:               "event.draft.withsubs.v1",
					Kind:             "event",
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L2",
					Lifecycle:        "draft",
					Endpoints: metadata.EndpointsMeta{
						Publisher:   metadatatest.CellIDAccessCore,
						Subscribers: []string{metadatatest.CellIDAuditCore},
					},
				}
			},
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "", clock.Real())
			got := findByCode(val.validateADV05(), "ADV-05")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, tt.wantSev, r.Severity)
				assert.Equal(t, IssueForbidden, r.IssueType)
				assert.Contains(t, r.Message, "is active but has no subscribers")
			}
		})
	}
}

// TestADV05_ExitCode_Regression proves that ADV-05 (dead event) does NOT
// produce a SeverityError and therefore must not cause `gocell validate` to
// exit non-zero.  ADV-05 was reclassified from SeverityError to
// SeverityWarning in M3 (ADR §M3-RULE-ENGINE); this test locks the regression
// to prevent accidental re-elevation.
//
// Two paths are exercised:
//  1. Direct detect (validateADV05): verifies the finding is SeverityWarning
//     and that HasErrors is false on those findings alone.
//  2. Engine loop path (run): verifies the engine accumulates the ADV-05
//     finding as SeverityWarning and that HasErrors stays false, so the
//     reclassified rule never drives a CI exit-code 1.
func TestADV05_ExitCode_Regression(t *testing.T) {
	t.Parallel()
	pm := &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Contracts: map[string]*metadata.ContractMeta{
			"event.dead.regression.v1": {
				ID:   "event.dead.regression.v1",
				Kind: "event",
				// OwnerCell omitted (zero value "") — intentionally invalid for ADV-05 test.
				ConsistencyLevel: "L2",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Subscribers: nil,
				},
				File: "contracts/event/dead/regression/v1/contract.yaml",
			},
		},
	}

	val := NewValidator(pm, "", clock.Real())

	// Path 1: direct detect — ADV-05 fires as SeverityWarning.
	direct := findByCode(val.validateADV05(), "ADV-05")
	require.NotEmpty(t, direct, "expected at least one ADV-05 finding from validateADV05")
	for _, r := range direct {
		assert.Equal(t, SeverityWarning, r.Severity,
			"ADV-05 must be SeverityWarning (reclassified from error in M3)")
	}
	assert.False(t, HasErrors(direct),
		"ADV-05 findings must not contain SeverityError — HasErrors must be false")

	// Path 2: engine loop — run() accumulates the warning findings correctly.
	adv05Rule := Rule{
		Code:   codeADV05,
		Phase:  PhaseBase,
		Detect: (*Validator).validateADV05,
	}
	stamped, err := val.run(context.Background(), []Rule{adv05Rule}, false)
	require.NoError(t, err)
	require.NotEmpty(t, stamped)
	for _, r := range stamped {
		assert.Equal(t, SeverityWarning, r.Severity,
			"engine must accumulate SeverityWarning ADV-05 findings (not error)")
	}
	assert.False(t, HasErrors(stamped),
		"engine-accumulated ADV-05 results must not cause HasErrors==true (no CI exit-code 1)")
}

// TestADV06_Removed verifies that ADV-06 no longer exists in the rule pipeline.
// Subscribers are derived from slice contractUsages + actorSubscribers, so drift
// between contract.yaml and slice.yaml is impossible by construction.
func TestADV06_Removed(t *testing.T) {
	pm := validProject()
	val := NewValidator(pm, "", clock.Real())
	// Only iterate non-Strict/non-Health rules: PhaseStrict rules (e.g. VERIFY-06)
	// require v.runCtx to be set via run(); PhaseHealth rules require a full project.
	for _, rule := range rulesForPhases(PhaseBase, PhaseDep) {
		for _, r := range rule.Detect(val) {
			if r.Code == "ADV-06" {
				t.Errorf("ADV-06 must not be emitted by allRules: found result %v", r)
			}
		}
	}
}

// --- helper function tests for new utilities ---

func TestContractDirFromID(t *testing.T) {
	assert.Equal(t, "contracts/http/auth/login/v1", contractDirFromID("http.auth.login.v1"))
	assert.Equal(t, "contracts/event/session/created/v1", contractDirFromID("event.session.created.v1"))
}

func TestRepositoryRoot(t *testing.T) {
	t.Run("root ending in src", func(t *testing.T) {
		tmpDir := t.TempDir()
		srcDir := filepath.Join(tmpDir, "src")
		require.NoError(t, os.MkdirAll(srcDir, 0o755))
		got := repositoryRoot(srcDir)
		assert.Equal(t, srcDir, got)
	})

	t.Run("root not ending in src", func(t *testing.T) {
		tmpDir := t.TempDir()
		got := repositoryRoot(tmpDir)
		assert.Equal(t, tmpDir, got)
	})
}

func TestActorExists(t *testing.T) {
	pm := validProject()
	val := NewValidator(pm, "", clock.Real())

	assert.True(t, val.actorExists("accesscore"), "cell should be a known actor")
	assert.True(t, val.actorExists(metadatatest.CellIDEdgeBFF), "external actor should be known")
	assert.False(t, val.actorExists("nonexistent"), "unknown ID should not exist")
}

// --- S1: IsWithinRoot path traversal guard ---

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
	// Also test relative paths (P1 fix: IsWithinRoot must handle relative root).
	cwd, err := os.Getwd()
	require.NoError(t, err)
	tests = append(tests, struct {
		name   string
		root   string
		target string
		want   bool
	}{
		"relative root dot",
		".",
		filepath.Join(cwd, "assemblies", "corebundle", "generated", "boundary.yaml"),
		true,
	})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsWithinRoot(tt.root, tt.target)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestIsWithinRoot_Symlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink requires SeCreateSymbolicLinkPrivilege on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()

	// Create a file outside root.
	outsideFile := filepath.Join(outside, "secret.yaml")
	require.NoError(t, os.WriteFile(outsideFile, []byte("x"), 0o644))

	// Create a symlink inside root pointing outside.
	symlink := filepath.Join(root, "escape")
	require.NoError(t, os.Symlink(outside, symlink))

	target := filepath.Join(symlink, "secret.yaml")
	assert.False(t, IsWithinRoot(root, target), "symlink target outside root should be rejected")
}

// --- S1: REF-11 path traversal ---

func TestREF11_PathTraversal(t *testing.T) {
	tmpDir := t.TempDir()
	srcDir := filepath.Join(tmpDir, "src")
	require.NoError(t, os.MkdirAll(srcDir, 0o755))

	pm := validProject()
	pm.Assemblies["corebundle"].Build.Entrypoint = "../../../etc/passwd"
	val := NewValidator(pm, srcDir, clock.Real())
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
	val := NewValidator(pm, tmpDir, clock.Real())
	got := findByCode(val.validateREF12(), "REF-12")
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Message, "path escapes project root")
	assert.Equal(t, IssueInvalid, got[0].IssueType)
}

// --- S2: NewValidator with nil project ---

func TestNewValidator_NilProject(t *testing.T) {
	val := NewValidator(nil, ".", clock.Real())
	require.NotNil(t, val)

	// Should not panic and should return empty results.
	results, err := val.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	assert.Empty(t, results)
}

// --- FMT-10: banned field names and slash-separated contract IDs ---

func TestFMT10(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "no banned names",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "cell with banned ID cellId",
			setup: func(pm *metadata.ProjectMeta) {
				c := &metadata.CellMeta{
					Type:             "core",
					ConsistencyLevel: "L1",
					Owner:            metadata.OwnerMeta{Team: "t", Role: "r"},
					Schema:           metadata.SchemaMeta{Primary: "s"},
					Verify:           metadata.CellVerifyMeta{Smoke: []string{"s"}},
				}
				c.ID = "cellId" // non-compliant ID used to trigger FMT-10 detection
				pm.Cells["cellId"] = c
			},
			wantCount: 1,
		},
		{
			name: "contract with slash separator",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http/auth/login/v1"] = &metadata.ContractMeta{
					ID:               "http/auth/login/v1",
					Kind:             "http",
					OwnerCell:        metadatatest.CellIDAccessCore,
					ConsistencyLevel: "L1",
					Lifecycle:        "active",
					Endpoints:        metadata.EndpointsMeta{Server: metadatatest.CellIDAccessCore},
				}
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "", clock.Real())
			got := findByCode(val.validateFMT10(), "FMT-10")
			assert.Len(t, got, tt.wantCount)
		})
	}
}

// --- FMT-11: cell.yaml required owner and verify.smoke fields ---

func TestFMT11(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
		wantField string // if non-empty, first result must match this field
	}{
		{
			name:      "all cells have owner.team, owner.role, verify.smoke",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "cell missing owner.team",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].Owner.Team = ""
			},
			wantCount: 1,
			wantField: "owner.team",
		},
		{
			name: "cell missing owner.role",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].Owner.Role = ""
			},
			wantCount: 1,
			wantField: "owner.role",
		},
		{
			name: "cell missing verify.smoke",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].Verify.Smoke = nil
			},
			wantCount: 1,
			wantField: "verify.smoke",
		},
		{
			name: "cell with empty verify.smoke slice",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].Verify.Smoke = []string{}
			},
			wantCount: 1,
			wantField: "verify.smoke",
		},
		{
			name: "cell missing all three fields",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].Owner.Team = ""
				pm.Cells["accesscore"].Owner.Role = ""
				pm.Cells["accesscore"].Verify.Smoke = nil
			},
			wantCount: 3,
		},
		{
			name: "multiple cells with missing fields",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].Owner.Team = ""
				pm.Cells["auditcore"].Owner.Role = ""
			},
			wantCount: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "", clock.Real())
			got := findByCode(val.validateFMT11(), "FMT-11")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				assert.Equal(t, IssueRequired, r.IssueType)
			}
			if tt.wantField != "" && len(got) > 0 {
				assert.Equal(t, tt.wantField, got[0].Field)
			}
		})
	}
}

// --- FMT-12: slice.yaml required verify.unit field ---

func TestFMT12(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name:      "all slices have verify.unit",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "slice missing verify.unit (nil)",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Unit = nil
			},
			wantCount: 1,
		},
		{
			name: "slice with empty verify.unit slice",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Unit = []string{}
			},
			wantCount: 1,
		},
		{
			name: "multiple slices missing verify.unit",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["accesscore/session-login"].Verify.Unit = nil
				pm.Slices["auditcore/audit-write"].Verify.Unit = nil
			},
			wantCount: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "", clock.Real())
			got := findByCode(val.validateFMT12(), "FMT-12")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				assert.Equal(t, IssueRequired, r.IssueType)
				assert.Equal(t, "verify.unit", r.Field)
			}
		})
	}
}

// --- FMT-13: migrated HTTP transport metadata must be complete and consistent ---

func TestFMT13(t *testing.T) {
	tests := []struct {
		name         string
		setup        func(*metadata.ProjectMeta)
		wantErrors   int
		wantWarnings int
		wantField    string
	}{
		{
			// FMT-13 now requires endpoints.http on all HTTP contracts (FMT-13 必填化).
			// Remove the endpoints.http from the contract to trigger the error.
			name: "http contract without transport metadata is now required (FMT-13必填化)",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = nil
			},
			wantErrors:   1,
			wantWarnings: 0,
			wantField:    "endpoints.http",
		},
		{
			name: "complete migrated http contract is allowed",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "POST",
					Path:          "/api/v1/auth/login",
					SuccessStatus: 200,
					NoContent:     false,
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			wantErrors:   0,
			wantWarnings: 0,
		},
		{
			name: "migrated http contract requires all transport fields",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:    "POST",
					NoContent: false,
				}
			},
			wantErrors:   2,
			wantWarnings: 1, // noContent=false without response
		},
		{
			name: "transport metadata is only valid on http contracts",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["event.session.created.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "POST",
					Path:          "/api/v1/should-not-exist",
					SuccessStatus: 202,
					NoContent:     false,
				}
			},
			wantErrors: 1,
			wantField:  "endpoints.http",
		},
		{
			name: "noContent forbids a response schema",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "DELETE",
					Path:          "/api/v1/auth/users/{userId}",
					SuccessStatus: 204,
					NoContent:     true,
					PathParams: map[string]metadata.ParamSchema{
						"userId": {Type: "string"},
					},
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			wantErrors: 1,
			wantField:  "schemaRefs.response",
		},
		{
			name: "noContent requires 204",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "DELETE",
					Path:          "/api/v1/auth/users/{userId}",
					SuccessStatus: 200,
					NoContent:     true,
					PathParams: map[string]metadata.ParamSchema{
						"userId": {Type: "string"},
					},
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = ""
			},
			wantErrors: 1,
			wantField:  "endpoints.http.noContent",
		},
		{
			name: "204 requires noContent true",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "DELETE",
					Path:          "/api/v1/auth/users/{userId}",
					SuccessStatus: 204,
					NoContent:     false,
					PathParams: map[string]metadata.ParamSchema{
						"userId": {Type: "string"},
					},
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = ""
			},
			wantErrors:   1,
			wantWarnings: 1, // noContent=false without response
			wantField:    "endpoints.http.noContent",
		},
		{
			name: "noContent false without response schema warns",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "GET",
					Path:          "/api/v1/auth/users",
					SuccessStatus: 200,
					NoContent:     false,
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = ""
			},
			wantErrors:   0,
			wantWarnings: 1,
			wantField:    "schemaRefs.response",
		},
		{
			name: "invalid HTTP method is rejected",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "TRACE",
					Path:          "/api/v1/auth/login",
					SuccessStatus: 200,
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			wantErrors: 1,
			wantField:  "endpoints.http.method",
		},
		{
			name: "path without leading slash is rejected",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "GET",
					Path:          "api/v1/auth/users",
					SuccessStatus: 200,
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			wantErrors: 1,
			wantField:  "endpoints.http.path",
		},
		{
			name: "non-2xx successStatus is rejected",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "POST",
					Path:          "/api/v1/auth/login",
					SuccessStatus: 301,
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			wantErrors: 1,
			wantField:  "endpoints.http.successStatus",
		},
		{
			name: "path placeholder without pathParams declaration is rejected",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "GET",
					Path:          "/api/v1/auth/users/{id}",
					SuccessStatus: 200,
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			wantErrors: 1,
			wantField:  "endpoints.http.pathParams",
		},
		{
			name: "pathParams key without matching placeholder is rejected",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "GET",
					Path:          "/api/v1/auth/users",
					SuccessStatus: 200,
					PathParams: map[string]metadata.ParamSchema{
						"ghost": {Type: "string"},
					},
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			wantErrors: 1,
			wantField:  "endpoints.http.pathParams.ghost",
		},
		{
			name: "pathParams with unknown type is rejected",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "GET",
					Path:          "/api/v1/auth/users/{id}",
					SuccessStatus: 200,
					PathParams: map[string]metadata.ParamSchema{
						"id": {Type: "bigint"},
					},
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			wantErrors: 1,
			wantField:  "endpoints.http.pathParams.id.type",
		},
		{
			name: "pathParams marked optional is rejected",
			setup: func(pm *metadata.ProjectMeta) {
				optional := false
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "GET",
					Path:          "/api/v1/auth/users/{id}",
					SuccessStatus: 200,
					PathParams: map[string]metadata.ParamSchema{
						"id": {Type: "string", Required: &optional},
					},
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			wantErrors: 1,
			wantField:  "endpoints.http.pathParams.id.required",
		},
		{
			name: "pathParams complete multi-placeholder accepted",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "GET",
					Path:          "/api/v1/access/roles/{userID}/{roleName}",
					SuccessStatus: 200,
					PathParams: map[string]metadata.ParamSchema{
						"userID":   {Type: "string", Format: "uuid"},
						"roleName": {Type: "string"},
					},
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			wantErrors:   0,
			wantWarnings: 0,
		},
		{
			name: "queryParams with unknown type is rejected",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "GET",
					Path:          "/api/v1/auth/users",
					SuccessStatus: 200,
					QueryParams: map[string]metadata.ParamSchema{
						"cursor": {Type: "blob"},
					},
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			wantErrors: 1,
			wantField:  "endpoints.http.queryParams.cursor.type",
		},
		{
			name: "queryParams optional typed entry accepted",
			setup: func(pm *metadata.ProjectMeta) {
				falsy := false
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "GET",
					Path:          "/api/v1/auth/users",
					SuccessStatus: 200,
					QueryParams: map[string]metadata.ParamSchema{
						"cursor": {Type: "string", Required: &falsy},
						"limit":  {Type: "integer"},
					},
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			wantErrors:   0,
			wantWarnings: 0,
		},
		{
			name: "duplicate path placeholder accepted with single declaration",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "GET",
					Path:          "/api/v1/things/{id}/children/{id}",
					SuccessStatus: 200,
					PathParams: map[string]metadata.ParamSchema{
						"id": {Type: "string"},
					},
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			wantErrors:   0,
			wantWarnings: 0,
		},
		{
			name: "combined pathParams and queryParams on one endpoint accepted",
			setup: func(pm *metadata.ProjectMeta) {
				falsy := false
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "GET",
					Path:          "/api/v1/users/{id}/roles",
					SuccessStatus: 200,
					PathParams: map[string]metadata.ParamSchema{
						"id": {Type: "string", Format: "uuid"},
					},
					QueryParams: map[string]metadata.ParamSchema{
						"cursor": {Type: "string", Required: &falsy},
					},
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			wantErrors:   0,
			wantWarnings: 0,
		},
		{
			name: "empty path with non-empty pathParams reports only the path-required error",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "GET",
					Path:          "",
					SuccessStatus: 200,
					PathParams: map[string]metadata.ParamSchema{
						"id": {Type: "string"},
					},
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			wantErrors: 1,
			wantField:  "endpoints.http.path",
		},
		{
			// queryParams has no path dependency, so empty path + invalid
			// query type should surface both diagnostics independently
			// (path-required + query type invalid).
			name: "empty path with invalid queryParams reports both errors",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
					Method:        "GET",
					Path:          "",
					SuccessStatus: 200,
					QueryParams: map[string]metadata.ParamSchema{
						"cursor": {Type: "blob"},
					},
				}
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			wantErrors: 2,
			wantField:  "endpoints.http.path",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "", clock.Real())
			got := findByCode(val.validateFMT13(), "FMT-13")
			errors := FilterErrors(got)
			warnings := FilterWarnings(got)
			assert.Len(t, errors, tt.wantErrors)
			assert.Len(t, warnings, tt.wantWarnings)
			if tt.wantField != "" && len(got) > 0 {
				assert.Equal(t, tt.wantField, got[0].Field)
			}
		})
	}
}

// TestFMT13_HasHowToFixHint locks in the PR239-DX1 message-quality
// improvement: the "no pathParams declaration" error must include a YAML
// fix snippet so the diagnostic explains *how* to fix it, not just *what*
// is missing. The hint is a concrete YAML fragment a user can copy-paste
// under their endpoints.http: block.
func TestFMT13_HasHowToFixHint(t *testing.T) {
	pm := validProject()
	pm.Contracts["http.auth.login.v1"].Endpoints.HTTP = &metadata.HTTPTransportMeta{
		Method:        "GET",
		Path:          "/api/v1/auth/users/{userId}",
		SuccessStatus: 200,
	}
	pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateFMT13(), "FMT-13")
	require.Len(t, got, 1, "expected one FMT-13 finding")

	fix := got[0].Fix
	assert.Contains(t, fix, "add to contract.yaml:",
		"FMT-13 fix must announce the fix hint")
	assert.Contains(t, fix, "pathParams:",
		"FMT-13 fix must include the pathParams: key")
	assert.Contains(t, fix, "<name>:",
		"FMT-13 fix YAML uses a literal <name> placeholder marker (the concrete "+
			"placeholder name is carried by the Message, keeping the *Fix const argument-free)")
	assert.Contains(t, fix, "type: string",
		"FMT-13 fix must include a type: string fallback")
	// The concrete placeholder name lives in the Message, not the Fix.
	assert.Contains(t, got[0].Message, "userId",
		"FMT-13 Message must name the offending placeholder (userId)")
}

// --- TOPO-07: actor maxConsistencyLevel constraint for consumers ---

func TestTOPO07(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(*metadata.ProjectMeta)
		wantCount     int
		wantIssueType IssueType // zero value = no assertion on IssueType
		wantFile      string    // empty = no file assertion
	}{
		{
			name:      "no actor consumers",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "consumer actor within max level",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Actors = []metadata.ActorMeta{
					{ID: metadatatest.CellIDEdgeBFF, MaxConsistencyLevel: "L2"},
				}
				// http.auth.login.v1 is L1, edge-bff max is L2: OK
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{metadatatest.CellIDEdgeBFF}
			},
			wantCount: 0,
		},
		{
			name: "consumer actor exceeds max level — IssueMismatch on contract file",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Actors = []metadata.ActorMeta{
					{ID: metadatatest.CellIDEdgeBFF, MaxConsistencyLevel: "L0"},
				}
				// http.auth.login.v1 is L1, edge-bff max is L0: violation
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{metadatatest.CellIDEdgeBFF}
			},
			wantCount:     1,
			wantIssueType: IssueMismatch,
			wantFile:      contractFileFromID("http.auth.login.v1"),
		},
		{
			name: "consumer actor with no max level (unconstrained)",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Actors = []metadata.ActorMeta{
					{ID: metadatatest.CellIDEdgeBFF, MaxConsistencyLevel: ""},
				}
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{metadatatest.CellIDEdgeBFF}
			},
			wantCount: 0,
		},
		{
			name: "consumer actor with malformed max level — IssueInvalid on actors.yaml",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Actors = []metadata.ActorMeta{
					{ID: metadatatest.CellIDEdgeBFF, MaxConsistencyLevel: "INVALID"},
				}
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{metadatatest.CellIDEdgeBFF}
			},
			wantCount:     1,
			wantIssueType: IssueInvalid,
			wantFile:      "actors.yaml",
		},
		{
			name: "cell consumer is not checked by TOPO-07",
			setup: func(pm *metadata.ProjectMeta) {
				// auditcore is a cell consumer (L2), contract is L1: TOPO-07 should skip cells
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{"auditcore"}
			},
			wantCount: 0,
		},
		{
			name: "wildcard consumer is skipped",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Actors = []metadata.ActorMeta{
					{ID: metadatatest.CellIDEdgeBFF, MaxConsistencyLevel: "L0"},
				}
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{"*"}
			},
			wantCount: 0,
		},
		{
			name: "event contract consumer actor exceeds max level",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Actors = []metadata.ActorMeta{
					{ID: metadatatest.CellIDEdgeBFF, MaxConsistencyLevel: "L1"},
				}
				// event.session.created.v1 is L2, edge-bff max is L1: violation
				pm.Contracts["event.session.created.v1"].Endpoints.Subscribers = []string{metadatatest.CellIDEdgeBFF}
			},
			wantCount: 1,
		},
		{
			name: "multiple consumer actors, one exceeds",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Actors = []metadata.ActorMeta{
					{ID: metadatatest.CellIDEdgeBFF, MaxConsistencyLevel: "L0"},
					{ID: "ext-monitor", MaxConsistencyLevel: "L4"},
				}
				pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{metadatatest.CellIDEdgeBFF, "ext-monitor"}
			},
			wantCount: 1, // only edge-bff exceeds
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "", clock.Real())
			got := findByCode(val.validateTOPO07(), "TOPO-07")
			assert.Len(t, got, tt.wantCount)
			if tt.wantCount == 0 {
				return
			}
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				if tt.wantIssueType != "" {
					assert.Equal(t, tt.wantIssueType, r.IssueType)
				}
				if tt.wantFile != "" {
					assert.Equal(t, tt.wantFile, r.File)
				}
			}
		})
	}
}

func TestTOPO07_FieldNameMatchesKind(t *testing.T) {
	tests := []struct {
		name      string
		kind      string
		wantField string
		setup     func(*metadata.ContractMeta)
	}{
		{
			name: "http uses clients",
			kind: "http", wantField: "endpoints.clients[0]",
			setup: func(c *metadata.ContractMeta) { c.Endpoints.Clients = []string{metadatatest.CellIDEdgeBFF} },
		},
		{
			name: "event uses subscribers",
			kind: "event", wantField: "endpoints.subscribers[0]",
			setup: func(c *metadata.ContractMeta) {
				r := true
				c.Endpoints.Subscribers = []string{metadatatest.CellIDEdgeBFF}
				c.Replayable = &r
				c.IdempotencyKey = "eventId"
				c.DeliverySemantics = "at-least-once"
			},
		},
		{
			name: "command uses invokers",
			kind: "command", wantField: "endpoints.invokers[0]",
			setup: func(c *metadata.ContractMeta) { c.Endpoints.Invokers = []string{metadatatest.CellIDEdgeBFF} },
		},
		{
			name: "projection uses readers",
			kind: "projection", wantField: "endpoints.readers[0]",
			setup: func(c *metadata.ContractMeta) {
				r := true
				c.Endpoints.Readers = []string{metadatatest.CellIDEdgeBFF}
				c.Replayable = &r
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			pm.Actors = []metadata.ActorMeta{
				{ID: metadatatest.CellIDEdgeBFF, MaxConsistencyLevel: "L0"},
			}
			c := &metadata.ContractMeta{
				ID: tt.kind + ".test.v1", Kind: tt.kind,
				OwnerCell: metadatatest.CellIDAccessCore, ConsistencyLevel: "L2",
				Lifecycle: "active",
				Endpoints: metadata.EndpointsMeta{
					Server:    metadatatest.CellIDAccessCore,
					Publisher: metadatatest.CellIDAccessCore,
					Handler:   metadatatest.CellIDAccessCore,
					Provider:  metadatatest.CellIDAccessCore,
				},
			}
			tt.setup(c)
			pm.Contracts[c.ID] = c

			val := NewValidator(pm, "", clock.Real())
			got := findByCode(val.validateTOPO07(), "TOPO-07")
			require.NotEmpty(t, got)
			assert.Equal(t, tt.wantField, got[0].Field)
		})
	}
}

func TestTOPO07_MalformedMessage(t *testing.T) {
	pm := validProject()
	pm.Actors = []metadata.ActorMeta{
		{ID: metadatatest.CellIDEdgeBFF, MaxConsistencyLevel: "INVALID"},
	}
	pm.Contracts["http.auth.login.v1"].Endpoints.Clients = []string{metadatatest.CellIDEdgeBFF}

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateTOPO07(), "TOPO-07")
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Message, "INVALID")
	assert.Contains(t, got[0].Message, "must be L0-L4")
}

// --- TOPO-08: deprecated contract reference blocking ---

func TestTOPO08(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
		wantFile  string // empty = no file assertion (zero-result cases)
	}{
		{
			name:      "no deprecated contracts",
			setup:     func(_ *metadata.ProjectMeta) {},
			wantCount: 0,
		},
		{
			name: "deprecated contract referenced by slice — IssueForbidden on slice.yaml",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Lifecycle = "deprecated"
			},
			wantCount: 1, // session-login uses http.auth.login.v1
			wantFile:  "cells/accesscore/slices/session-login/slice.yaml",
		},
		{
			name: "deprecated contract referenced by multiple slices",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["event.session.created.v1"].Lifecycle = "deprecated"
			},
			wantCount: 2, // session-login publishes + audit-write subscribes
		},
		{
			name: "draft contract not flagged",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Lifecycle = "draft"
			},
			wantCount: 0,
		},
		{
			name: "active contract not flagged",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].Lifecycle = "active"
			},
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "", clock.Real())
			got := findByCode(val.validateTOPO08(), "TOPO-08")
			assert.Len(t, got, tt.wantCount)
			if tt.wantCount == 0 {
				return
			}
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				assert.Equal(t, IssueForbidden, r.IssueType)
				assert.Contains(t, r.Message, "ownerCell")
				if tt.wantFile != "" {
					assert.Equal(t, tt.wantFile, r.File)
				}
			}
		})
	}
}

// --- TOPO-08 replaces ADV-02: deprecated contract produces only error, no warning ---

func TestTOPO08_ReplacesADV02(t *testing.T) {
	pm := validProject()
	pm.Contracts["http.auth.login.v1"].Lifecycle = "deprecated"

	val := NewValidator(pm, "", clock.Real())
	results, err := val.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)

	topo08 := findByCode(results, "TOPO-08")
	adv02 := findByCode(results, "ADV-02")

	assert.NotEmpty(t, topo08, "TOPO-08 error should fire for deprecated contract")
	assert.Empty(t, adv02, "ADV-02 should no longer fire — TOPO-08 replaces it")

	for _, r := range topo08 {
		assert.Equal(t, SeverityError, r.Severity)
	}
}

// --- REF-16: assembly boundary.yaml existence ---

func TestREF16(t *testing.T) {
	t.Run("skipped when root is empty", func(t *testing.T) {
		pm := validProject()
		val := NewValidator(pm, "", clock.Real())
		got := findByCode(val.validateREF16(), "REF-16")
		assert.Empty(t, got)
	})

	t.Run("boundary.yaml exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		srcDir := filepath.Join(tmpDir, "src")
		require.NoError(t, os.MkdirAll(srcDir, 0o755))
		// Create boundary.yaml under assemblies/ (where gocell generate writes it).
		boundaryDir := filepath.Join(srcDir, "assemblies", "corebundle", "generated")
		require.NoError(t, os.MkdirAll(boundaryDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(boundaryDir, "boundary.yaml"), []byte("assembly: corebundle"), 0o644))

		pm := validProject()
		val := NewValidator(pm, srcDir, clock.Real())
		got := findByCode(val.validateREF16(), "REF-16")
		assert.Empty(t, got)
	})

	t.Run("boundary.yaml missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		srcDir := filepath.Join(tmpDir, "src")
		require.NoError(t, os.MkdirAll(srcDir, 0o755))

		pm := validProject()
		val := NewValidator(pm, srcDir, clock.Real())
		got := findByCode(val.validateREF16(), "REF-16")
		assert.Len(t, got, 1)
		assert.Equal(t, SeverityWarning, got[0].Severity)
		assert.Equal(t, IssueRefNotFound, got[0].IssueType)
		assert.Contains(t, got[0].Message, "boundary.yaml")
		assert.Contains(t, got[0].Message, "assemblies/corebundle/generated/boundary.yaml")
	})

	t.Run("path traversal in assembly ID", func(t *testing.T) {
		tmpDir := t.TempDir()
		srcDir := filepath.Join(tmpDir, "src")
		require.NoError(t, os.MkdirAll(srcDir, 0o755))

		pm := validProject()
		pm.Assemblies["../../etc"] = &metadata.AssemblyMeta{
			ID:    "../../etc",
			Cells: []string{metadatatest.CellIDAccessCore},
			Build: metadata.BuildMeta{
				Entrypoint: "cmd/evil/main.go",
				Binary:     "evil",
			},
			// File is set so REF-16's defense-in-depth IsWithinRoot check
			// sees a traversal-bearing path. Post-M1 (#1082), Locator rejects
			// ".." in manifest module paths at parse time, so this attack
			// vector cannot reach REF-16 in production — the test directly
			// injects the meta to verify REF-16 still catches it.
			File: "assemblies/../../etc/assembly.yaml",
		}

		val := NewValidator(pm, srcDir, clock.Real())
		got := findByCode(val.validateREF16(), "REF-16")
		// Should find path traversal error for the malicious assembly
		var traversal []ValidationResult
		for _, r := range got {
			if r.IssueType == IssueInvalid {
				traversal = append(traversal, r)
			}
		}
		assert.Len(t, traversal, 1)
		assert.Equal(t, SeverityError, traversal[0].Severity)
		assert.Contains(t, traversal[0].Message, "escapes project root")
	})

	t.Run("multiple assemblies with missing boundary.yaml", func(t *testing.T) {
		tmpDir := t.TempDir()
		srcDir := filepath.Join(tmpDir, "src")
		require.NoError(t, os.MkdirAll(srcDir, 0o755))

		pm := validProject()
		pm.Assemblies["edge-bundle"] = &metadata.AssemblyMeta{
			ID:    "edge-bundle",
			Cells: []string{metadatatest.CellIDAccessCore},
			Build: metadata.BuildMeta{
				Entrypoint:     "cmd/edge-bundle/main.go",
				Binary:         "edge-bundle",
				DeployTemplate: "k8s",
			},
		}

		val := NewValidator(pm, srcDir, clock.Real())
		got := findByCode(val.validateREF16(), "REF-16")
		assert.Len(t, got, 2) // both assemblies missing boundary.yaml
	})
}

// --- FMT-14: slice allowedFiles required ---

func TestFMT14(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name: "slices with allowedFiles are valid",
			setup: func(pm *metadata.ProjectMeta) {
				for _, s := range pm.Slices {
					s.AllowedFiles = []string{"cells/x/slices/y/**"}
				}
			},
			wantCount: 0,
		},
		{
			name: "slices without allowedFiles trigger errors",
			setup: func(pm *metadata.ProjectMeta) {
				for _, s := range pm.Slices {
					s.AllowedFiles = nil
				}
			},
			wantCount: 2,
		},
		{
			name: "only one slice missing",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Slices["auditcore/audit-write"].AllowedFiles = nil
			},
			wantCount: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, "", clock.Real())
			got := findByCode(val.validateFMT14(), "FMT-14")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				assert.Equal(t, IssueRequired, r.IssueType)
				assert.Equal(t, "allowedFiles", r.Field)
			}
		})
	}
}

func TestFMT14_ExamplePathSuggestion(t *testing.T) {
	pm := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDOrderCell: {ID: metadatatest.CellIDOrderCell},
		},
		Slices: map[string]*metadata.SliceMeta{
			metadatatest.CellIDOrderCell + "/ordercreate": {
				ID:            "ordercreate",
				BelongsToCell: metadatatest.CellIDOrderCell,
				File:          "examples/todoorder/cells/ordercell/slices/ordercreate/slice.yaml",
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateFMT14(), "FMT-14")
	require.Len(t, got, 1)
	assert.Contains(t, got[0].Message, `examples/todoorder/cells/ordercell/slices/ordercreate/**`)
}

// --- FMT-15: list response schema must require hasMore and nextCursor ---

func TestFMT15(t *testing.T) {
	// valid: nextCursor declared in properties, hasMore and nextCursor in required
	validListSchema := `{"properties":{"data":{"type":"array","items":{"type":"object"}},` +
		`"nextCursor":{"type":"string"}},"required":["data","nextCursor","hasMore"]}`
	// missing hasMore from required (nextCursor still in properties and required)
	missingHasMore := `{"properties":{"data":{"type":"array","items":{"type":"object"}},` +
		`"nextCursor":{"type":"string"}},"required":["data","nextCursor"]}`
	// missing nextCursor from properties (nextCursor still in required)
	missingNextCursorProperty := `{"properties":{"data":{"type":"array","items":{"type":"object"}}}` +
		`,"required":["data","nextCursor","hasMore"]}`
	// missing nextCursor from required (nextCursor still in properties)
	missingNextCursorRequired := `{"properties":{"data":{"type":"array","items":{"type":"object"}},` +
		`"nextCursor":{"type":"string"}},"required":["data","hasMore"]}`
	singleObject := `{"properties":{"data":{"type":"object"}},"required":["data"]}`
	invalidJSON := `{not json`

	tests := []struct {
		name         string
		emptyRoot    bool // when true, Validator is created with root=""
		setup        func(*metadata.ProjectMeta)
		readFile     func(string) ([]byte, error)
		wantCount    int
		wantSeverity Severity // if non-empty, each result must have this severity; defaults to SeverityError
	}{
		{
			name: "list schema with hasMore and nextCursor required",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			readFile:  func(_ string) ([]byte, error) { return []byte(validListSchema), nil },
			wantCount: 0,
		},
		{
			name: "list schema missing hasMore",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			readFile:  func(_ string) ([]byte, error) { return []byte(missingHasMore), nil },
			wantCount: 1,
		},
		{
			name: "list schema missing nextCursor in properties",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			readFile:  func(_ string) ([]byte, error) { return []byte(missingNextCursorProperty), nil },
			wantCount: 1,
		},
		{
			name: "list schema missing nextCursor in required",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			readFile:  func(_ string) ([]byte, error) { return []byte(missingNextCursorRequired), nil },
			wantCount: 1,
		},
		{
			name: "list schema missing both hasMore and nextCursor",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			readFile: func(_ string) ([]byte, error) {
				return []byte(`{"properties":{"data":{"type":"array","items":{"type":"object"}}},"required":["data"]}`), nil
			},
			wantCount: 3,
		},
		{
			name: "non-list schema skipped",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			readFile:  func(_ string) ([]byte, error) { return []byte(singleObject), nil },
			wantCount: 0,
		},
		{
			name:      "no schemaRefs.response skipped",
			setup:     func(_ *metadata.ProjectMeta) {},
			readFile:  nil,
			wantCount: 0,
		},
		{
			name: "nextCursor declared as null is not a valid property",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			readFile: func(_ string) ([]byte, error) {
				return []byte(`{"properties":{"data":{"type":"array"},"nextCursor":null},"required":["data","nextCursor","hasMore"]}`), nil
			},
			wantCount: 1,
		},
		{
			name: "file read ErrNotExist skipped (REF-12 handles)",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			readFile:  func(_ string) ([]byte, error) { return nil, os.ErrNotExist },
			wantCount: 0,
		},
		{
			name: "file read non-ENOENT IO error reports FMT-15",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			readFile:  func(_ string) ([]byte, error) { return nil, os.ErrPermission },
			wantCount: 1,
		},
		{
			name: "invalid JSON reports FMT-15",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			readFile:  func(_ string) ([]byte, error) { return []byte(invalidJSON), nil },
			wantCount: 1,
		},
		{
			name: "list-like schema with oneOf combinator reports FMT-15 warning",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			readFile: func(_ string) ([]byte, error) {
				return []byte(`{"properties":{"data":{},"nextCursor":{"type":"string"},` +
					`"hasMore":{"type":"boolean"}},"oneOf":[{"properties":{"data":{"type":"object"}}},` +
					`{"properties":{"data":{"type":"array"}}}]}`), nil
			},
			wantCount:    1,
			wantSeverity: SeverityWarning,
		},
		{
			name: "list-like schema with anyOf combinator reports FMT-15 warning",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			readFile: func(_ string) ([]byte, error) {
				return []byte(`{"properties":{"data":{},"hasMore":{"type":"boolean"}},"anyOf":[{"properties":{"data":{"type":"array"}}}]}`), nil
			},
			wantCount:    1,
			wantSeverity: SeverityWarning,
		},
		{
			name: "non-list combinator schema silently skipped",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			readFile: func(_ string) ([]byte, error) {
				// combinator but no hasMore/nextCursor at top level → not list-related
				return []byte(`{"properties":{"data":{}},"oneOf":[` +
					`{"properties":{"data":{"type":"string"}}},` +
					`{"properties":{"data":{"type":"integer"}}}]}`), nil
			},
			wantCount: 0,
		},
		{
			name: "non-http contract skipped",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["event.session.created.v1"].SchemaRefs.Response = "response.schema.json"
			},
			readFile:  func(_ string) ([]byte, error) { return []byte(missingHasMore), nil },
			wantCount: 0,
		},
		{
			name:      "empty root skipped",
			emptyRoot: true, // root="" causes early return in validateFMT15
			setup: func(pm *metadata.ProjectMeta) {
				pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"
			},
			readFile:  func(_ string) ([]byte, error) { return []byte(missingHasMore), nil },
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			root := "/fake/root"
			if tt.emptyRoot {
				root = ""
			}
			val := NewValidator(pm, root, clock.Real())
			if tt.readFile != nil {
				val.readFile = tt.readFile
			}
			got := findByCode(val.validateFMT15(), "FMT-15")
			assert.Len(t, got, tt.wantCount)
			wantSev := tt.wantSeverity
			if wantSev == "" {
				wantSev = SeverityError
			}
			for _, r := range got {
				assert.Equal(t, wantSev, r.Severity)
			}
		})
	}

	t.Run("readFile receives correct schemaPath", func(t *testing.T) {
		pm := validProject()
		pm.Contracts["http.auth.login.v1"].SchemaRefs.Response = "response.schema.json"

		var capturedPath string
		val := NewValidator(pm, "/project", clock.Real())
		val.readFile = func(path string) ([]byte, error) {
			capturedPath = path
			return []byte(validListSchema), nil
		}
		val.validateFMT15()

		// The schema resolver canonicalizes the fake root through filepath.Abs,
		// which preserves the current drive on Windows.
		expected, err := filepath.Abs(filepath.Join(
			string([]byte{'/'}), "project", "contracts", "http", "auth", "login", "v1", "response.schema.json",
		))
		require.NoError(t, err)
		assert.Equal(t, expected, capturedPath)
	})
}

// --- OUTGUARD-01: L2+ must declare durabilityMode; L0/L1 optional ---

func TestOUTGUARD01(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*metadata.ProjectMeta)
		wantCount int
	}{
		{
			name: "L2 cell without durabilityMode — error",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].DurabilityMode = ""
				pm.Cells["auditcore"].DurabilityMode = ""
			},
			wantCount: 2, // both L2 cells missing durabilityMode
		},
		{
			name: "L2 cell with durabilityMode — no error",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].DurabilityMode = "durable"
				pm.Cells["auditcore"].DurabilityMode = "durable"
			},
			wantCount: 0,
		},
		{
			name: "L0 cell without durabilityMode — no error (L0/L1 optional, K8s defaulting)",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].DurabilityMode = "durable"
				pm.Cells["auditcore"].DurabilityMode = "durable"
				pm.Cells["sharedcrypto"].DurabilityMode = ""
			},
			wantCount: 0,
		},
		{
			name: "mixed — only L2+ cell missing durabilityMode errors",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].DurabilityMode = "durable"
				pm.Cells["auditcore"].DurabilityMode = ""    // L2 — error
				pm.Cells["sharedcrypto"].DurabilityMode = "" // L0 — allowed (defaults to demo)
			},
			wantCount: 1,
		},
		{
			name: "L1 cell without durabilityMode — no error (L0/L1 optional)",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].DurabilityMode = "durable"
				pm.Cells["auditcore"].DurabilityMode = "durable"
				pm.Cells[metadatatest.CellIDL1Cell] = &metadata.CellMeta{
					ID:               metadatatest.CellIDL1Cell,
					Type:             "core",
					ConsistencyLevel: "L1",
					// No DurabilityMode — L0/L1 allowed to omit (advisory only).
					Owner:  metadata.OwnerMeta{Team: "t", Role: "cell-owner"},
					Schema: metadata.SchemaMeta{Primary: "cell_l1"},
					Verify: metadata.CellVerifyMeta{Smoke: []string{"smoke.l1cell.startup"}},
				}
			},
			wantCount: 0,
		},
		{
			name: "L1 cell with durabilityMode — no error",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["accesscore"].DurabilityMode = "durable"
				pm.Cells["auditcore"].DurabilityMode = "durable"
				pm.Cells[metadatatest.CellIDL1Cell] = &metadata.CellMeta{
					ID:               metadatatest.CellIDL1Cell,
					Type:             "core",
					ConsistencyLevel: "L1",
					DurabilityMode:   "demo",
					Owner:            metadata.OwnerMeta{Team: "t", Role: "cell-owner"},
					Schema:           metadata.SchemaMeta{Primary: "cell_l1"},
					Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.l1cell.startup"}},
				}
			},
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)
			val := NewValidator(pm, ".", clock.Real())
			got := findByCode(val.validateOUTGUARD01(), "OUTGUARD-01")
			assert.Len(t, got, tt.wantCount)
			for _, r := range got {
				assert.Equal(t, SeverityError, r.Severity)
				assert.Equal(t, IssueRequired, r.IssueType)
			}
		})
	}
}

func TestOUTGUARD01_L3_L4_Error(t *testing.T) {
	pm := validProject()
	// Suppress existing L2 warnings.
	pm.Cells["accesscore"].DurabilityMode = "durable"
	pm.Cells["auditcore"].DurabilityMode = "durable"
	// Add L3 and L4 cells without durabilityMode.
	l3cell := &metadata.CellMeta{
		Type:             "core",
		ConsistencyLevel: "L3",
		Owner:            metadata.OwnerMeta{Team: "t", Role: "cell-owner"},
		Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.l3cell.startup"}},
	}
	l3cell.ID = "l3cell"
	pm.Cells["l3cell"] = l3cell
	l4cell := &metadata.CellMeta{
		Type:             "core",
		ConsistencyLevel: "L4",
		Owner:            metadata.OwnerMeta{Team: "t", Role: "cell-owner"},
		Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.l4cell.startup"}},
	}
	l4cell.ID = "l4cell"
	pm.Cells["l4cell"] = l4cell

	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateOUTGUARD01(), "OUTGUARD-01")
	assert.Len(t, got, 2, "both L3 and L4 cells should error")
}

func TestValidate_OUTGUARD01_Registration(t *testing.T) {
	// Entry-point test: call Validate() (not validateOUTGUARD01 directly)
	// and assert OUTGUARD-01 is registered and fires. Prevents silent
	// deregistration if someone removes the rule from Validate().
	pm := validProject()
	pm.Cells["accesscore"].DurabilityMode = "" // L2, missing → error

	val := NewValidator(pm, ".", clock.Real())
	all, err := val.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)
	got := findByCode(all, "OUTGUARD-01")
	assert.NotEmpty(t, got, "OUTGUARD-01 must be registered in Validate() entry point")
}

func TestOUTGUARD01_InvalidDurabilityMode(t *testing.T) {
	pm := validProject()
	pm.Cells["accesscore"].DurabilityMode = "banana" // invalid value
	pm.Cells["auditcore"].DurabilityMode = "durable"

	val := NewValidator(pm, ".", clock.Real())
	got := findByCode(val.validateOUTGUARD01(), "OUTGUARD-01")
	assert.Len(t, got, 1, "invalid durabilityMode should produce error")
	assert.Equal(t, SeverityError, got[0].Severity)
	assert.Equal(t, IssueInvalid, got[0].IssueType)
	assert.Contains(t, got[0].Message, "banana")
	assert.Contains(t, got[0].Fix, "use demo for examples/tests", "durabilityModeHintSuffix must appear in OUTGUARD-01 fix")
}

// TestOUTGUARD01_InvalidDurabilityMode_L0L1 covers the L0/L1 branch: the field
// is optional, but when explicitly set its value is still validated (a typo
// must not slip through just because the cell is below L2).
func TestOUTGUARD01_InvalidDurabilityMode_L0L1(t *testing.T) {
	tests := []struct {
		name  string
		setup func(pm *metadata.ProjectMeta)
	}{
		{
			name: "L0 cell with invalid durabilityMode",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells["sharedcrypto"].DurabilityMode = "banana" // L0, invalid
			},
		},
		{
			name: "L1 cell with invalid durabilityMode",
			setup: func(pm *metadata.ProjectMeta) {
				pm.Cells[metadatatest.CellIDL1Cell] = &metadata.CellMeta{
					ID:               metadatatest.CellIDL1Cell,
					Type:             "core",
					ConsistencyLevel: "L1",
					DurabilityMode:   "banana", // L1, invalid
					Owner:            metadata.OwnerMeta{Team: "t", Role: "cell-owner"},
					Schema:           metadata.SchemaMeta{Primary: "cell_l1"},
					Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.l1cell.startup"}},
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := validProject()
			tt.setup(pm)

			val := NewValidator(pm, ".", clock.Real())
			got := findByCode(val.validateOUTGUARD01(), "OUTGUARD-01")
			assert.Len(t, got, 1, "invalid durabilityMode on L0/L1 cell should still error")
			assert.Equal(t, SeverityError, got[0].Severity)
			assert.Equal(t, IssueInvalid, got[0].IssueType)
			assert.Contains(t, got[0].Message, "banana")
		})
	}
}

// --- Parser examples/ walk coverage (V-A11) ---
// Confirms that Parser.ParseFS includes cells under examples/*/cells/**/cell.yaml
// in ProjectMeta.Cells.

func TestProjectWalksExamples(t *testing.T) {
	fsys := fstest.MapFS{
		"examples/demo/cells/foocell/cell.yaml": {
			Data: []byte("id: foocell\ntype: support\nconsistencyLevel: L0\n" +
				"owner:\n  team: demo\n  role: cell-owner\n" +
				"verify:\n  smoke:\n    - smoke.foocell.startup\n"),
		},
	}

	parser := metadata.NewParser(".")
	pm, err := parser.ParseFS(fsys)
	require.NoError(t, err)

	cell, ok := pm.Cells["foocell"]
	require.True(t, ok, "Cells map should contain foocell from examples/demo/cells/foocell/cell.yaml")
	assert.True(t, strings.HasPrefix(cell.File, "examples/demo/"),
		"cell.File should start with examples/demo/, got %q", cell.File)
}
