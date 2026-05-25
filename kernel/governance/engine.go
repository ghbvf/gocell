package governance

import "context"

// engine.go is the single execution body for all governance rules (ADR
// 202605041430 §M3-RULE-ENGINE). It provides one data-driven registry —
// allRules in rules_registry.go — iterated by a single loop (replacing the
// former four separate dispatch lists, which were removed in §M3). Each rule
// is a Rule value carrying its classification metadata
// (Phase / Next) plus a compiler-checked Detect function value.
//
// Detect returns the finished []ValidationResult (built through the existing
// locator constructors newError/newWarning/newScopedError/newErrorAt, which stay
// the construction funnel — GOVERNANCE-RULE-ERROR-FIX-FIELD-01 unchanged). The
// engine's only post-processing is to stamp the M3 metadata field Next onto each
// finding from the owning Rule. Rule.Code duplicates the code each Detect emits;
// that pairing is drift-locked by the retargeted reachability archtest exactly as
// today, so the duplication is archtest-Medium (same tier as the pre-M3
// reachability test), not a new gap.
//
// Per-finding Metric (ADR §M3 P-C3): ValidationResult.Metric is a *float64 that
// the detect function itself sets on specific findings when an orderable distance
// is meaningful for that particular finding (e.g. FMT-23 sets days-remaining on
// the stale-contract warning it emits). Most findings carry nil — a boolean
// detect satisfies P-C3 with nil because the finding itself is the distance.
// ADV-05's dead-event count is a repository-aggregate (derivable as the number
// of ADV-05 findings), not a per-finding distance, so ADV-05 findings carry no
// Metric. stamp() passes through any Metric the detect already set; it does not
// touch or overwrite it.
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
// pull is simpler). Per-finding Metric has no go/analysis / golangci / staticcheck
// analog — those are bool pass/fail + severity only; it is a GoCell extension
// (conceptually SonarQube per-issue remediation effort, set by the detect that
// has the orderable distance, not by a rule-level function).

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

// Rule is one governance rule expressed as data: classification metadata plus a
// compiler-checked detect function value. The allRules slice (rules_registry.go)
// is the sole source of truth; run() iterates it. Detect returns the finished
// findings (built via the locator constructors); the engine stamps Next onto each
// finding. Per-finding Metric is set by the Detect function itself when an
// orderable distance is meaningful — stamp() passes it through unchanged.
// See package doc for the open-source analogs this mirrors.
type Rule struct {
	Code  RuleCode
	Phase Phase
	// Next is an OPTIONAL disposition override. When unset (""), the engine
	// derives each finding's Next from its Severity (error→block, warning→
	// advisory) — see resolveNext. Set it only for a non-default M5 disposition
	// (autofix / suggest / escalate); no rule declares one yet, so it is "" for
	// all current rules and the disposition is severity-derived per finding.
	//
	// ADR §M3 "harvest" slot: the NextAutofix/NextSuggest/NextEscalate values
	// carry the harvest intent (replacing the former separate Harvest field idea).
	// No separate Harvest field is introduced until M5 defines an actuator.
	Next   NextAction
	Detect func(v *Validator) []ValidationResult
}

// run is the single loop body for all entry points: ValidateStrict (base+dep+strict)
// and CheckHealth (health). It executes the given rules in order, accumulating
// findings and stamping each rule's Next/Metric metadata onto its findings. ctx
// cancellation is honored between rules (partial findings returned with the error
// so callers can distinguish a clean run from an interrupted one). When failFast
// is true, run returns as soon as any rule produces a SeverityError; warnings
// never trigger the bailout.
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

// stamp applies the rule's M3 Next metadata to each finding the rule produced.
// Next is resolved per finding from its Severity (see resolveNext) so a rule
// that emits both error and warning findings (e.g. JOURNEY-STATUS-LIFECYCLE-01,
// FMT-23) labels each correctly; Rule.Next overrides only for non-default
// dispositions. Per-finding Metric (ADR §M3 P-C3) is set by the Detect function
// itself on the specific findings it describes; stamp() passes it through
// unchanged and never overwrites it.
func (r Rule) stamp(_ *Validator, found []ValidationResult) []ValidationResult {
	for i := range found {
		found[i].Next = resolveNext(r.Next, found[i].Severity)
	}
	return found
}

// resolveNext returns the explicit Rule.Next override when set, otherwise the
// severity default: SeverityError → NextBlock, SeverityWarning → NextAdvisory.
// Deriving from severity (rather than a hand-set per-rule value) keeps a warning
// from ever being labeled "block" and removes the per-rule classification error
// surface; the explicit override is reserved for the M5 dispositions
// (autofix / suggest / escalate), which no rule declares yet.
func resolveNext(override NextAction, sev Severity) NextAction {
	if override != "" {
		return override
	}
	if sev == SeverityWarning {
		return NextAdvisory
	}
	return NextBlock
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
