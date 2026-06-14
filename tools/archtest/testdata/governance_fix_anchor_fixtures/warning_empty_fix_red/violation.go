// Package warning_empty_fix_red is a testdata fixture for INV-3
// (GOVERNANCE-RULE-ERROR-FIX-FIELD-01) negative case — the fix-arg scan path
// for the warning constructor.
//
// Shape tested: a newWarning call whose fix argument (the last positional arg)
// is an empty string literal. The typed-Fix contract is severity-agnostic —
// warnings carry structured remediation too — so newWarning is scanned for a
// non-empty fix exactly like the error constructors.
//
// The fake newWarning mirrors the production *locator method name + arg layout
// (code, typ, file, field, msg, fix). Its body returns a zero ValidationResult
// via a var declaration (not a composite literal) so this fixture exercises
// ONLY the fix-arg path, not the raw-composite funnel ban.
//
// Expected: 1 violation from the fix-arg scan path.
package warning_empty_fix_red

import gov "github.com/ghbvf/gocell/framework/kernel/governance"

type fakeLocator struct{}

func (l *fakeLocator) newWarning(code gov.RuleCode, typ gov.IssueType, file, field, msg, fix string) gov.ValidationResult {
	var r gov.ValidationResult
	return r
}

// violateWarningEmptyFix calls newWarning with an empty fix argument.
func (l *fakeLocator) violateWarningEmptyFix() gov.ValidationResult {
	return l.newWarning("X-04", gov.IssueRefNotFound, "fixture.yaml", "field.path", "advisory observation", "")
}
