package governance

// adv06_removal_test.go tests that:
//   1. codeADV06 constant no longer exists in rulecodes.go — covered by goldenRuleIDs()
//   2. validateADV06 is removed from allRules — covered by TestADV06_NotInRules
//   3. ADV-05 fires when Subscribers is empty (no cells, no actors)
//   4. ADV-05 passes when contract has ActorSubscribers that populate Subscribers
//
// These are TDD RED tests: they reflect the post-flip invariants.
// Tests 1+2 are implicitly covered by TestAllRulesMatchGolden
// via goldenRuleIDs() not listing "ADV-06".

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

func minimalGovernanceProject() *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells:      map[string]*metadata.CellMeta{},
		Slices:     map[string]*metadata.SliceMeta{},
		Contracts:  map[string]*metadata.ContractMeta{},
		Journeys:   map[string]*metadata.JourneyMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// assertResultsContainCode checks that at least one result has the given code + field.
func assertResultsContainCode(t *testing.T, results []ValidationResult, code RuleCode, field string) {
	t.Helper()
	for _, r := range results {
		if r.Code == code && r.Field == field {
			return
		}
	}
	t.Errorf("expected a %s result for field %q; got %d results: %v", code, field, len(results), results)
}

// TestADV05_EmptySubscribers_NoActors asserts ADV-05 fires when a contract has
// no cell subscribers AND no ActorSubscribers (so Subscribers is empty).
func TestADV05_EmptySubscribers_NoActors(t *testing.T) {
	project := minimalGovernanceProject()
	project.Contracts["event.dead.v1"] = &metadata.ContractMeta{
		ID:        "event.dead.v1",
		Kind:      "event",
		Lifecycle: "active",
		Endpoints: metadata.EndpointsMeta{
			Publisher:        "somecell",
			ActorSubscribers: nil,
			Subscribers:      nil, // derived = empty (no cells, no actors)
		},
		File: "contracts/event/dead/v1/contract.yaml",
	}

	v := NewValidator(project, "", clock.Real())
	results := v.validateADV05()
	// Field anchors at the locatable lifecycle field, not the derived
	// endpoints.subscribers (which carries no YAML position).
	assertResultsContainCode(t, results, codeADV05, "lifecycle")
}

// TestADV05_ActorSubscribers_PopulatesSubscribers asserts ADV-05 passes when
// the Subscribers field is populated (by derive) from ActorSubscribers.
//
// In the post-flip model, the test directly sets Subscribers (as derive would),
// simulating the output of deriveEventSubscribers having merged ActorSubscribers.
func TestADV05_ActorSubscribers_PopulatesSubscribers(t *testing.T) {
	project := minimalGovernanceProject()
	project.Contracts["event.audit.appended.v1"] = &metadata.ContractMeta{
		ID:        "event.audit.appended.v1",
		Kind:      "event",
		Lifecycle: "active",
		Endpoints: metadata.EndpointsMeta{
			Publisher:        "auditcore",
			ActorSubscribers: []string{"external-audit-sink"},
			// Subscribers is the derived field; simulate post-derive state:
			Subscribers: []string{"external-audit-sink"},
		},
		File: "contracts/event/audit/appended/v1/contract.yaml",
	}

	v := NewValidator(project, "", clock.Real())
	results := v.validateADV05()
	// No ADV-05 error: Subscribers is non-empty because actors are present
	for _, r := range results {
		if r.Code == codeADV05 {
			t.Errorf("ADV-05 must not fire when Subscribers is non-empty (actors present); got: %v", r)
		}
	}
}

// TestADV06_NotInRules asserts that no result has code == "ADV-06" after the rule
// is removed from the pipeline. We iterate allRules and confirm no result has code "ADV-06".
func TestADV06_NotInRules(t *testing.T) {
	project := minimalGovernanceProject()
	// Add a contract with no subscribers to guarantee ADV-05 fires if present,
	// ensuring the rules pipeline runs all rules.
	project.Contracts["event.test.v1"] = &metadata.ContractMeta{
		ID:        "event.test.v1",
		Kind:      "event",
		Lifecycle: "active",
		Endpoints: metadata.EndpointsMeta{Publisher: "testcell"},
		File:      "contracts/event/test/v1/contract.yaml",
	}

	v := NewValidator(project, "", clock.Real())
	// Only iterate non-Strict/non-Health rules: PhaseStrict rules (e.g. VERIFY-06)
	// require v.runCtx to be set via run(); PhaseHealth rules require a full project.
	// ADV-06 was a PhaseBase/advisory rule, so limiting to PhaseBase+PhaseDep is sufficient.
	for _, rule := range rulesForPhases(PhaseBase, PhaseDep) {
		for _, r := range rule.Detect(v) {
			if r.Code == "ADV-06" {
				t.Errorf("ADV-06 must not be emitted by allRules: found result %v", r)
			}
		}
	}
}
