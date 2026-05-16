package governance

// BFS rule-reachability owner invariant (R2-P1, 2026-05-16):
//
// *locator is the SOLE ValidationResult-emitter holder in this package.
// The three emitter methods — newResult, newResultAt, newScopedResult —
// are declared on *locator; no other receiver may carry an
// (RuleCode, ...) ValidationResult method shape and have its call sites
// captured by the BFS reachability test.
//
// Enforcement is type-identity on the receiver at the predicate level
// (rule_inventory_test.go's signatureMatchesValidationResultEmitter):
// only methods whose receiver is types.Identical to *locator's named
// type pass the owner gate. This is structurally immune to Go method
// promotion via embedding — Validator / DependencyChecker embed locator
// (validate.go / depcheck.go) and inherit *locator's method set into
// their own method sets, but their named types remain distinct from
// locator's named type, so types.Identical correctly rejects them.
//
// Why not a sealed marker interface? An earlier R2-P1 draft (PR #521
// commit baba2a6f2) used `validationResultEmitter interface {
// isValidationResultEmitter() }` with types.Implements as the owner
// gate. Reviewer F-1 caught the structural flaw: method promotion via
// embedding inherits the marker method into *Validator / *DependencyChecker
// method sets, so types.Implements returned true on the outer types
// too — defeating "only *locator". Sealed markers are the right pattern
// for *cross-package* unimplementability (kernel/outbox/cell_marker.go's
// CellPublisher, kernel/persistence/cell_marker.go's CellTxManager) but
// the wrong pattern for *same-package owner allowlist*: marker promotion
// is structural in Go and cannot be selectively blocked at the method-set
// layer. types.Identical on the named type is the correct primitive.
//
// AI-rebust: Hard. Threat model coverage:
//
//  1. AI adds emitter shape on a new non-locator receiver inside the
//     package → recvNamed != locator → rejected.
//  2. AI adds a new emitter method on *locator → recvNamed == locator
//     → accepted (legitimate extension).
//  3. AI renames locator → build fails on the witnesses below, before
//     the test even loads.
//  4. AI adds emitter shape on *Validator / *DependencyChecker (the
//     embedding-promotion attack surface) → recvNamed == Validator /
//     DependencyChecker, not locator → rejected. This is the gap that
//     defeated the prior marker iface.
//
// Compile-time witnesses below pin the three identifiers the predicate
// resolves at runtime via scope.Lookup; rename of any one fails build
// here, transforming a would-be runtime t.Fatalf into a compile error.
// This is the strongest Hard form Go's go/types model permits: per-process
// *types.Type pointers have no compile-time embedding mechanism, so
// lookup-by-name at the loader boundary is structurally unavoidable.
// OSS comparators (staticcheck.analysisutil.ObjectOf, govet passes,
// gopls) all silently skip on miss (Soft); hoisting the rename check up
// to compile-time via these witnesses closes the gap two档 tighter.
var (
	_ *locator         // witness for the canonical emitter holder type
	_ RuleCode         // zero-value blank var; INV-2 only scans newResult call sites
	_ ValidationResult // and ValidationResult composite literals — these are neither.
)
