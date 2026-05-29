// INVARIANT: GOVERNANCE-RULE-REACHABILITY-TEST-01

package governance

import (
	"sort"
	"strings"
	"testing"
)

// TestAllRulesMatchGolden proves that the set of Rule.Code values in allRules
// (rules_registry.go) is exactly goldenRuleIDs(), with no duplicates. This is
// the single registration invariant after the M3 rule-engine migration
// (ADR §M3-RULE-ENGINE): allRules is the sole source of truth, the engine runs
// every entry structurally (no separate dispatch list can drift), and direct
// enumeration is strictly stronger than the former BFS reachability traversal
// over former dispatch lists (replaced by allRules in §M3).
//
// The uniqueness + golden-set pairing also locks the Code↔Detect binding: a
// Rule literal that pairs a code with the wrong Detect method either drops the
// other method's code from the set (caught by the golden diff) or duplicates a
// code (caught by the uniqueness check). Combined with the per-rule unit tests
// (each asserts its method emits its own code) and archtest
// GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01 (codes come from rulecodes.go
// consts), the wiring is drift-locked.
//
// ref: kubernetes/apimachinery pkg/util/validation/field/errors_test.go
// (golden error-code allowlist + direct enumeration check).
func TestAllRulesMatchGolden(t *testing.T) {
	t.Parallel()

	actual := make([]string, 0, len(allRules))
	seen := make(map[RuleCode]struct{}, len(allRules))
	for _, r := range allRules {
		if _, dup := seen[r.Code]; dup {
			t.Fatalf("duplicate rule code in allRules: %s — each rule must appear exactly once", r.Code)
		}
		seen[r.Code] = struct{}{}
		actual = append(actual, string(r.Code))
	}
	sort.Strings(actual)

	if diff := symmetricDiff(goldenRuleIDs(), actual); len(diff) > 0 {
		t.Fatalf("allRules ↔ goldenRuleIDs() drift detected.\n"+
			"To fix: add or remove the rule from allRules in rules_registry.go, "+
			"OR update goldenRuleIDs() if the change is intentional.\n"+
			"Diff (- only in golden, + only in allRules):\n%s",
			strings.Join(diff, "\n"))
	}
}

// goldenRuleIDs returns the pinned set of all rule IDs declared in
// kernel/governance/*.go. Update this list whenever a rule is added /
// renamed / removed; TestAllRulesMatchGolden locks it against allRules, so the
// list itself is the count — no hand-maintained total is kept here.
func goldenRuleIDs() []string {
	return []string{
		// ADV — advisory warnings (rules_misc_advisory.go).
		// ADV-02 retired before PR-FUNNEL-03; ADV-06 removed when Subscribers
		// became a derived field (subscribers are always in sync by construction).
		// Gaps are intentional.
		"ADV-01", "ADV-03", "ADV-04", "ADV-05",

		// CONTRACT-ENDPOINT-TEST-MAPPING — active HTTP contract → slice.verify.contract.serve
		// reverse coverage check (rules_contract_test_mapping.go).
		"CONTRACT-ENDPOINT-TEST-MAPPING-01",

		// CH — contract-health (contracthealth.go + rules_http.go)
		"CH-01", "CH-02", "CH-03", "CH-04", "CH-05", "CH-06",

		// CONTRACT-CONSISTENCY-EMIT — http trigger ↔ outbox emit alignment
		// (rules_misc_consistency.go)
		"CONTRACT-CONSISTENCY-EMIT-01",

		// DEP — dependency graph (depcheck.go)
		"DEP-01", "DEP-02", "DEP-03",

		// DOC-NAME — document literal scanning (rules_misc_advisory.go;
		// strict-mode orchestrator is in rules_misc_strict.go)
		"DOC-NAME-01",

		// FMT — format / structural (rules_fmt.go for FMT-01..15, 24, 26..33
		// + strict-mode FMT-16/17/19/A1/C1 + FMT-20..23/25 in
		// rules_misc_strict.go; FMT-19 implementation in rules_misc_advisory.go).
		"FMT-01", "FMT-02", "FMT-03", "FMT-04", "FMT-05",
		"FMT-06", "FMT-07", "FMT-08", "FMT-09", "FMT-10",
		"FMT-11", "FMT-12", "FMT-13", "FMT-14", "FMT-15",
		// FMT-18 deleted in PR-V1-CODEGEN-FULL-MIGRATION W4 (replaced by
		// archtest CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01); gap intentional.
		// FMT-31 (rules_fmt.go) reclaimed the /internal/v1 caller-clients
		// invariant at the YAML governance layer (charter §5.1 L5→L6 carrier
		// migration, replaces tools/archtest/contract_spec_clients_test.go).
		"FMT-16", "FMT-17", "FMT-19",
		"FMT-20", "FMT-21", "FMT-22", "FMT-23", "FMT-24", "FMT-25",
		"FMT-26", "FMT-27", "FMT-28", "FMT-29", "FMT-30", "FMT-31",
		"FMT-32", "FMT-33", "FMT-34",
		// FMT-35: subscribe CU field placement (handler required for subscribe;
		// handler/group/field forbidden for non-subscribe roles).
		"FMT-35",
		// FMT-36: cell.requires ∈ closed enum {postgres,redis,rabbitmq}, no dups (Design Y, #855).
		"FMT-36",
		// FMT-37: webhook contract-side required fields — inbound→signature+payload;
		// signature.algorithm==hmac-sha256 (live parity with FMT-04; #1265).
		"FMT-37",
		"FMT-A1", "FMT-C1",

		// JOURNEY — journey lifecycle & cross-file consistency
		// (rules_journey.go). Inverse-direction REF-07 closure +
		// board.state × yaml.lifecycle strong-mapping matrix. AI-robust
		// Medium.
		"JOURNEY-CONTRACT-EXISTENCE-01", "JOURNEY-STATUS-LIFECYCLE-01",

		// OUTGUARD — outbox durability (rules_misc_advisory.go)
		"OUTGUARD-01",

		// PROJECTION-CONSISTENCY — projection contract must be >= L3
		// (rules_projection_consistency.go)
		"PROJECTION-CONSISTENCY-01",

		// REF — reference integrity (rules_ref.go for REF-01..11, 13..17;
		// REF-12 was relocated to rules_fmt.go in PR-FUNNEL-03 because it is
		// I/O-flavored — pairs with FMT cluster's disk-format rules).
		"REF-01", "REF-02", "REF-03", "REF-04", "REF-05",
		"REF-06", "REF-07", "REF-08", "REF-09", "REF-10",
		"REF-11", "REF-12", "REF-13", "REF-14", "REF-15",
		// REF-18: actorSubscribers reference integrity (each entry must be a
		// registered external actor, not a cell ID and not a wildcard).
		"REF-16", "REF-17", "REF-18",

		// SLICE-CONSISTENCY — slice level vs parent cell + contractUsages role lower bound (rules_misc_advisory.go)
		"SLICE-CONSISTENCY-01",
		"SLICE-CONSISTENCY-02",

		// CELL-LIFECYCLE — cell/slice maturity lifecycle membership + slice≤cell (rules_lifecycle.go)
		"CELL-LIFECYCLE-01",

		// TOPO — topology (rules_topo.go)
		"TOPO-01", "TOPO-02", "TOPO-03", "TOPO-04", "TOPO-05",
		"TOPO-06", "TOPO-07", "TOPO-08", "TOPO-09",

		// VERIFY — verification closure (rules_verify.go)
		"VERIFY-01", "VERIFY-02", "VERIFY-03",
		"VERIFY-04", "VERIFY-05", "VERIFY-06",
	}
}

// TestVERIFY06IsFirstPhaseStrict locks the ordering invariant that VERIFY-06
// is the first PhaseStrict rule in allRules. The rules_registry.go comment
// relies on this: "Must be first in PhaseStrict so fail-fast stops on
// VERIFY-06 before FMT rules." If VERIFY-06 were not first, a failing FMT
// rule could run before the ctx-subprocess guard fires.
func TestVERIFY06IsFirstPhaseStrict(t *testing.T) {
	t.Parallel()
	strict := rulesForPhases(PhaseStrict)
	if len(strict) == 0 {
		t.Fatal("rulesForPhases(PhaseStrict) is empty — VERIFY-06 must be first")
	}
	if strict[0].Code != codeVERIFY06 {
		t.Fatalf("rulesForPhases(PhaseStrict)[0].Code = %s; want %s (fail-fast ordering invariant)",
			strict[0].Code, codeVERIFY06)
	}
}

// symmetricDiff returns ordered "- a" / "+ b" lines for items present in only
// one side. Inputs must be sorted.
func symmetricDiff(want, got []string) []string {
	wantSet := map[string]struct{}{}
	for _, s := range want {
		wantSet[s] = struct{}{}
	}
	gotSet := map[string]struct{}{}
	for _, s := range got {
		gotSet[s] = struct{}{}
	}
	var diff []string
	for _, s := range want {
		if _, ok := gotSet[s]; !ok {
			diff = append(diff, "- "+s)
		}
	}
	for _, s := range got {
		if _, ok := wantSet[s]; !ok {
			diff = append(diff, "+ "+s)
		}
	}
	return diff
}
