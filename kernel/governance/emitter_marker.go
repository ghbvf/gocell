package governance

// validationResultEmitter is the unexported sealed marker for types whose
// methods construct ValidationResult with a rule-ID argument at parameter
// position 0. Only *locator implements it; the rule-reachability BFS
// (rule_inventory_test.go) uses types.Implements(*recvType, emitterIface)
// to detect emitter call sites without relying on the weaker
// "same-package owner" heuristic that the pre-R2-P1 predicate used.
//
// AI-rebust: Hard. The marker method isValidationResultEmitter() is
// unexported — types outside kernel/governance cannot implement it, so
// "non-emitter receiver matches the predicate" is not expressible. Adding
// a new emitter on a new receiver requires explicitly implementing the
// marker inside this package; the marker method declaration is visible
// in PR diff and forces reviewer awareness.
//
// ref: kernel/outbox/cell_marker.go      CellPublisher sealed marker
// ref: kernel/persistence/cell_marker.go CellTxManager sealed marker
type validationResultEmitter interface {
	isValidationResultEmitter()
}

// *locator implements validationResultEmitter. The three emitter methods
// — newResult / newResultAt / newScopedResult, declared in locator.go —
// are the canonical ValidationResult constructors. Pinning *locator as
// the sole marker-implementing receiver funnels BFS emitter detection
// through the type system rather than through package-locality heuristics.
func (*locator) isValidationResultEmitter() {}

// Compile-time witnesses for rule_inventory_test.go's loader, which
// resolves these three identifiers from package scope at runtime. Rename
// of any one (validationResultEmitter / RuleCode / ValidationResult) fails
// build here — transforming what would otherwise be a runtime t.Fatalf
// inside the loader into a compile error. This is the strongest Hard
// form Go's go/types model permits: the layer below cannot enforce
// "resolve type by compile-time reference" because *types.Type values
// are per-process pointers with no compile-time embedding mechanism;
// hoisting the check one layer up (compile-time witness) closes the gap.
//
// AI-rebust: Hard. OSS comparators — staticcheck (analysisutil.ObjectOf
// silently skips on miss), govet passes/printf (skips pass on missing
// target), gopls (same pattern) — all stop at Soft for the rename
// threat. These witnesses upgrade rename to compile failure.
var (
	_ validationResultEmitter = (*locator)(nil)
	_ RuleCode                // zero-value witness; no composite literal so
	_ ValidationResult        // TestGovernanceRuleCodeConstSingleSource does not flag.
)
