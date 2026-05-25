package governance

import "context"

// engine.go is the single execution body for all governance rules (ADR
// 202605041430 §M3-RULE-ENGINE). It replaces the four former dispatch lists
// (Validator.rules / strictRules, DependencyChecker.checks, CheckContractHealth)
// with one data-driven registry — allRules in rules_registry.go — iterated by a
// single loop. Each rule is a Rule value carrying its classification metadata
// (Phase / Next / optional Metric) plus a compiler-checked Detect function value.
//
// Detect returns the finished []ValidationResult (built through the existing
// locator constructors newError/newWarning/newScopedError/newErrorAt, which stay
// the construction funnel — GOVERNANCE-RULE-ERROR-FIX-FIELD-01 unchanged). The
// engine's only post-processing is to stamp the M3 metadata fields Next and
// Metric onto each finding from the owning Rule. Rule.Code duplicates the code
// each Detect emits; that pairing is drift-locked by the retargeted reachability
// archtest exactly as today, so the duplication is archtest-Medium (same tier as
// the pre-M3 reachability test), not a new gap.
//
// Carrier decision: rules are Go typed-struct values, not YAML. A YAML carrier
// would demote the detect binding from compiler-checked (Hard) to a string→
// registry lookup (archtest Medium) and introduce a YAML↔Go drift seam; its only
// unique benefit (non-Go external consumption) serves M5-HARVEST, which does not
// yet exist. See ADR §M3 amendment.
//
// ref: golang/tools go/analysis/analysis.go (Analyzer struct{Name,Doc,Run,...} +
// driver loop); golangci-lint pkg/lint/linter/config.go + lintersdb/manager.go
// (linter.Config registry iterated by Manager). Deliberately omitted: Requires/
// ResultType/FactTypes (rules share one *ProjectMeta, no inter-rule results),
// flag.FlagSet (rule params come from cell/slice.yaml), push-Report (pure-memory
// pull is simpler). Metric (distance function) has no go/analysis / golangci /
// staticcheck analog — those are bool pass/fail + severity only; it is a GoCell
// extension (conceptually SonarQube remediation-effort).

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

// NextAction is the structured remediation disposition for a finding (ADR §M3
// P-C2). It is a richer severity: it tells the (future) M5-HARVEST layer what to
// DO with a finding, not merely how loud it is.
type NextAction string

const (
	// NextBlock fails CI now (the default for SeverityError findings).
	NextBlock NextAction = "block"
	// NextAdvisory routes the finding to the inspection report, non-blocking
	// (the default for SeverityWarning findings).
	NextAdvisory NextAction = "advisory"
	// NextAutofix marks a mechanically fixable finding an M5 bot can open a PR for.
	NextAutofix NextAction = "autofix"
	// NextSuggest writes advice to the harvest/ directory (M5).
	NextSuggest NextAction = "suggest"
	// NextEscalate raises a human alert (M5).
	NextEscalate NextAction = "escalate"
)

// Metric is an optional distance function for a rule (ADR §M3 P-C3). Most rules
// have no meaningful distance — a reference either resolves or it does not — and
// leave it nil. Only rules with a genuine continuous distance (deprecation days
// remaining, coverage gap, dead-event count) declare one. It returns (value, ok);
// ok=false when the metric is not applicable to the current project state, in
// which case the finding carries no metric value.
type Metric func(v *Validator) (float64, bool)

// Rule is one governance rule expressed as data: classification metadata plus a
// compiler-checked detect function value. The allRules slice (rules_registry.go)
// is the sole source of truth; run() iterates it. Detect returns the finished
// findings (built via the locator constructors); the engine stamps Next/Metric.
// See package doc for the open-source analogs this mirrors.
type Rule struct {
	Code   RuleCode
	Phase  Phase
	Next   NextAction
	Metric Metric // optional; nil when the rule has no continuous distance
	Detect func(v *Validator) []ValidationResult
}

// run executes the given rules in order, accumulating findings and stamping each
// rule's Next/Metric metadata onto its findings. ctx cancellation is honored
// between rules (partial findings returned with the error so callers can
// distinguish a clean run from an interrupted one). When failFast is true, run
// returns as soon as any rule produces a SeverityError; warnings never trigger
// the bailout. This single loop body is what the former ValidateStrict /
// DependencyChecker.Check{,FailFast} / CheckContractHealth all collapse into.
func (v *Validator) run(ctx context.Context, rules []Rule, failFast bool) ([]ValidationResult, error) {
	v.runCtx = ctx
	var out []ValidationResult
	for i := range rules {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		found := rules[i].stamp(v, rules[i].Detect(v))
		out = append(out, found...)
		if failFast && HasErrors(found) {
			return out, nil
		}
	}
	return out, nil
}

// stamp applies the rule's M3 metadata (Next, Metric) to each finding the rule
// produced. Metric is evaluated once per run (project state is constant across a
// single run) and attached only when applicable.
func (r Rule) stamp(v *Validator, found []ValidationResult) []ValidationResult {
	var metric *float64
	if r.Metric != nil {
		if val, ok := r.Metric(v); ok {
			metric = &val
		}
	}
	for i := range found {
		found[i].Next = r.Next
		found[i].Metric = metric
	}
	return found
}

// rulesForPhases returns the subset of allRules whose Phase is in the given set,
// preserving allRules declaration order. Command entry points select the phases
// they run: validate → {Base, Dep} (+ Strict); check → {Health}.
func rulesForPhases(phases ...Phase) []Rule {
	return filterByPhase(allRules, phases...)
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
