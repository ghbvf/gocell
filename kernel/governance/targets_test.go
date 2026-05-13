package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/metadata"
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
			"J-ssologin": {
				ID:        "J-ssologin",
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
	assert.Equal(t, []string{"J-ssologin"}, result.Journeys)
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
	assert.Equal(t, []string{"J-ssologin"}, result.Journeys)
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
	assert.Equal(t, []string{"J-audit-trail", "J-ssologin"}, result.Journeys)
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
	assert.Equal(t, []string{"J-audit-trail", "J-ssologin"}, result.Journeys)
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
	assert.Equal(t, []string{"J-audit-trail", "J-ssologin"}, result.Journeys)
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
	// accesscore is in J-ssologin but NOT in J-audit-trail.
	result := ts.SelectFromSlice("accesscore/session-refresh")

	assert.Equal(t, []string{"accesscore/session-refresh"}, result.Slices)
	assert.Equal(t, []string{"accesscore"}, result.Cells)
	assert.Equal(t, []string{"http.auth.login.v1"}, result.Contracts)
	assert.Equal(t, []string{"J-ssologin"}, result.Journeys)
}

func TestSelectFromFiles_JourneyFile(t *testing.T) {
	ts := NewTargetSelector(targetsProject())
	result := ts.SelectFromFiles([]string{
		"journeys/J-ssologin.yaml",
	})

	// J-ssologin references accesscore and auditcore, so all their slices are affected.
	assert.Equal(t, []string{
		"accesscore/session-login", "accesscore/session-refresh", "auditcore/audit-write",
	}, result.Slices)
	assert.Equal(t, []string{"accesscore", "auditcore"}, result.Cells)
	// Contracts come from slice contractUsages + journey.Contracts (merged).
	assert.Equal(t, []string{"event.session.created.v1", "http.auth.login.v1"}, result.Contracts)
	assert.Equal(t, []string{"J-audit-trail", "J-ssologin"}, result.Journeys)
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
			"sharedcrypto": {
				ID:               "sharedcrypto",
				Type:             "support",
				ConsistencyLevel: "L0",
			},
			"sharedvalidate": {
				ID:               "sharedvalidate",
				Type:             "support",
				ConsistencyLevel: "L0",
				// L0 cell with NO slices — tests propagation for slice-less cells.
			},
			"accesscore": {
				ID:               "accesscore",
				Type:             "core",
				ConsistencyLevel: "L2",
				L0Dependencies: []metadata.L0DepMeta{
					{Cell: "sharedcrypto", Reason: "hashing"},
					{Cell: "sharedvalidate", Reason: "input validation"},
				},
			},
			"auditcore": {
				ID:               "auditcore",
				Type:             "core",
				ConsistencyLevel: "L2",
				// no L0 dependencies
			},
			"billingcore": {
				ID:               "billingcore",
				Type:             "core",
				ConsistencyLevel: "L2",
				L0Dependencies: []metadata.L0DepMeta{
					{Cell: "sharedcrypto", Reason: "signature"},
				},
				// NOT referenced by J-l0-test journey — used to test
				// that journey changes don't trigger L0 propagation.
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"sharedcrypto/hasher": {
				ID:            "hasher",
				BelongsToCell: "sharedcrypto",
			},
			// sharedvalidate has NO slices (intentional).
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
			"billingcore/payment": {
				ID:            "payment",
				BelongsToCell: "billingcore",
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
				Cells: []string{"sharedcrypto", "accesscore"},
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
			files: []string{"cells/sharedcrypto/slices/hasher/hash.go"},
			// sharedcrypto/hasher is directly affected;
			// accesscore AND billingcore both depend on sharedcrypto,
			// so their slices are also selected.
			wantSlices:    []string{"accesscore/session-login", "billingcore/payment", "sharedcrypto/hasher"},
			wantCells:     []string{"accesscore", "billingcore", "sharedcrypto"},
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
			// Journey references sharedcrypto (L0) and accesscore.
			// billingcore depends on sharedcrypto but is NOT in the journey.
			// If L0 propagation fired from journey expansion, billingcore/payment
			// would appear — its absence proves the guard works.
			wantSlices:    []string{"accesscore/session-login", "sharedcrypto/hasher"},
			wantCells:     []string{"accesscore", "sharedcrypto"},
			wantContracts: []string{"http.auth.login.v1"},
		},
		{
			name:  "L0 cell without slices propagates to dependents",
			files: []string{"cells/sharedvalidate/cell.yaml"},
			// sharedvalidate is L0 with no slices. Changing its cell.yaml
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

func TestSelectFromFiles_ExampleMetadataPaths(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"ordercell": {
				ID:               "ordercell",
				ConsistencyLevel: "L2",
				File:             "examples/todoorder/cells/ordercell/cell.yaml",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"ordercell/ordercreate": {
				ID:            "ordercreate",
				BelongsToCell: "ordercell",
				File:          "examples/todoorder/cells/ordercell/slices/ordercreate/slice.yaml",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.order.create.v1", Role: "serve"},
				},
			},
			"ordercell/orderquery": {
				ID:            "orderquery",
				BelongsToCell: "ordercell",
				File:          "examples/todoorder/cells/ordercell/slices/orderquery/slice.yaml",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.order.list.v1", Role: "serve"},
				},
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.order.create.v1": {
				ID:        "http.order.create.v1",
				Kind:      "http",
				OwnerCell: "ordercell",
				Dir:       "examples/todoorder/contracts/http/order/create/v1",
				File:      "examples/todoorder/contracts/http/order/create/v1/contract.yaml",
				Endpoints: metadata.EndpointsMeta{
					HTTP: &metadata.HTTPTransportMeta{
						Responses: map[int]metadata.HTTPResponseMeta{
							400: {SchemaRef: "../../../../shared/errors/error-response-v1.schema.json"},
						},
					},
				},
			},
			"http.order.list.v1": {
				ID:        "http.order.list.v1",
				Kind:      "http",
				OwnerCell: "ordercell",
				Dir:       "examples/todoorder/contracts/http/order/list/v1",
				File:      "examples/todoorder/contracts/http/order/list/v1/contract.yaml",
				Endpoints: metadata.EndpointsMeta{
					HTTP: &metadata.HTTPTransportMeta{
						Responses: map[int]metadata.HTTPResponseMeta{
							400: {SchemaRef: "../../../../shared/errors/error-response-v1.schema.json"},
						},
					},
				},
			},
		},
		Journeys: map[string]*metadata.JourneyMeta{
			"J-ordercreate": {
				ID:        "J-ordercreate",
				Cells:     []string{"ordercell"},
				Contracts: []string{"http.order.create.v1"},
				File:      "examples/todoorder/journeys/J-ordercreate.yaml",
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
	ts := NewTargetSelector(project)

	cellResult := ts.SelectFromFiles([]string{"examples/todoorder/cells/ordercell/cell.go"})
	assert.Equal(t, []string{"ordercell/ordercreate", "ordercell/orderquery"}, cellResult.Slices)
	assert.Equal(t, []string{"ordercell"}, cellResult.Cells)
	assert.Equal(t, []string{"http.order.create.v1", "http.order.list.v1"}, cellResult.Contracts)
	assert.Equal(t, []string{"J-ordercreate"}, cellResult.Journeys)

	contractResult := ts.SelectFromFiles([]string{"examples/todoorder/contracts/http/order/create/v1/contract.yaml"})
	assert.Equal(t, []string{"ordercell/ordercreate"}, contractResult.Slices)
	assert.Equal(t, []string{"ordercell"}, contractResult.Cells)
	assert.Equal(t, []string{"http.order.create.v1"}, contractResult.Contracts)
	assert.Equal(t, []string{"J-ordercreate"}, contractResult.Journeys)

	sharedSchemaResult := ts.SelectFromFiles([]string{"examples/todoorder/contracts/shared/errors/error-response-v1.schema.json"})
	assert.Equal(t, []string{"ordercell/ordercreate", "ordercell/orderquery"}, sharedSchemaResult.Slices)
	assert.Equal(t, []string{"ordercell"}, sharedSchemaResult.Cells)
	assert.Equal(t, []string{"http.order.create.v1", "http.order.list.v1"}, sharedSchemaResult.Contracts)
	assert.Equal(t, []string{"J-ordercreate"}, sharedSchemaResult.Journeys)

	journeyResult := ts.SelectFromFiles([]string{"examples/todoorder/journeys/J-ordercreate.yaml"})
	assert.Equal(t, []string{"ordercell/ordercreate", "ordercell/orderquery"}, journeyResult.Slices)
	assert.Equal(t, []string{"ordercell"}, journeyResult.Cells)
	assert.Equal(t, []string{"http.order.create.v1", "http.order.list.v1"}, journeyResult.Contracts)
	assert.Equal(t, []string{"J-ordercreate"}, journeyResult.Journeys)
}
