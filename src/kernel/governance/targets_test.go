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
			"access-core": {
				ID:               "access-core",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
			},
			"audit-core": {
				ID:               "audit-core",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
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
			},
			"access-core/session-refresh": {
				ID:            "session-refresh",
				BelongsToCell: "access-core",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "call"},
				},
			},
			"audit-core/audit-write": {
				ID:            "audit-write",
				BelongsToCell: "audit-core",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "event.session.created.v1", Role: "subscribe"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.login.v1": {
				ID:        "http.auth.login.v1",
				Kind:      "http",
				OwnerCell: "access-core",
				Lifecycle: "active",
			},
			"event.session.created.v1": {
				ID:        "event.session.created.v1",
				Kind:      "event",
				OwnerCell: "access-core",
				Lifecycle: "active",
			},
		},
		Journeys: map[string]*metadata.JourneyMeta{
			"J-sso-login": {
				ID:        "J-sso-login",
				Goal:      "SSO login flow",
				Cells:     []string{"access-core", "audit-core"},
				Contracts: []string{"http.auth.login.v1", "event.session.created.v1"},
			},
			"J-audit-trail": {
				ID:    "J-audit-trail",
				Goal:  "Audit trail for login",
				Cells: []string{"audit-core"},
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"core-bundle": {
				ID:    "core-bundle",
				Cells: []string{"access-core", "audit-core"},
				Build: metadata.BuildMeta{
					Entrypoint: "src/cmd/core-bundle/main.go",
					Binary:     "core-bundle",
				},
			},
		},
	}
}

func TestSelectFromFiles_SliceDirectory(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles([]string{
		"cells/access-core/slices/session-login/handler.go",
	})

	assert.Equal(t, []string{"access-core/session-login"}, result.Slices)
	assert.Equal(t, []string{"access-core"}, result.Cells)
	assert.Equal(t, []string{"event.session.created.v1", "http.auth.login.v1"}, result.Contracts)
	assert.Equal(t, []string{"J-sso-login"}, result.Journeys)
}

func TestSelectFromFiles_CellDirectoryNonSlices(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	// A file directly under cells/access-core (not in slices/) affects all slices of that cell.
	result := ts.SelectFromFiles([]string{
		"cells/access-core/cell.yaml",
	})

	assert.Equal(t, []string{"access-core/session-login", "access-core/session-refresh"}, result.Slices)
	assert.Equal(t, []string{"access-core"}, result.Cells)
	assert.Equal(t, []string{"event.session.created.v1", "http.auth.login.v1"}, result.Contracts)
	assert.Equal(t, []string{"J-sso-login"}, result.Journeys)
}

func TestSelectFromFiles_ContractDirectory(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles([]string{
		"contracts/event/session/created/v1/contract.yaml",
	})

	// event.session.created.v1 is used by access-core/session-login (publish) and audit-core/audit-write (subscribe).
	assert.Equal(t, []string{"access-core/session-login", "audit-core/audit-write"}, result.Slices)
	assert.Equal(t, []string{"access-core", "audit-core"}, result.Cells)
	// Both journeys are affected since both cells are involved.
	assert.Equal(t, []string{"J-audit-trail", "J-sso-login"}, result.Journeys)
}

func TestSelectFromFiles_MultipleFilesMergedAndDeduped(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles([]string{
		"cells/access-core/slices/session-login/handler.go",
		"cells/access-core/slices/session-login/types.go", // duplicate slice
		"cells/audit-core/slices/audit-write/writer.go",
	})

	assert.Equal(t, []string{"access-core/session-login", "audit-core/audit-write"}, result.Slices)
	assert.Equal(t, []string{"access-core", "audit-core"}, result.Cells)
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
		"cells/access-core/slices/nonexistent-slice/handler.go",
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
	assert.Equal(t, []string{"access-core/session-login", "access-core/session-refresh"}, result.Slices)
	assert.Equal(t, []string{"access-core"}, result.Cells)
}

func TestSelectFromSlice_Basic(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromSlice("audit-core/audit-write")

	assert.Equal(t, []string{"audit-core/audit-write"}, result.Slices)
	assert.Equal(t, []string{"audit-core"}, result.Cells)
	assert.Equal(t, []string{"event.session.created.v1"}, result.Contracts)
	assert.Equal(t, []string{"J-audit-trail", "J-sso-login"}, result.Journeys)
}

func TestSelectFromSlice_NonexistentSlice(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromSlice("access-core/nonexistent")

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
		"cells/access-core/slices/session-login/handler.go",
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
	// A file under cells/access-core/internal/ (not slices/) should affect all slices.
	result := ts.SelectFromFiles([]string{
		"cells/access-core/internal/repo/db.go",
	})

	assert.Equal(t, []string{"access-core/session-login", "access-core/session-refresh"}, result.Slices)
	assert.Equal(t, []string{"access-core"}, result.Cells)
}

func TestSelectFromSlice_ExpandsJourneysCorrectly(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	// access-core is in J-sso-login but NOT in J-audit-trail.
	result := ts.SelectFromSlice("access-core/session-refresh")

	assert.Equal(t, []string{"access-core/session-refresh"}, result.Slices)
	assert.Equal(t, []string{"access-core"}, result.Cells)
	assert.Equal(t, []string{"http.auth.login.v1"}, result.Contracts)
	assert.Equal(t, []string{"J-sso-login"}, result.Journeys)
}

func TestSelectFromFiles_JourneyFile(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles([]string{
		"journeys/J-sso-login.yaml",
	})

	// J-sso-login references access-core and audit-core, so all their slices are affected.
	assert.Equal(t, []string{
		"access-core/session-login", "access-core/session-refresh", "audit-core/audit-write",
	}, result.Slices)
	assert.Equal(t, []string{"access-core", "audit-core"}, result.Cells)
	// Contracts come from slice contractUsages + journey.Contracts (merged).
	assert.Equal(t, []string{"event.session.created.v1", "http.auth.login.v1"}, result.Contracts)
	assert.Equal(t, []string{"J-audit-trail", "J-sso-login"}, result.Journeys)
}

func TestSelectFromFiles_AssemblyFile(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles([]string{
		"assemblies/core-bundle/assembly.yaml",
	})

	// core-bundle contains access-core and audit-core, so all their slices are affected.
	assert.Equal(t, []string{
		"access-core/session-login", "access-core/session-refresh", "audit-core/audit-write",
	}, result.Slices)
	assert.Equal(t, []string{"access-core", "audit-core"}, result.Cells)
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
			"access-core": {
				ID:               "access-core",
				Type:             "core",
				ConsistencyLevel: "L2",
				L0Dependencies: []metadata.L0DepMeta{
					{Cell: "shared-crypto", Reason: "hashing"},
					{Cell: "shared-validate", Reason: "input validation"},
				},
			},
			"audit-core": {
				ID:               "audit-core",
				Type:             "core",
				ConsistencyLevel: "L2",
				// no L0 dependencies
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"shared-crypto/hasher": {
				ID:            "hasher",
				BelongsToCell: "shared-crypto",
			},
			// shared-validate has NO slices (intentional).
			"access-core/session-login": {
				ID:            "session-login",
				BelongsToCell: "access-core",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
				},
			},
			"audit-core/audit-write": {
				ID:            "audit-write",
				BelongsToCell: "audit-core",
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
				Cells: []string{"shared-crypto", "access-core"},
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
			name:  "L0 cell change propagates to dependent cell",
			files: []string{"cells/shared-crypto/slices/hasher/hash.go"},
			// shared-crypto/hasher is directly affected;
			// access-core depends on shared-crypto via l0Dependencies,
			// so access-core/session-login is also selected.
			wantSlices:    []string{"access-core/session-login", "shared-crypto/hasher"},
			wantCells:     []string{"access-core", "shared-crypto"},
			wantContracts: []string{"http.auth.login.v1"},
		},
		{
			name:  "non-L0 cell change does not trigger L0 tracking",
			files: []string{"cells/access-core/slices/session-login/handler.go"},
			// access-core is L2, so no L0 propagation happens.
			wantSlices:    []string{"access-core/session-login"},
			wantCells:     []string{"access-core"},
			wantContracts: []string{"http.auth.login.v1"},
		},
		{
			name:  "journey referencing L0 cell does NOT trigger L0 propagation",
			files: []string{"journeys/J-l0-test.yaml"},
			// Journey references shared-crypto (L0) and access-core.
			// But journey changes should NOT trigger L0 dependency propagation —
			// only file-path changes to cells/** should.
			wantSlices:    []string{"access-core/session-login", "shared-crypto/hasher"},
			wantCells:     []string{"access-core", "shared-crypto"},
			wantContracts: []string{"http.auth.login.v1"},
		},
		{
			name:  "L0 cell without slices propagates to dependents",
			files: []string{"cells/shared-validate/cell.yaml"},
			// shared-validate is L0 with no slices. Changing its cell.yaml
			// should still propagate to access-core (which depends on it).
			wantSlices:    []string{"access-core/session-login"},
			wantCells:     []string{"access-core"},
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
