// Package scoped_empty_fix_red is a testdata fixture for INV-3
// (GOVERNANCE-RULE-ERROR-FIX-FIELD-01) negative case — the fix-arg scan path
// for the scoped error constructor.
//
// Shape tested: a newScopedError call whose fix argument (the last positional
// arg) is an empty string literal. newScopedError builds cross-file findings
// (no single position); like newError/newErrorAt it must carry a non-empty
// remediation string.
//
// The fake newScopedError mirrors the production *locator method name + arg
// layout (code, typ, scope, field, msg, fix). Its body returns a zero
// ValidationResult via a var declaration (not a composite literal) so this
// fixture exercises ONLY the fix-arg path, not the raw-composite funnel ban.
//
// Expected: 1 violation from the fix-arg scan path.
package scoped_empty_fix_red

import gov "github.com/ghbvf/gocell/kernel/governance"

type fakeLocator struct{}

func (l *fakeLocator) newScopedError(code gov.RuleCode, typ gov.IssueType, scope, field, msg, fix string) gov.ValidationResult {
	var r gov.ValidationResult
	return r
}

// violateScopedEmptyFix calls newScopedError with an empty fix argument.
func (l *fakeLocator) violateScopedEmptyFix() gov.ValidationResult {
	return l.newScopedError("X-03", gov.IssueForbidden, "project", "field.path", "problem statement", "")
}
