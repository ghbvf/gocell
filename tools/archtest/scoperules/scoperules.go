// Package scoperules is the single, zero-dependency source of truth for which
// archtest rule IDs constitute the FRAMEWORK scope — the curated, module-path-
// agnostic / portable rule set that applies to any repository building Cells on
// the GoCell framework (#1878 / #2331).
//
// # Why a separate leaf package
//
// Two consumers need this set, with very different weight budgets:
//
//   - tools/archtest.StandardCellRules() pairs each ID with its Check* Run func
//     (the heavy go/analysis + packages.Load rule bodies). It DERIVES its
//     membership by iterating [FrameworkRuleIDs], so the set is defined once.
//   - cmd/gocell/internal/archtestrunner (the `gocell verify archtest
//     --scope=framework` subprocess runner) only needs the ID STRINGS to narrow
//     the discovered test set. Importing tools/archtest there would link testify
//     / net/http/httptest / go/importer into the governance CLI — so the runner
//     imports THIS leaf instead, which has no imports at all.
//
// Because StandardCellRules() iterates FrameworkRuleIDs and the runner reads the
// identical slice, the two cannot disagree on framework-scope MEMBERSHIP by
// construction (AI-robust Hard): adding or removing a framework rule is a single
// edit here. TestStandardCellRulesComposition (tools/archtest) additionally
// proves every ID is paired with a non-nil Run (Run-binding completeness).
//
// Scope membership lives ONLY here. Each rule still keeps its own unexported
// self-identity const (rulePanicRegistered01, …) used as its diagnostic Report
// label — those are per-rule labels, not a second membership list, and are left
// in place so a rule body stays decoupled from this scope leaf. The binding that
// must hold — every ID below has a matching `// INVARIANT: <id>` anchor in a
// tools/archtest/*_test.go header — is enforced fail-loud by the runner's
// zero-func guard (an unanchored framework ID is a hard error, never a silent
// skip).
//
// Guard timing: that check is a RUNTIME guard, fired when `gocell verify archtest
// --scope=framework` resolves the set — NOT a compile-time or `go build` check.
// So after adding an ID here, add its `// INVARIANT: <id>` anchor to the owning
// *_test.go and verify locally with `gocell verify archtest --scope=framework
// --list-tests`; otherwise the mismatch surfaces only when that command runs.
package scoperules

import "slices"

// Framework-scope rule IDs. Each maps to a portable Check* in
// tools/archtest.StandardCellRules() and to an `// INVARIANT: <id>` anchor in a
// tools/archtest/*_test.go file (which the runner uses to resolve the ID to its
// owning test functions).
const (
	PanicRegistered01               = "PANIC-REGISTERED-01"
	ErrcodeKindLiteral01            = "ERRCODE-KIND-LITERAL-01"
	MessageConstLiteral01           = "MESSAGE-CONST-LITERAL-01"
	ExportedErrorNew01              = "EXPORTED-ERROR-NEW-01"
	ScaffoldDerivedForceOverwrite01 = "SCAFFOLD-DERIVED-FORCEOVERWRITE-01"
	OutboxReconstructionCaller01    = "OUTBOX-RECONSTRUCTION-CALLER-01"
	ProjectionApplyHookFunnel01     = "PROJECTION-APPLY-HOOK-FUNNEL-01"
	OutboxHandleResultFactoryPref01 = "OUTBOX-HANDLERESULT-FACTORY-PREFERRED-01"
	SagaStepCompensatePure01        = "SAGA-STEP-COMPENSATE-PURE-01"
	ProdMainWiringNoopReject01      = "PROD-MAIN-WIRING-NOOP-REJECT-01"
)

// frameworkRuleIDs is the ordered framework-scope membership list — the single
// source consumed by StandardCellRules() (derive) and the archtest scope runner.
// Order mirrors StandardCellRules()'s historical registration order. It is
// unexported and only reachable through [FrameworkRuleIDs], which returns a copy,
// so no consumer can mutate (append / index-assign / reassign) the membership —
// the single-source guarantee is structural, not by convention.
var frameworkRuleIDs = []string{
	PanicRegistered01,
	ErrcodeKindLiteral01,
	MessageConstLiteral01,
	ExportedErrorNew01,
	ScaffoldDerivedForceOverwrite01,
	OutboxReconstructionCaller01,
	ProjectionApplyHookFunnel01,
	OutboxHandleResultFactoryPref01,
	SagaStepCompensatePure01,
	ProdMainWiringNoopReject01,
}

// FrameworkRuleIDs returns a fresh copy of the framework-scope membership list.
// Returning a copy (not the backing slice) keeps [frameworkRuleIDs] the sole
// mutable holder, so the set cannot drift via a caller's append or element write.
func FrameworkRuleIDs() []string { return slices.Clone(frameworkRuleIDs) }
