// Package pgrepoapproved provides the only approved way to call
// pgExecutor.ExecDirect from a PG repo/store method. Every legitimate
// ExecDirect callsite in a *_repo.go / *_store.go file MUST contain a
// pgrepoapproved.ApprovedExecDirect(reason) marker in the same FuncDecl body
// before invoking ExecDirect. This creates a typed funnel that archtest
// PG-REPO-AMBIENT-TX-01 R3(b) statically verifies: any ExecDirect call
// without a same-body marker, or with a non-literal reason, is rejected.
// See .claude/rules/gocell/ai-robust.md "Hard 范本" (typed marker funnel
// for unbounded ops) for the pattern; this is the sibling deployment of
// pkg/panicregister applied to ExecDirect.
package pgrepoapproved

// ApprovedExecDirect tags the enclosing repo/store function as an
// ADR-approved caller of pgExecutor.ExecDirect (which bypasses the ambient
// transaction).
//
// reason MUST be a const string literal (kebab-case identifier like
// "revoke-session-cascade") that documents the ADR-explicit rationale for
// bypassing the ambient transaction. archtest PG-REPO-AMBIENT-TX-01 R3(b)
// rejects non-literal forms (fmt.Sprintf, concatenation, variables).
//
// It is not cross-checked against any catalog at build time; it serves as
// source-level documentation. Reviewers verify the corresponding ADR
// rationale exists.
//
// Note: an empty string "" satisfies the const-literal check but violates
// review policy; reviewers must reject meaningless reasons.
//
// ApprovedExecDirect is purely a source-level marker (no-op at runtime).
// The archtest enforces that every ExecDirect call from a non-pgExecutor
// receiver has a sibling ApprovedExecDirect(literal) call in the same
// FuncDecl body.
//
// Hard funnel rationale: this is the unique allowlist mechanism for
// ExecDirect callsites in GoCell production code. The form-uniqueness
// (callee = pgrepoapproved.ApprovedExecDirect, arg[0] = const literal) plus
// the same-body co-location check make any other shape — missing marker,
// different callee, non-literal reason, marker in a different function —
// fail archtest immediately. See .claude/rules/gocell/ai-robust.md
// "Hard 范本目录" §"typed marker funnel for unbounded ops".
func ApprovedExecDirect(reason string) {
	_ = reason
}
