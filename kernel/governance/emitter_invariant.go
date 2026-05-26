package governance

// Compile-time name pins for the governance archtest funnels.
//
// The tools/archtest governance invariants resolve these identifiers at test
// time via go/types scope.Lookup:
//
//   - GOVERNANCE-RULES-REGISTRATION-GUARD-01 looks up Validator (rule-method set).
//   - GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01 looks up RuleCode (every code
//     argument / Code field must resolve to a rulecodes.go RuleCode const).
//   - GOVERNANCE-RULE-ERROR-FIX-FIELD-01 looks up ValidationResult (raw
//     composite-literal ban outside locator.go; findings flow through the
//     newError / newWarning / newScopedError / newErrorAt locator funnel).
//
// A scope.Lookup miss makes those archtests fail at CI time, but the blank
// witnesses below hoist a rename of any of the three names up to a COMPILE
// error here — one档 tighter than the runtime t.Fatal. Keeping them is the
// cheapest way to make the archtest name dependencies rename-safe at build time.
//
// The pre-M3 BFS reachability test that previously lived here was retired with
// the rule-engine migration (ADR §M3-RULE-ENGINE): registration is now the
// allRules slice, golden-locked by TestAllRulesMatchGolden (rule_inventory_test.go),
// so there is no traversal that needs a typed emitter-owner gate.
var (
	_ *Validator
	_ RuleCode
	_ ValidationResult
)
