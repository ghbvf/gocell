// Package panicregister provides the only approved way to panic in GoCell
// production code. All production panic() call sites must wrap their payload
// with [Approved], passing a kebab-case reason literal that documents the
// ADR-approved rationale. This creates a single typed funnel that archtest
// PANIC-REGISTERED-01 can statically verify: any bare panic(), or any panic()
// whose argument is not a direct call to panicregister.Approved, is rejected.
// See .claude/rules/gocell/error-handling.md for the A/B/C classification
// policy and the list of approved sites.
package panicregister

// Approved tags a production panic call site as ADR-approved.
//
// reason MUST be a const string literal (kebab-case identifier like
// "lifecycle-recover-rethrow-to-recovery-middleware"). archtest
// PANIC-REGISTERED-01 rejects non-literal forms (fmt.Sprintf, concatenation,
// variables).
//
// value is the panic payload — typically:
//   - *errcode.Error from errcode.Assertion(...) for state-machine /
//     programmer-error sites
//   - the original value recovered from a defer for framework re-throw sites
//
// Approved returns value unchanged, so recover() upstack observes the same
// value it would observe from a bare panic(value). The function is purely
// a source-level marker; the archtest enforces that every production panic()
// call site has the form panic(panicregister.Approved(literal, _)).
//
// Hard funnel rationale: this is the unique panic-approved entry point in
// GoCell production code. Using any other shape — bare panic, different
// callee, non-literal reason — fails archtest PANIC-REGISTERED-01. See
// .claude/rules/gocell/ai-collab.md "Hard 范本" and charter §4 Wave 2.
func Approved(reason string, value any) any {
	return value
}
