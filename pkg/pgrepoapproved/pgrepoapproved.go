// Package pgrepoapproved provides the proof-of-authorization token that
// pgexec.ExecDirect requires as its first argument. ExecDirect bypasses the
// ambient transaction (independent-commit cascade/compensation paths), so
// every callsite must be explicitly ADR-approved.
//
// The approval is bound to the call argument, not a sibling marker statement:
//
//	pgexec.ExecDirect(pgrepoapproved.Approve("revoke-session-cascade"),
//	    s.db, cascadeCtx, revokeSessionSQL, revokedAt, sessionID)
//
// This makes "ExecDirect without approval" a compile error (the first
// parameter is typed Approval) and "approval without a call" impossible
// (there is no standalone marker statement to misplace, share, or strand in
// the wrong scope). It supersedes the PR #1194 sibling-marker form
// (the former ApprovedExecDirect), removing the entire same-approval-scope
// co-location machinery. Sibling deployment of pkg/panicregister.Approved,
// which likewise binds its token to the panic call expression.
//
// archtest PG-REPO-AMBIENT-TX-01 R3 statically enforces that arg[0] of every
// pgexec.ExecDirect callsite is an inline CallExpr to pgrepoapproved.Approve
// whose own arg[0] is a kebab-case string LITERAL (form-uniqueness, Hard). A
// pre-constructed or reused Approval value (a := Approve("x"); ExecDirect(a,
// ...)) is rejected because arg[0] is then an *ast.Ident, not the sanctioned
// CallExpr form. See .claude/rules/gocell/ai-robust.md "Hard 范本目录"
// §"typed marker funnel for unbounded ops".
package pgrepoapproved

// Approval is the sealed proof token for a single pgexec.ExecDirect callsite.
// It is a sealed interface: only the unexported approval type (minted by
// Approve) implements it, so callers cannot forge one via a composite literal
// or a parallel implementation. The value carries no runtime data — ExecDirect
// ignores it; it exists solely to make the bypass authorization an inseparable
// part of the call expression that archtest can statically verify.
type Approval interface{ approvedExecDirect() }

type approval struct{}

func (approval) approvedExecDirect() {}

// Approve mints an Approval for the pgexec.ExecDirect callsite it is passed to.
//
// reason MUST be a kebab-case string LITERAL in source (^[a-z][a-z0-9-]+$ —
// minimum length 2, lowercase-letter start, e.g. "revoke-session-cascade"; not
// a placeholder such as todo/fixme/tbd/xxx/placeholder/wip) — archtest
// PG-REPO-AMBIENT-TX-01 R3 rejects const identifiers, "a"+"b" concatenation,
// variables, and empty/placeholder reasons. The reason is not cross-checked
// against any catalog at build time; it serves as source-level documentation
// (sibling to pkg/panicregister.Approved). Reviewers verify the corresponding
// ADR rationale exists.
func Approve(reason string) Approval {
	_ = reason
	return approval{}
}
