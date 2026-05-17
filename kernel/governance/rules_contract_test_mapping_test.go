package governance

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// --- CONTRACT-ENDPOINT-TEST-MAPPING-01 ---
//
// Rule: every active HTTP contract must be referenced by at least one slice in
// the server cell's verify.contract list as "contract.<id>.serve".
// Exemptions: examples/ contracts and non-active lifecycle.

func TestCONTRACTENDPOINTTESTMAPPING01_Happy(t *testing.T) {
	// 1 active HTTP contract + 1 slice verify.contract contains .serve → no result.
	pm := minimalHTTPProject()
	addServeToSlice(pm, "accesscore/session-login", "http.auth.login.v1")

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateCONTRACTENDPOINTTESTMAPPING01(), codeCONTRACTENDPOINTTESTMAPPING01)
	assert.Empty(t, got, "active HTTP contract served by a slice must produce no findings")
}

func TestCONTRACTENDPOINTTESTMAPPING01_MissingServe(t *testing.T) {
	// 1 active HTTP contract + slice verify.contract does NOT contain .serve → 1 error.
	pm := minimalHTTPProject()
	// Do NOT add a serve entry to any slice.

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateCONTRACTENDPOINTTESTMAPPING01(), codeCONTRACTENDPOINTTESTMAPPING01)
	require.Len(t, got, 1)
	r := got[0]
	assert.Equal(t, SeverityError, r.Severity)
	assert.Equal(t, IssueRequired, r.IssueType)
	assert.Equal(t, "contracts/http/auth/login/v1/contract.yaml", r.File)
	assert.Equal(t, "id", r.Field)
	assert.True(t, strings.Contains(r.Message, "; fix:"),
		"SeverityError messages must include `; fix:` anchor; got: %s", r.Message)
	assert.True(t, strings.Contains(r.Message, "contract.http.auth.login.v1.serve"),
		"error message must include the expected verify.contract entry; got: %s", r.Message)
}

func TestCONTRACTENDPOINTTESTMAPPING01_ExamplesExempt(t *testing.T) {
	// contract.File starts with "examples/" → exempt regardless of slice coverage.
	pm := minimalHTTPProject()
	pm.Contracts["http.auth.login.v1"].File = "examples/todoorder/contracts/http/auth/login/v1/contract.yaml"
	pm.Contracts["http.auth.login.v1"].Dir = "examples/todoorder/contracts/http/auth/login/v1"
	// No serve entry added — should still produce no findings.

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateCONTRACTENDPOINTTESTMAPPING01(), codeCONTRACTENDPOINTTESTMAPPING01)
	assert.Empty(t, got, "examples/ contracts must not require slice serve coverage")
}

func TestCONTRACTENDPOINTTESTMAPPING01_NonActiveExempt(t *testing.T) {
	// lifecycle = "experimental" → exempt.
	pm := minimalHTTPProject()
	pm.Contracts["http.auth.login.v1"].Lifecycle = "experimental"
	// No serve entry added.

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateCONTRACTENDPOINTTESTMAPPING01(), codeCONTRACTENDPOINTTESTMAPPING01)
	assert.Empty(t, got, "non-active (experimental) contracts must not require serve coverage")
}

func TestCONTRACTENDPOINTTESTMAPPING01_DeprecatedExempt(t *testing.T) {
	// lifecycle = "deprecated" → exempt (same policy as experimental).
	pm := minimalHTTPProject()
	pm.Contracts["http.auth.login.v1"].Lifecycle = "deprecated"
	// No serve entry added.

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateCONTRACTENDPOINTTESTMAPPING01(), codeCONTRACTENDPOINTTESTMAPPING01)
	assert.Empty(t, got, "deprecated contracts must not require slice serve coverage")
}

func TestCONTRACTENDPOINTTESTMAPPING01_NonHTTPExempt(t *testing.T) {
	// kind = "event" → exempt (event contracts are handled by ADV-06).
	pm := minimalHTTPProject()
	pm.Contracts["http.auth.login.v1"].Kind = "event"
	// No serve entry added.

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateCONTRACTENDPOINTTESTMAPPING01(), codeCONTRACTENDPOINTTESTMAPPING01)
	assert.Empty(t, got, "non-HTTP (event) contracts must not trigger this rule")
}

// TestCONTRACTENDPOINTTESTMAPPING01_SliceServeMissingContract guards direction B
// (slice → contract) case 1: slice declares "contract.X.serve" but contract X
// does not exist in the project. Previously silent — review F4 fixed.
func TestCONTRACTENDPOINTTESTMAPPING01_SliceServeMissingContract(t *testing.T) {
	pm := minimalHTTPProject()
	// Slice declares a serve entry pointing at a contract that does not exist.
	addServeToSlice(pm, "accesscore/session-login", "http.does.not.exist.v1")

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateCONTRACTENDPOINTTESTMAPPING01(), codeCONTRACTENDPOINTTESTMAPPING01)
	// One direction B failure (missing contract) and one direction A failure
	// (the existing real contract http.auth.login.v1 still has no serve).
	require.GreaterOrEqual(t, len(got), 1)
	var found bool
	for _, r := range got {
		if r.IssueType == IssueRefNotFound &&
			strings.Contains(r.Message, "http.does.not.exist.v1") &&
			strings.Contains(r.Message, "does not exist") &&
			strings.Contains(r.Message, "; fix:") {
			assert.Equal(t, SeverityError, r.Severity)
			assert.Equal(t, "cells/accesscore/slices/session-login/slice.yaml", r.File)
			found = true
			break
		}
	}
	assert.True(t, found, "direction B must report missing contract; got: %v", got)
}

// TestCONTRACTENDPOINTTESTMAPPING01_SliceServeNonHTTPContract guards direction B
// case 2: slice's .serve entry references an event-kind contract. The .serve role
// is HTTP-only; event contracts use ADV-06.
func TestCONTRACTENDPOINTTESTMAPPING01_SliceServeNonHTTPContract(t *testing.T) {
	pm := minimalHTTPProject()
	const eventID = "event.session.created.v1"
	pm.Contracts[eventID] = &metadata.ContractMeta{
		ID:        eventID,
		Kind:      "event",
		Lifecycle: "active",
		Endpoints: metadata.EndpointsMeta{Server: "accesscore"},
		Dir:       "contracts/event/session/created/v1",
		File:      "contracts/event/session/created/v1/contract.yaml",
	}
	addServeToSlice(pm, "accesscore/session-login", eventID)

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateCONTRACTENDPOINTTESTMAPPING01(), codeCONTRACTENDPOINTTESTMAPPING01)
	var found bool
	for _, r := range got {
		if r.IssueType == IssueMismatch &&
			strings.Contains(r.Message, eventID) &&
			strings.Contains(r.Message, `kind is "event"`) &&
			strings.Contains(r.Message, "; fix:") {
			assert.Equal(t, SeverityError, r.Severity)
			found = true
			break
		}
	}
	assert.True(t, found, "direction B must report non-HTTP kind; got: %v", got)
}

// TestCONTRACTENDPOINTTESTMAPPING01_SliceServeNonActiveContract guards
// direction B case 3: slice's .serve entry references a deprecated contract.
func TestCONTRACTENDPOINTTESTMAPPING01_SliceServeNonActiveContract(t *testing.T) {
	pm := minimalHTTPProject()
	// Demote the existing contract to deprecated lifecycle.
	pm.Contracts["http.auth.login.v1"].Lifecycle = "deprecated"
	addServeToSlice(pm, "accesscore/session-login", "http.auth.login.v1")

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateCONTRACTENDPOINTTESTMAPPING01(), codeCONTRACTENDPOINTTESTMAPPING01)
	var found bool
	for _, r := range got {
		if r.IssueType == IssueMismatch &&
			strings.Contains(r.Message, "http.auth.login.v1") &&
			strings.Contains(r.Message, `lifecycle is "deprecated"`) &&
			strings.Contains(r.Message, "; fix:") {
			assert.Equal(t, SeverityError, r.Severity)
			found = true
			break
		}
	}
	assert.True(t, found, "direction B must report non-active lifecycle; got: %v", got)
}

// TestCONTRACTENDPOINTTESTMAPPING01_SliceServeExamplesContract guards direction B
// case 4: platform slice's .serve entry references a contract under examples/.
// Platform must not serve example contracts (CLAUDE.md "依赖规则": examples
// depend on platform, not the reverse).
func TestCONTRACTENDPOINTTESTMAPPING01_SliceServeExamplesContract(t *testing.T) {
	pm := minimalHTTPProject()
	const exampleID = "http.todo.order.create.v1"
	pm.Contracts[exampleID] = &metadata.ContractMeta{
		ID:        exampleID,
		Kind:      "http",
		Lifecycle: "active",
		Endpoints: metadata.EndpointsMeta{Server: "ordercell",
			HTTP: &metadata.HTTPTransportMeta{Method: "POST", Path: "/api/v1/orders", SuccessStatus: 201}},
		Dir:  "examples/todoorder/contracts/http/todo/order/create/v1",
		File: "examples/todoorder/contracts/http/todo/order/create/v1/contract.yaml",
	}
	addServeToSlice(pm, "accesscore/session-login", exampleID)

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateCONTRACTENDPOINTTESTMAPPING01(), codeCONTRACTENDPOINTTESTMAPPING01)
	var found bool
	for _, r := range got {
		if r.IssueType == IssueForbidden &&
			strings.Contains(r.Message, exampleID) &&
			strings.Contains(r.Message, "examples/") &&
			strings.Contains(r.Message, "; fix:") {
			assert.Equal(t, SeverityError, r.Severity)
			found = true
			break
		}
	}
	assert.True(t, found, "direction B must reject platform .serve of examples/ contract; got: %v", got)
}

// TestCONTRACTENDPOINTTESTMAPPING01_SliceServeExamplesSelf asserts that an
// examples slice serving an examples contract within the same project is
// allowed. The platform→examples direction is the forbidden one
// (TestCONTRACTENDPOINTTESTMAPPING01_SliceServeExamplesContract above);
// examples→examples must remain legal because examples may depend on all
// layers (CLAUDE.md "依赖规则").
func TestCONTRACTENDPOINTTESTMAPPING01_SliceServeExamplesSelf(t *testing.T) {
	pm := minimalHTTPProject()
	const exampleCell = "ordercell"
	pm.Cells[exampleCell] = &metadata.CellMeta{
		ID:               exampleCell,
		Type:             "core",
		ConsistencyLevel: "L1",
		Owner:            metadata.OwnerMeta{Team: "demo", Role: "cell-owner"},
		Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.ordercell.startup"}},
		Dir:              "examples/todoorder/cells/" + exampleCell,
		File:             "examples/todoorder/cells/" + exampleCell + "/cell.yaml",
	}
	const exampleContract = "http.todo.order.create.v1"
	pm.Contracts[exampleContract] = &metadata.ContractMeta{
		ID:        exampleContract,
		Kind:      "http",
		Lifecycle: "active",
		Endpoints: metadata.EndpointsMeta{
			Server: exampleCell,
			HTTP:   &metadata.HTTPTransportMeta{Method: "POST", Path: "/api/v1/orders", SuccessStatus: 201},
		},
		Dir:  "examples/todoorder/contracts/http/todo/order/create/v1",
		File: "examples/todoorder/contracts/http/todo/order/create/v1/contract.yaml",
	}
	// Examples slice serving the examples contract within the same project.
	pm.Slices[exampleCell+"/ordercreate"] = &metadata.SliceMeta{
		ID:            "ordercreate",
		BelongsToCell: exampleCell,
		ContractUsages: []metadata.ContractUsage{
			{Contract: exampleContract, Role: "serve"},
		},
		Verify: metadata.SliceVerifyMeta{
			Unit:     []string{"unit.ordercreate.service"},
			Contract: []string{"contract." + exampleContract + ".serve"},
		},
		AllowedFiles: []string{"examples/todoorder/cells/" + exampleCell + "/slices/ordercreate/**"},
		Dir:          "ordercreate",
		CellDir:      "examples/todoorder/cells/" + exampleCell,
		File:         "examples/todoorder/cells/" + exampleCell + "/slices/ordercreate/slice.yaml",
	}

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateCONTRACTENDPOINTTESTMAPPING01(), codeCONTRACTENDPOINTTESTMAPPING01)
	for _, r := range got {
		// Allowed: examples → examples self-serve must not produce a finding.
		assert.NotContains(t, r.Message, exampleContract,
			"examples slice → examples contract must not be flagged; got: %s", r.Message)
	}
}

// TestCONTRACTENDPOINTTESTMAPPING01_SliceServeServerMismatch guards direction B
// (slice → contract): a slice declares "contract.X.serve" in verify.contract but
// contract X's endpoints.server is a different cell than the slice's belongsToCell.
func TestCONTRACTENDPOINTTESTMAPPING01_SliceServeServerMismatch(t *testing.T) {
	pm := minimalHTTPProject()
	// slice belongs to "accesscore", but contract endpoints.server is also "accesscore"
	// in minimalHTTPProject. To create a mismatch: change the slice to belong to a
	// different cell but still declare the .serve entry for the contract.
	const otherCell = "configcore"
	pm.Cells[otherCell] = &metadata.CellMeta{
		ID:               otherCell,
		Type:             "core",
		ConsistencyLevel: "L1",
		Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
		Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke." + otherCell + ".startup"}},
		Dir:              otherCell,
		File:             "cells/" + otherCell + "/cell.yaml",
	}
	// Add a second slice in configcore that declares .serve for a contract owned by accesscore.
	pm.Slices[otherCell+"/flag-read"] = &metadata.SliceMeta{
		ID:            "flag-read",
		BelongsToCell: otherCell,
		ContractUsages: []metadata.ContractUsage{
			{Contract: "http.auth.login.v1", Role: "serve"},
		},
		Verify: metadata.SliceVerifyMeta{
			Unit:     []string{"unit.flag-read.service"},
			Contract: []string{"contract.http.auth.login.v1.serve"},
		},
		AllowedFiles: []string{"cells/" + otherCell + "/slices/flag-read/**"},
		Dir:          "flag-read",
		CellDir:      otherCell,
		File:         "cells/" + otherCell + "/slices/flag-read/slice.yaml",
	}

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateCONTRACTENDPOINTTESTMAPPING01(), codeCONTRACTENDPOINTTESTMAPPING01)
	// Direction B should fire: configcore/flag-read declares .serve for http.auth.login.v1
	// but that contract's endpoints.server = "accesscore", not "configcore".
	require.GreaterOrEqual(t, len(got), 1, "direction B must fire when slice's cell ≠ contract's endpoints.server")
	var found bool
	for _, r := range got {
		if r.Severity == SeverityError && r.IssueType == IssueMismatch &&
			strings.Contains(r.Message, "configcore") {
			found = true
			break
		}
	}
	assert.True(t, found, "direction B result must reference the mismatched cell; got: %v", got)
}

// TestCONTRACTENDPOINTTESTMAPPING01_CandidateSliceHint verifies that direction A
// error messages include candidate slice paths (F13 fix).
func TestCONTRACTENDPOINTTESTMAPPING01_CandidateSliceHint(t *testing.T) {
	t.Run("single candidate", func(t *testing.T) {
		pm := minimalHTTPProject()
		// minimalHTTPProject has one slice in accesscore. No serve entry → direction A fires.
		val := NewValidator(pm, "", clock.Real())
		got := findByCode(val.validateCONTRACTENDPOINTTESTMAPPING01(), codeCONTRACTENDPOINTTESTMAPPING01)
		require.Len(t, got, 1)
		assert.Contains(t, got[0].Message, "candidate slices:",
			"error message must include candidate slice hint; got: %s", got[0].Message)
		assert.Contains(t, got[0].Message, "cells/accesscore/slices/session-login/slice.yaml",
			"candidate hint must include the actual slice file; got: %s", got[0].Message)
	})

	t.Run("no candidate when cell has no slices", func(t *testing.T) {
		pm := minimalHTTPProject()
		// Change server to a cell that exists but has no slices.
		pm.Contracts["http.auth.login.v1"].Endpoints.Server = "auditcore"
		pm.Cells["auditcore"] = &metadata.CellMeta{
			ID:   "auditcore",
			Type: "core",
			File: "cells/auditcore/cell.yaml",
		}
		val := NewValidator(pm, "", clock.Real())
		got := findByCode(val.validateCONTRACTENDPOINTTESTMAPPING01(), codeCONTRACTENDPOINTTESTMAPPING01)
		require.Len(t, got, 1)
		assert.Contains(t, got[0].Message, "no slice belongs to owner cell auditcore",
			"error message must suggest creating a slice; got: %s", got[0].Message)
	})
}

// TestCONTRACTENDPOINTTESTMAPPING01_Integrated guards against silent de-registration:
// a refactor that removes the rule from rules() would let per-method tests still pass
// while the rule no longer fires in the real validate pipeline.
func TestCONTRACTENDPOINTTESTMAPPING01_Integrated(t *testing.T) {
	pm := minimalHTTPProject()
	// No serve entry → expect the rule to fire via ValidateStrict.

	val := NewValidator(pm, "", clock.Real())
	results, err := val.ValidateStrict(t.Context(), false, false)
	require.NoError(t, err)

	got := findByCode(results, codeCONTRACTENDPOINTTESTMAPPING01)
	require.Len(t, got, 1, "CONTRACT-ENDPOINT-TEST-MAPPING-01 must fire via ValidateStrict")
	assert.Equal(t, SeverityError, got[0].Severity)
}

// =============================================================================
// helpers specific to this rule's tests
// =============================================================================

// minimalHTTPProject returns a ProjectMeta with one active HTTP contract
// (http.auth.login.v1) owned by the platform "accesscore" cell and one slice
// in that cell, with no verify.contract serve entries. Cell ID and contract
// ID are intentionally fixed — every test in this file targets the same shape;
// varying them would require a richer fixture and is not needed for this
// rule's coverage. Tests mutate the returned project to set up each scenario.
func minimalHTTPProject() *metadata.ProjectMeta {
	const (
		cellID     = "accesscore"
		contractID = "http.auth.login.v1"
	)
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			cellID: {
				ID:               cellID,
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "platform", Role: "cell-owner"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke." + cellID + ".startup"}},
				Dir:              cellID,
				File:             "cells/" + cellID + "/cell.yaml",
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			cellID + "/session-login": {
				ID:            "session-login",
				BelongsToCell: cellID,
				ContractUsages: []metadata.ContractUsage{
					{Contract: contractID, Role: "serve"},
				},
				Verify: metadata.SliceVerifyMeta{
					Unit: []string{"unit.session-login.service"},
					// verify.contract intentionally empty — tests add serve entries as needed
					Contract: []string{},
				},
				AllowedFiles: []string{"cells/" + cellID + "/slices/session-login/**"},
				Dir:          "session-login",
				CellDir:      cellID,
				File:         "cells/" + cellID + "/slices/session-login/slice.yaml",
			},
		},
		Contracts: map[string]*metadata.ContractMeta{
			contractID: {
				ID:               contractID,
				Kind:             "http",
				OwnerCell:        cellID,
				ConsistencyLevel: "L1",
				Lifecycle:        "active",
				Endpoints: metadata.EndpointsMeta{
					Server:  cellID,
					Clients: []string{},
					HTTP: &metadata.HTTPTransportMeta{
						Method:        "POST",
						Path:          "/api/v1/auth/login",
						SuccessStatus: 200,
					},
				},
				Dir:  "contracts/http/auth/login/v1",
				File: "contracts/http/auth/login/v1/contract.yaml",
			},
		},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// addServeToSlice appends "contract.<contractID>.serve" to the named slice's
// verify.contract list.
func addServeToSlice(pm *metadata.ProjectMeta, sliceKey, contractID string) {
	s := pm.Slices[sliceKey]
	if s == nil {
		return
	}
	entry := "contract." + contractID + ".serve"
	s.Verify.Contract = append(s.Verify.Contract, entry)
}
