package governance

// rules_registry.go holds allRules — the single source of truth for every
// governance rule (ADR 202605041430 §M3-RULE-ENGINE). engine.go iterates it;
// command entry points (ValidateStrict, the check command) select phases.
//
// Each entry is a Rule value: static classification (Code / Phase / Severity /
// IssueType / Next / optional Metric) plus a compiler-checked Detect function
// value. Detect functions live alongside their former rule cluster
// (rules_ref.go, rules_topo.go, ...) and return []Hit evidence only; the engine
// stamps classification in Rule.build.
//
// Invariant: the set of Rule.Code values here equals goldenRuleIDs() and every
// Code is unique. The engine running every entry is structural (no separate
// registration list can drift); the golden set + uniqueness are archtest-locked
// (GOVERNANCE-RULES-REGISTRATION-GUARD-01, retargeted to this slice).
//
// Populated incrementally during the M3 migration; the old rules() /
// strictRules() / DependencyChecker.checks() / CheckContractHealth dispatch
// remains the live path until every rule is migrated here (Batch 3 flip).
var allRules []Rule
