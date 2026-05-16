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

func TestCONTRACTENDPOINTTESTMAPPING01_NonHTTPExempt(t *testing.T) {
	// kind = "event" → exempt (event contracts are handled by ADV-06).
	pm := minimalHTTPProject()
	pm.Contracts["http.auth.login.v1"].Kind = "event"
	// No serve entry added.

	val := NewValidator(pm, "", clock.Real())
	got := findByCode(val.validateCONTRACTENDPOINTTESTMAPPING01(), codeCONTRACTENDPOINTTESTMAPPING01)
	assert.Empty(t, got, "non-HTTP (event) contracts must not trigger this rule")
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
