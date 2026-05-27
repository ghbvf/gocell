package governance

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
)

// targetsProject returns a ProjectMeta for impact-analysis tests.
// 2 cells, 3 slices (some with contractUsages), 2 contracts, 2 journeys, 1 assembly.
func targetsProject() *metadata.ProjectMeta {
	// File fields are populated so targets.matchSliceFromCellsPath /
	// matchFromJourneyPath / matchFromAssemblyPath / contractPathMatches
	// can resolve changed file paths against parsed metadata. After M1
	// (#1082)'s Locator funnel, TargetSelector no longer falls back to
	// path-prefix derivation when File is empty.
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDAccessCore: {
				ID:               metadatatest.CellIDAccessCore,
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				File:             "cells/" + metadatatest.CellIDAccessCore + "/cell.yaml",
			},
			metadatatest.CellIDAuditCore: {
				ID:               metadatatest.CellIDAuditCore,
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				File:             "cells/" + metadatatest.CellIDAuditCore + "/cell.yaml",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"accesscore/session-login": {
				ID:            "session-login",
				BelongsToCell: metadatatest.CellIDAccessCore,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
					{Contract: "event.session.created.v1", Role: "publish"},
				},
				File: "cells/" + metadatatest.CellIDAccessCore + "/slices/session-login/slice.yaml",
			},
			"accesscore/session-refresh": {
				ID:            "session-refresh",
				BelongsToCell: metadatatest.CellIDAccessCore,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "call"},
				},
				File: "cells/" + metadatatest.CellIDAccessCore + "/slices/session-refresh/slice.yaml",
			},
			"auditcore/audit-write": {
				ID:            "audit-write",
				BelongsToCell: metadatatest.CellIDAuditCore,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "event.session.created.v1", Role: "subscribe"},
				},
				File: "cells/" + metadatatest.CellIDAuditCore + "/slices/audit-write/slice.yaml",
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.login.v1": {
				ID:        "http.auth.login.v1",
				Kind:      "http",
				OwnerCell: metadatatest.CellIDAccessCore,
				Lifecycle: "active",
				Dir:       "contracts/http/auth/login/v1",
				File:      "contracts/http/auth/login/v1/contract.yaml",
			},
			"event.session.created.v1": {
				ID:        "event.session.created.v1",
				Kind:      "event",
				OwnerCell: metadatatest.CellIDAccessCore,
				Lifecycle: "active",
				Dir:       "contracts/event/session/created/v1",
				File:      "contracts/event/session/created/v1/contract.yaml",
			},
		},
		Journeys: map[string]*metadata.JourneyMeta{
			"J-ssologin": {
				ID:        "J-ssologin",
				Goal:      "SSO login flow",
				Cells:     []string{metadatatest.CellIDAccessCore, metadatatest.CellIDAuditCore},
				Contracts: []string{"http.auth.login.v1", "event.session.created.v1"},
				File:      "journeys/J-ssologin.yaml",
			},
			"J-audit-trail": {
				ID:    "J-audit-trail",
				Goal:  "Audit trail for login",
				Cells: []string{metadatatest.CellIDAuditCore},
				File:  "journeys/J-audit-trail.yaml",
			},
		},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"corebundle": {
				ID:    "corebundle",
				Cells: []string{metadatatest.CellIDAccessCore, metadatatest.CellIDAuditCore},
				Build: metadata.BuildMeta{
					Entrypoint: "cmd/corebundle/main.go",
					Binary:     "corebundle",
				},
				File: "assemblies/corebundle/assembly.yaml",
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

	// After M1 (#1082)'s Locator funnel, TargetSelector no longer tries to
	// resolve a slice ID from path segments — it uses parsed
	// SliceMeta.File / CellMeta.File directly. A file inside the accesscore
	// cell directory therefore propagates impact to all of that cell's
	// known slices (the safe over-selection direction), even when the
	// specific slice path doesn't match any parsed slice. The prior fallback
	// silently dropped the file change, which under-selected.
	assert.Equal(t, []string{"accesscore/session-login", "accesscore/session-refresh"}, result.Slices)
	assert.Equal(t, []string{"accesscore"}, result.Cells)
	assert.Equal(t, []string{"J-ssologin"}, result.Journeys)
	assert.Equal(t, []string{"event.session.created.v1", "http.auth.login.v1"}, result.Contracts)
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
	// File fields are populated so targets.matchSliceFromCellsPath can
	// match changed file paths against parsed cell/slice/journey metadata
	// — after M1 (#1082)'s Locator funnel, TargetSelector relies on parsed
	// .File values rather than HasPrefix("cells/")-style path derivation.
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDSharedCrypto: {
				ID:               metadatatest.CellIDSharedCrypto,
				Type:             "support",
				ConsistencyLevel: "L0",
				File:             "cells/" + metadatatest.CellIDSharedCrypto + "/cell.yaml",
			},
			metadatatest.CellIDSharedValidate: {
				ID:               metadatatest.CellIDSharedValidate,
				Type:             "support",
				ConsistencyLevel: "L0",
				// L0 cell with NO slices — tests propagation for slice-less cells.
				File: "cells/" + metadatatest.CellIDSharedValidate + "/cell.yaml",
			},
			metadatatest.CellIDAccessCore: {
				ID:               metadatatest.CellIDAccessCore,
				Type:             "core",
				ConsistencyLevel: "L2",
				L0Dependencies: []metadata.L0DepMeta{
					{Cell: metadatatest.CellIDSharedCrypto, Reason: "hashing"},
					{Cell: metadatatest.CellIDSharedValidate, Reason: "input validation"},
				},
				File: "cells/" + metadatatest.CellIDAccessCore + "/cell.yaml",
			},
			metadatatest.CellIDAuditCore: {
				ID:               metadatatest.CellIDAuditCore,
				Type:             "core",
				ConsistencyLevel: "L2",
				// no L0 dependencies
				File: "cells/" + metadatatest.CellIDAuditCore + "/cell.yaml",
			},
			metadatatest.CellIDBillingCore: {
				ID:               metadatatest.CellIDBillingCore,
				Type:             "core",
				ConsistencyLevel: "L2",
				L0Dependencies: []metadata.L0DepMeta{
					{Cell: metadatatest.CellIDSharedCrypto, Reason: "signature"},
				},
				// NOT referenced by J-l0-test journey — used to test
				// that journey changes don't trigger L0 propagation.
				File: "cells/" + metadatatest.CellIDBillingCore + "/cell.yaml",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"sharedcrypto/hasher": {
				ID:            "hasher",
				BelongsToCell: metadatatest.CellIDSharedCrypto,
				File:          "cells/" + metadatatest.CellIDSharedCrypto + "/slices/hasher/slice.yaml",
			},
			// sharedvalidate has NO slices (intentional).
			"accesscore/session-login": {
				ID:            "session-login",
				BelongsToCell: metadatatest.CellIDAccessCore,
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.auth.login.v1", Role: "serve"},
				},
				File: "cells/" + metadatatest.CellIDAccessCore + "/slices/session-login/slice.yaml",
			},
			"auditcore/audit-write": {
				ID:            "audit-write",
				BelongsToCell: metadatatest.CellIDAuditCore,
				File:          "cells/" + metadatatest.CellIDAuditCore + "/slices/audit-write/slice.yaml",
			},
			"billingcore/payment": {
				ID:            "payment",
				BelongsToCell: metadatatest.CellIDBillingCore,
				File:          "cells/" + metadatatest.CellIDBillingCore + "/slices/payment/slice.yaml",
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			"http.auth.login.v1": {
				ID:   "http.auth.login.v1",
				Kind: "http",
				Dir:  "contracts/http/auth/login/v1",
				File: "contracts/http/auth/login/v1/contract.yaml",
			},
		},
		Journeys: map[string]*metadata.JourneyMeta{
			"J-l0-test": {
				ID:    "J-l0-test",
				Cells: []string{metadatatest.CellIDSharedCrypto, metadatatest.CellIDAccessCore},
				File:  "journeys/J-l0-test.yaml",
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
			metadatatest.CellIDOrderCell: {
				ID:               metadatatest.CellIDOrderCell,
				ConsistencyLevel: "L2",
				File:             "examples/todoorder/cells/ordercell/cell.yaml",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"ordercell/ordercreate": {
				ID:            "ordercreate",
				BelongsToCell: metadatatest.CellIDOrderCell,
				File:          "examples/todoorder/cells/ordercell/slices/ordercreate/slice.yaml",
				ContractUsages: []metadata.ContractUsage{
					{Contract: "http.order.create.v1", Role: "serve"},
				},
			},
			"ordercell/orderquery": {
				ID:            "orderquery",
				BelongsToCell: metadatatest.CellIDOrderCell,
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
				OwnerCell: metadatatest.CellIDOrderCell,
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
				OwnerCell: metadatatest.CellIDOrderCell,
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
				Cells:     []string{metadatatest.CellIDOrderCell},
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

// TestSelectFromFiles_ExampleAssemblyPath verifies that matchFromAssemblyPath
// accepts examples/{id}/assembly.yaml paths (not just assemblies/).
// Regression test for F1: examples/ prefix was silently ignored.
func TestSelectFromFiles_ExampleAssemblyPath(t *testing.T) {
	project := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			metadatatest.CellIDOrderCell: {
				ID:               metadatatest.CellIDOrderCell,
				ConsistencyLevel: "L2",
			},
			metadatatest.NewCellID("auditcell"): {
				ID:               metadatatest.NewCellID("auditcell"),
				ConsistencyLevel: "L2",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"ordercell/ordercreate": {
				ID:            "ordercreate",
				BelongsToCell: metadatatest.CellIDOrderCell,
			},
			"auditcell/auditwrite": {
				ID:            "auditwrite",
				BelongsToCell: metadatatest.NewCellID("auditcell"),
			},
		},
		Contracts: map[string]*metadata.ContractMeta{},
		Journeys:  map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{
			"todoorder": {
				ID:    "todoorder",
				Cells: []string{metadatatest.CellIDOrderCell, metadatatest.NewCellID("auditcell")},
				File:  "examples/todoorder/assembly.yaml",
			},
		},
	}
	ts := NewTargetSelector(project)

	// examples/todoorder/assembly.yaml must add both cells to the result.
	result := ts.SelectFromFiles([]string{"examples/todoorder/assembly.yaml"})
	assert.Equal(t, []string{"auditcell/auditwrite", "ordercell/ordercreate"}, result.Slices)
	assert.Equal(t, []string{"auditcell", "ordercell"}, result.Cells)
}
