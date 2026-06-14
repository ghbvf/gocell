package governance

// subscribe_governance_test.go covers the two governance rules that close the
// subscribe single-source loop:
//   - REF-18: actorSubscribers must list registered external actors only
//     (no cell IDs, no wildcards) — otherwise the derive step re-opens the
//     hand-written cell-subscriber bypass the flip was designed to close.
//   - FMT-35: the contractUsage handler/group/field columns are subscribe-only
//     (handler required for subscribe; all three forbidden otherwise).

import (
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/metadata/metadatatest"
)

// assertNoCode fails when any result carries the given code.
func assertNoCode(t *testing.T, results []ValidationResult, code RuleCode) {
	t.Helper()
	for _, r := range results {
		if r.Code == code {
			t.Errorf("did not expect a %s result, got: %v", code, r)
		}
	}
}

func eventContractWithActorSubs(id string, actorSubs []string) *metadata.ContractMeta {
	return &metadata.ContractMeta{
		ID:        id,
		Kind:      "event",
		Lifecycle: "active",
		Endpoints: metadata.EndpointsMeta{
			Publisher:        metadatatest.NewCellID("somecell"),
			ActorSubscribers: actorSubs,
		},
		File: "contracts/event/x/v1/contract.yaml",
	}
}

// TestREF18_RegisteredExternalActor_Passes verifies a registered external actor
// in actorSubscribers produces no REF-18 finding.
func TestREF18_RegisteredExternalActor_Passes(t *testing.T) {
	project := minimalGovernanceProject()
	project.Actors = []metadata.ActorMeta{{ID: "external-audit-sink"}}
	project.Contracts["event.audit.appended.v1"] = eventContractWithActorSubs("event.audit.appended.v1", []string{"external-audit-sink"})

	results := NewValidator(project, "", clock.Real()).validateREF18()
	assertNoCode(t, results, codeREF18)
}

// TestREF18_CellID_Rejected is the core F3 guard: a cell ID written into
// actorSubscribers re-opens a hand-written cell subscriber and must be rejected.
func TestREF18_CellID_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	project.Cells[metadatatest.CellIDAccessCore] = &metadata.CellMeta{ID: metadatatest.CellIDAccessCore}
	project.Contracts["event.session.created.v1"] = eventContractWithActorSubs("event.session.created.v1", []string{"accesscore"})

	results := NewValidator(project, "", clock.Real()).validateREF18()
	assertResultsContainCode(t, results, codeREF18, "actorSubscribers[0]")
}

// TestREF18_Wildcard_Rejected verifies a "*" wildcard in actorSubscribers is rejected.
func TestREF18_Wildcard_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	project.Contracts["event.user.created.v1"] = eventContractWithActorSubs("event.user.created.v1", []string{"*"})

	results := NewValidator(project, "", clock.Real()).validateREF18()
	assertResultsContainCode(t, results, codeREF18, "actorSubscribers[0]")
}

// TestREF18_UnregisteredActor_Rejected verifies an actorSubscribers entry that
// is neither a cell nor a registered actor is rejected as a dangling reference.
func TestREF18_UnregisteredActor_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	project.Contracts["event.user.created.v1"] = eventContractWithActorSubs("event.user.created.v1", []string{"ghost-system"})

	results := NewValidator(project, "", clock.Real()).validateREF18()
	assertResultsContainCode(t, results, codeREF18, "actorSubscribers[0]")
}

// --- FMT-35 ---

func sliceWithCU(cu metadata.ContractUsage) *metadata.SliceMeta {
	return &metadata.SliceMeta{
		ID:             "subs",
		BelongsToCell:  metadatatest.CellIDDemo,
		File:           "cells/demo/slices/subs/slice.yaml",
		ContractUsages: []metadata.ContractUsage{cu},
	}
}

// TestFMT35_SubscribeWithHandler_Passes verifies a well-formed subscribe CU passes.
func TestFMT35_SubscribeWithHandler_Passes(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "event.foo.v1", Role: "subscribe", Handler: "HandleFoo",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertNoCode(t, results, codeFMT35)
}

// TestFMT35_SubscribeMissingHandler_Rejected verifies subscribe without handler fails.
func TestFMT35_SubscribeMissingHandler_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "event.foo.v1", Role: "subscribe", Handler: "",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertResultsContainCode(t, results, codeFMT35, "contractUsages[0].handler")
}

// TestFMT35_NonSubscribeWithHandler_Rejected verifies handler on a non-subscribe
// role (e.g. serve) is rejected — the column is subscribe-only.
func TestFMT35_NonSubscribeWithHandler_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "http.users.v1", Role: "serve", Handler: "HandleHTTP",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertResultsContainCode(t, results, codeFMT35, "contractUsages[0].handler")
}

// TestFMT35_NonSubscribeWithGroupAndField_Rejected verifies group and field on a
// non-subscribe role each produce a finding.
func TestFMT35_NonSubscribeWithGroupAndField_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "event.foo.v1", Role: "publish", Group: "g", Field: "f",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertResultsContainCode(t, results, codeFMT35, "contractUsages[0].group")
	assertResultsContainCode(t, results, codeFMT35, "contractUsages[0].field")
}

// --- FMT-35: projection / onReset placement (F3) ---

// TestFMT35_NonSubscribeWithProjection_Rejected verifies that a non-subscribe
// CU (e.g. provide) with projection set produces a FMT-35 error.
func TestFMT35_NonSubscribeWithProjection_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract:   "data.foo.v1",
		Role:       "provide",
		Projection: "some_projection",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertResultsContainCode(t, results, codeFMT35, "contractUsages[0].projection")
}

// TestFMT35_NonSubscribeWithOnReset_Rejected verifies that a non-subscribe
// CU with onReset set produces a FMT-35 error.
func TestFMT35_NonSubscribeWithOnReset_Rejected(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract: "data.foo.v1",
		Role:     "serve",
		OnReset:  "ResetModel",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertResultsContainCode(t, results, codeFMT35, "contractUsages[0].onReset")
}

// TestFMT35_SubscribeWithProjectionAndOnReset_Passes verifies that a subscribe
// CU with projection and onReset set produces no FMT-35 finding on those columns.
func TestFMT35_SubscribeWithProjectionAndOnReset_Passes(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract:   "event.foo.v1",
		Role:       "subscribe",
		Handler:    "HandleFoo",
		Projection: "foo_projection",
		OnReset:    "ResetFoo",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	for _, r := range results {
		if r.Code == codeFMT35 {
			// Check neither projection nor onReset is the violating field
			if r.Field == "contractUsages[0].projection" || r.Field == "contractUsages[0].onReset" {
				t.Errorf("unexpected FMT-35 finding on subscribe CU with projection/onReset: %v", r)
			}
		}
	}
}

// TestFMT35_SubscribeWithProjectionOnly_Passes verifies that a subscribe CU
// with only projection set (no onReset) produces no FMT-35 finding.
func TestFMT35_SubscribeWithProjectionOnly_Passes(t *testing.T) {
	project := minimalGovernanceProject()
	project.Slices["demo/subs"] = sliceWithCU(metadata.ContractUsage{
		Contract:   "event.foo.v1",
		Role:       "subscribe",
		Handler:    "HandleFoo",
		Projection: "foo_projection",
	})

	results := NewValidator(project, "", clock.Real()).validateFMT35()
	assertNoCode(t, results, codeFMT35)
}
