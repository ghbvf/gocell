package governance

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/stretchr/testify/assert"
)

// targetsProject returns a ProjectMeta for impact-analysis tests.
// 2 cells, 3 slices (some with contractUsages), 2 contracts, 2 journeys, 1 assembly.
func targetsProject() *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
			},
			"auditcore": {
				ID:               "auditcore",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/session-login": {
				ID:            "session-login",
				BelongsToCell: "accesscore",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
					{Contract: "event.session.created.v1", Role: "publish"},
				},
			},
			"accesscore/session-refresh": {
				ID:            "session-refresh",
				BelongsToCell: "accesscore",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "call"},
				},
			},
			"auditcore/audit-write": {
				ID:            "audit-write",
				BelongsToCell: "auditcore",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "event.session.created.v1", Role: "subscribe"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.login.v1": {
				ID:        "http.auth.login.v1",
				Kind:      "http",
				OwnerCell: "accesscore",
				Lifecycle: "active",
			},
			"event.session.created.v1": {
				ID:        "event.session.created.v1",
				Kind:      "event",
				OwnerCell: "accesscore",
				Lifecycle: "active",
			},
		},
		Journeys: map[string]*metadata.JourneyMeta{
			"J-sso-login": {
				ID:        "J-sso-login",
				Goal:      "SSO login flow",
				Cells:     []string{"accesscore", "auditcore"},
				Contracts: []string{"http.auth.login.v1", "event.session.created.v1"},
			},
			"J-audit-trail": {
				ID:    "J-audit-trail",
				Goal:  "Audit trail for login",
				Cells: []string{"auditcore"},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"corebundle": {
				ID:    "corebundle",
				Cells: []string{"accesscore", "auditcore"},
				Build: metadata.BuildMeta{
					Entrypoint: "cmd/corebundle/main.go",
					Binary:     "corebundle",
				},
			},
		},
	}
}

func TestSelectFromFiles_SliceDirectory(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles([]string{
		"cells/accesscore/slices/session-login/handler.go",
	})

	assert.Equal(t, []string{"accesscore/session-login"}, result.Slices)
	assert.Equal(t, []string{"accesscore"}, result.Cells)
	assert.Equal(t, []string{"event.session.created.v1", "http.auth.login.v1"}, result.Contracts)
	assert.Equal(t, []string{"J-sso-login"}, result.Journeys)
}

func TestSelectFromFiles_CellDirectoryNonSlices(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	// A file directly under cells/accesscore (not in slices/) affects all slices of that cell.
	result := ts.SelectFromFiles([]string{
		"cells/accesscore/cell.yaml",
	})

	assert.Equal(t, []string{"accesscore/session-login", "accesscore/session-refresh"}, result.Slices)
	assert.Equal(t, []string{"accesscore"}, result.Cells)
	assert.Equal(t, []string{"event.session.created.v1", "http.auth.login.v1"}, result.Contracts)
	assert.Equal(t, []string{"J-sso-login"}, result.Journeys)
}

func TestSelectFromFiles_ContractDirectory(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles([]string{
		"contracts/event/session/created/v1/contract.yaml",
	})

	// event.session.created.v1 is used by accesscore/session-login (publish) and auditcore/audit-write (subscribe).
	assert.Equal(t, []string{"accesscore/session-login", "auditcore/audit-write"}, result.Slices)
	assert.Equal(t, []string{"accesscore", "auditcore"}, result.Cells)
	// Both journeys are affected since both cells are involved.
	assert.Equal(t, []string{"J-audit-trail", "J-sso-login"}, result.Journeys)
}

func TestSelectFromFiles_MultipleFilesMergedAndDeduped(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles([]string{
		"cells/accesscore/slices/session-login/handler.go",
		"cells/accesscore/slices/session-login/types.go", // duplicate slice
		"cells/auditcore/slices/audit-write/writer.go",
	})

	assert.Equal(t, []string{"accesscore/session-login", "auditcore/audit-write"}, result.Slices)
	assert.Equal(t, []string{"accesscore", "auditcore"}, result.Cells)
	assert.Equal(t, []string{"J-audit-trail", "J-sso-login"}, result.Journeys)
	assert.Equal(t, []string{"event.session.created.v1", "http.auth.login.v1"}, result.Contracts)
}

func TestSelectFromFiles_UnrelatedPaths(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles([]string{
		"docs/architecture.md",
		"README.md",
		"pkg/errcode/errcode.go",
	})

	assert.Nil(t, result.Slices)
	assert.Nil(t, result.Cells)
	assert.Nil(t, result.Journeys)
	assert.Nil(t, result.Contracts)
}

func TestSelectFromFiles_UnknownCell(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles([]string{
		"cells/nonexistent-cell/slices/foo/bar.go",
	})

	assert.Nil(t, result.Slices)
	assert.Nil(t, result.Cells)
	assert.Nil(t, result.Journeys)
	assert.Nil(t, result.Contracts)
}

func TestSelectFromFiles_UnknownSlice(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles([]string{
		"cells/accesscore/slices/nonexistent-slice/handler.go",
	})

	assert.Nil(t, result.Slices)
	assert.Nil(t, result.Cells)
	assert.Nil(t, result.Journeys)
	assert.Nil(t, result.Contracts)
}

func TestSelectFromFiles_UnknownContract(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles([]string{
		"contracts/http/unknown/endpoint/v1/contract.yaml",
	})

	assert.Nil(t, result.Slices)
}

func TestSelectFromFiles_ContractSchemaFile(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	// Even non-contract.yaml files under the contract directory should match.
	result := ts.SelectFromFiles([]string{
		"contracts/http/auth/login/v1/request.schema.json",
	})

	// http.auth.login.v1 is used by session-login (serve) and session-refresh (call).
	assert.Equal(t, []string{"accesscore/session-login", "accesscore/session-refresh"}, result.Slices)
	assert.Equal(t, []string{"accesscore"}, result.Cells)
}

func TestSelectFromSlice_Basic(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromSlice("auditcore/audit-write")

	assert.Equal(t, []string{"auditcore/audit-write"}, result.Slices)
	assert.Equal(t, []string{"auditcore"}, result.Cells)
	assert.Equal(t, []string{"event.session.created.v1"}, result.Contracts)
	assert.Equal(t, []string{"J-audit-trail", "J-sso-login"}, result.Journeys)
}

func TestSelectFromSlice_NonexistentSlice(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromSlice("accesscore/nonexistent")

	assert.Nil(t, result.Slices)
	assert.Nil(t, result.Cells)
	assert.Nil(t, result.Journeys)
	assert.Nil(t, result.Contracts)
}

func TestSelectFromFiles_EmptyProject(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells:     map[string]*metadata.CellMeta{},
		Slices:    map[string]*metadata.SliceMeta{},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
	}
	ts := NewTargetSelector(project)

	result := ts.SelectFromFiles([]string{
		"cells/accesscore/slices/session-login/handler.go",
		"contracts/http/auth/login/v1/contract.yaml",
	})

	assert.Nil(t, result.Slices)
	assert.Nil(t, result.Cells)
	assert.Nil(t, result.Journeys)
	assert.Nil(t, result.Contracts)
}

func TestSelectFromFiles_EmptyFileList(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles(nil)

	assert.Nil(t, result.Slices)
	assert.Nil(t, result.Cells)
	assert.Nil(t, result.Journeys)
	assert.Nil(t, result.Contracts)
}

func TestSelectFromFiles_CellDirectoryDeepNonSlice(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	// A file under cells/accesscore/internal/ (not slices/) should affect all slices.
	result := ts.SelectFromFiles([]string{
		"cells/accesscore/internal/repo/db.go",
	})

	assert.Equal(t, []string{"accesscore/session-login", "accesscore/session-refresh"}, result.Slices)
	assert.Equal(t, []string{"accesscore"}, result.Cells)
}

func TestSelectFromSlice_ExpandsJourneysCorrectly(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	// accesscore is in J-sso-login but NOT in J-audit-trail.
	result := ts.SelectFromSlice("accesscore/session-refresh")

	assert.Equal(t, []string{"accesscore/session-refresh"}, result.Slices)
	assert.Equal(t, []string{"accesscore"}, result.Cells)
	assert.Equal(t, []string{"http.auth.login.v1"}, result.Contracts)
	assert.Equal(t, []string{"J-sso-login"}, result.Journeys)
}

func TestSelectFromFiles_JourneyFile(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles([]string{
		"journeys/J-sso-login.yaml",
	})

	// J-sso-login references accesscore and auditcore, so all their slices are affected.
	assert.Equal(t, []string{
		"accesscore/session-login", "accesscore/session-refresh", "auditcore/audit-write",
	}, result.Slices)
	assert.Equal(t, []string{"accesscore", "auditcore"}, result.Cells)
	// Contracts come from slice contractUsages + journey.Contracts (merged).
	assert.Equal(t, []string{"event.session.created.v1", "http.auth.login.v1"}, result.Contracts)
	assert.Equal(t, []string{"J-audit-trail", "J-sso-login"}, result.Journeys)
}

func TestSelectFromFiles_AssemblyFile(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles([]string{
		"assemblies/corebundle/assembly.yaml",
	})

	// corebundle contains accesscore and auditcore, so all their slices are affected.
	assert.Equal(t, []string{
		"accesscore/session-login", "accesscore/session-refresh", "auditcore/audit-write",
	}, result.Slices)
	assert.Equal(t, []string{"accesscore", "auditcore"}, result.Cells)
}

func TestSelectFromFiles_JourneyStatusBoard(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	// status-board.yaml is not a J-*.yaml journey file; it should return empty.
	result := ts.SelectFromFiles([]string{
		"journeys/status-board.yaml",
	})

	assert.Nil(t, result.Slices)
	assert.Nil(t, result.Cells)
	assert.Nil(t, result.Journeys)
	assert.Nil(t, result.Contracts)
}

func TestSelectFromFiles_NonexistentJourney(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles([]string{
		"journeys/J-nonexistent.yaml",
	})

	assert.Nil(t, result.Slices)
	assert.Nil(t, result.Cells)
	assert.Nil(t, result.Journeys)
	assert.Nil(t, result.Contracts)
}

// --- L0 dependency tracking (GOV-6) ---

// l0Project returns a ProjectMeta with L0 cells (with and without slices)
// and dependent cells, plus a journey referencing the L0 cell.
func l0Project() *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"shared-crypto": {
				ID:               "shared-crypto",
				Type:             "support",
				ConsistencyLevel: "L0",
			},
			"shared-validate": {
				ID:               "shared-validate",
				Type:             "support",
				ConsistencyLevel: "L0",
				// L0 cell with NO slices — tests propagation for slice-less cells.
			},
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L2",
				L0Dependencies: []metadata.L0DepMeta{
					{Cell: "shared-crypto", Reason: "hashing"},
					{Cell: "shared-validate", Reason: "input validation"},
				},
			},
			"auditcore": {
				ID:               "auditcore",
				Type:             "core",
				ConsistencyLevel: "L2",
				// no L0 dependencies
			},
			"billing-core": {
				ID:               "billing-core",
				Type:             "core",
				ConsistencyLevel: "L2",
				L0Dependencies: []metadata.L0DepMeta{
					{Cell: "shared-crypto", Reason: "signature"},
				},
				// NOT referenced by J-l0-test journey — used to test
				// that journey changes don't trigger L0 propagation.
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"shared-crypto/hasher": {
				ID:            "hasher",
				BelongsToCell: "shared-crypto",
			},
			// shared-validate has NO slices (intentional).
			"accesscore/session-login": {
				ID:            "session-login",
				BelongsToCell: "accesscore",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
				},
			},
			"auditcore/audit-write": {
				ID:            "audit-write",
				BelongsToCell: "auditcore",
			},
			"billing-core/payment": {
				ID:            "payment",
				BelongsToCell: "billing-core",
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.login.v1": {
				ID:   "http.auth.login.v1",
				Kind: "http",
			},
		},
		Journeys: map[string]*metadata.JourneyMeta{
			"J-l0-test": {
				ID:    "J-l0-test",
				Cells: []string{"shared-crypto", "accesscore"},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

func TestSelectFromFiles_L0DependencyTracking(t *testing.T) {
	tests := []struct {
		name          string
		files         []string
		wantSlices    []string
		wantCells     []string
		wantContracts []string
	}{
		{
			name:  "L0 cell change propagates to all dependent cells",
			files: []string{"cells/shared-crypto/slices/hasher/hash.go"},
			// shared-crypto/hasher is directly affected;
			// accesscore AND billing-core both depend on shared-crypto,
			// so their slices are also selected.
			wantSlices:    []string{"accesscore/session-login", "billing-core/payment", "shared-crypto/hasher"},
			wantCells:     []string{"accesscore", "billing-core", "shared-crypto"},
			wantContracts: []string{"http.auth.login.v1"},
		},
		{
			name:  "non-L0 cell change does not trigger L0 tracking",
			files: []string{"cells/accesscore/slices/session-login/handler.go"},
			// accesscore is L2, so no L0 propagation happens.
			wantSlices:    []string{"accesscore/session-login"},
			wantCells:     []string{"accesscore"},
			wantContracts: []string{"http.auth.login.v1"},
		},
		{
			name:  "journey referencing L0 cell does NOT trigger L0 propagation",
			files: []string{"journeys/J-l0-test.yaml"},
			// Journey references shared-crypto (L0) and accesscore.
			// billing-core depends on shared-crypto but is NOT in the journey.
			// If L0 propagation fired from journey expansion, billing-core/payment
			// would appear — its absence proves the guard works.
			wantSlices:    []string{"accesscore/session-login", "shared-crypto/hasher"},
			wantCells:     []string{"accesscore", "shared-crypto"},
			wantContracts: []string{"http.auth.login.v1"},
		},
		{
			name:  "L0 cell without slices propagates to dependents",
			files: []string{"cells/shared-validate/cell.yaml"},
			// shared-validate is L0 with no slices. Changing its cell.yaml
			// should still propagate to accesscore (which depends on it).
			wantSlices:    []string{"accesscore/session-login"},
			wantCells:     []string{"accesscore"},
			wantContracts: []string{"http.auth.login.v1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := NewTargetSelector(l0Project())
			result := ts.SelectFromFiles(tt.files)
			assert.Equal(t, tt.wantSlices, result.Slices)
			assert.Equal(t, tt.wantCells, result.Cells)
			if tt.wantContracts != nil {
				assert.Equal(t, tt.wantContracts, result.Contracts)
			}
		})
	}
}
