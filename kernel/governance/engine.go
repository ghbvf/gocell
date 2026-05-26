package governance

import "context"

// engine.go is the single execution body for all governance rules (ADR
// 202605041430 §M3-RULE-ENGINE). It provides one data-driven registry —
// allRules in rules_registry.go — iterated by a single loop (replacing the
// former four separate dispatch lists, which were removed in §M3). Each rule
// is a Rule value carrying its classification metadata (Phase) plus a
// compiler-checked Detect function value.
//
// Detect returns the finished []ValidationResult (built through the existing
// locator constructors newError/newWarning/newScopedError/newErrorAt, which stay
// the construction funnel — GOVERNANCE-RULE-ERROR-FIX-FIELD-01 unchanged).
// Rule.Code duplicates the code each Detect emits. Three archtests cover the
// pairing: GOVERNANCE-RULE-CODE-DETECT-BINDING-01 walks each Detect body and
// asserts the emitted code(s) include Rule.Code (catching a swapped
// {Code, Detect} pair); GOVERNANCE-RULES-REGISTRATION-GUARD-01 locks that every
// Detect method is registered in allRules; and TestAllRulesMatchGolden
// golden-locks the rule-code set + uniqueness. The per-rule unit tests
// additionally assert each method emits its own code.
//
// next-action and per-finding metric were removed from M3 and from the current
// M0-M4 roadmap as speculative repository-convergence scaffolding (no consumer
// exists; M5-HARVEST is canceled). M3 ships the engine refactor only: single
// allRules registry + engine + Phase + ADV-05 fix. Future repository
// convergence must start with a new ADR and a real consumer.
// See ADR §M3 amendment (2026-05-26 #687 round-3).
//
// Carrier decision: rules are Go typed-struct values, not YAML. A YAML carrier
// would demote the detect binding from compiler-checked (Hard) to a string→
// registry lookup (archtest Medium) and introduce a YAML↔Go drift seam; its only
// unique benefit (non-Go external consumption) has no consumer in the current
// M0-M4 roadmap. See ADR §M3 amendment.
//
// ref: golang/tools go/analysis/analysis.go (Analyzer struct{Name,Doc,Run,...} +
// driver loop); golangci-lint pkg/lint/linter/config.go + lintersdb/manager.go
// (linter.Config registry iterated by Manager). Deliberately omitted: Requires/
// ResultType/FactTypes (rules share one *ProjectMeta, no inter-rule results),
// flag.FlagSet (rule params come from cell/slice.yaml), push-Report (pure-memory
// pull is simpler).

// Phase selects which command surface a rule participates in. It replaces the
// former four separate dispatch lists with one declarative attribute, so the
// strict/base split is a rule property rather than two rule sets (ADR §6.3 root
// cause: "FMT strict-only 切两套规则集 ... 被错做规则集合分裂").
type Phase int

const (
	// PhaseBase rules run on every `gocell validate` invocation.
	PhaseBase Phase = iota
	// PhaseStrict rules run only with `gocell validate --strict` (VERIFY-06,
	// FMT-16/17/19, DOC-NAME-01).
	PhaseStrict
	// PhaseDep rules are the cell dependency-graph checks (DEP-01..03). They
	// run alongside base rules on `gocell validate` but are grouped separately
	// because their detect functions consult the cell/contract registries.
	PhaseDep
	// PhaseHealth rules are the contract-health invariants (CH-01..06) run by
	// `gocell check`.
	PhaseHealth
)

// Rule is one governance rule expressed as data: classification metadata plus a
// compiler-checked detect function value. The allRules slice (rules_registry.go)
// is the sole source of truth; run() iterates it. Detect returns the finished
// findings (built via the locator constructors).
// See package doc for the open-source analogs this mirrors.
type Rule struct {
	Code   RuleCode
	Phase  Phase
	Detect func(v *Validator) []ValidationResult
}

// run is the single loop body for all entry points: ValidateStrict (base+dep+strict)
// and CheckHealth (health). It executes the given rules in order, accumulating
// findings. ctx cancellation is honored between rules (partial findings returned
// with the error so callers can distinguish a clean run from an interrupted one).
// When failFast is true, run returns as soon as any rule produces a SeverityError;
// warnings never trigger the bailout.
func (v *Validator) run(ctx context.Context, rules []Rule, failFast bool) ([]ValidationResult, error) {
	v.runCtx = ctx
	var out []ValidationResult
	for i := range rules {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		found := rules[i].Detect(v)
		out = append(out, found...)
		// Re-check after Detect: a long rule (e.g. CH-04 scanning many handler
		// files) may have observed cancellation mid-body and returned partial
		// findings. Without this, cancellation during the *final* rule would be
		// lost (no next iteration to catch it) and the run would look clean.
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if failFast && HasErrors(found) {
			return out, nil
		}
	}
	return out, nil
}

// rulesForPhases returns the subset of allRules whose Phase is in the given set,
// preserving allRules declaration order. Command entry points select the phases
// they run: validate → {Base, Dep} (+ Strict); check → {Health}.
func rulesForPhases(phases ...Phase) []Rule {
	return filterByPhase(allRules, phases...)
}

// StrictRuleCodes returns the codes of the PhaseStrict rules in declaration
// order — the rules that run only under `gocell validate --strict`. The
// validate CLI derives its --strict flag help from this, so the help text is a
// pure projection of the registry and can never drift from the rules that run.
func StrictRuleCodes() []RuleCode {
	rules := rulesForPhases(PhaseStrict)
	codes := make([]RuleCode, len(rules))
	for i := range rules {
		codes[i] = rules[i].Code
	}
	return codes
}

// filterByPhase is the pure phase selector rulesForPhases delegates to, kept
// separate so it can be unit-tested without populating the package-level
// allRules registry. Declaration order is preserved.
func filterByPhase(rules []Rule, phases ...Phase) []Rule {
	want := make(map[Phase]struct{}, len(phases))
	for _, p := range phases {
		want[p] = struct{}{}
	}
	var out []Rule
	for i := range rules {
		if _, ok := want[rules[i].Phase]; ok {
			out = append(out, rules[i])
		}
	}
	return out
}
