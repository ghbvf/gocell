// Package pgrepoapproved provides the only approved way to call
// pgExecutor.ExecDirect from a PG repo/store method. Every legitimate
// ExecDirect callsite in a *_repo.go / *_store.go file MUST contain a
// pgrepoapproved.ApprovedExecDirect(reason) marker in the SAME approval
// scope (FuncDecl body OR enclosing FuncLit body — markers in nested
// closures do NOT approve outer-scope ExecDirect calls and vice versa).
// This creates a typed funnel that archtest PG-REPO-AMBIENT-TX-01 R3(b)
// statically verifies. See .claude/rules/gocell/ai-robust.md "Hard 范本"
// (typed marker funnel for unbounded ops); this is the sibling deployment
// of pkg/panicregister applied to ExecDirect.
package pgrepoapproved

// ApprovedExecDirect tags the enclosing repo/store function (or closure)
// as an ADR-approved caller of pgExecutor.ExecDirect (which bypasses the
// ambient transaction).
//
// reason MUST be a *ast.BasicLit + token.STRING (a Go const string LITERAL
// in source — not a const identifier, not "a" + "b" concatenation, not a
// variable). The literal value must match the kebab-case regex
// ^[a-z][a-z0-9-]+$ AND must not be a placeholder identifier (todo / fixme /
// tbd / xxx / placeholder / wip). archtest PG-REPO-AMBIENT-TX-01 R3(b)
// statically rejects every other form. The reason "is not cross-checked
// against any catalog at build time; it serves as source-level documentation"
// (sibling to pkg/panicregister.Approved); reviewers verify the corresponding
// ADR rationale exists, and the placeholder/empty/non-kebab forms above are
// rejected so the reason cannot be a meaningless audit-trail entry.
//
// ApprovedExecDirect is purely a source-level marker (no-op at runtime).
// The archtest enforces that every ExecDirect call from a non-pgExecutor
// receiver has a sibling ApprovedExecDirect(literal) call in the same
// approval scope.
//
// Hard funnel rationale: this is the unique allowlist mechanism for
// ExecDirect callsites in GoCell production code. The form-uniqueness
// (callee = pgrepoapproved.ApprovedExecDirect, arg[0] = kebab-case BasicLit,
// non-placeholder) plus the same-scope co-location check make any other
// shape — missing marker, different callee, non-literal reason, const ident /
// concat / empty / placeholder reason, marker in a different func/closure —
// fail archtest immediately. See .claude/rules/gocell/ai-robust.md
// "Hard 范本目录" §"typed marker funnel for unbounded ops".
func ApprovedExecDirect(reason string) {
	_ = reason
}
