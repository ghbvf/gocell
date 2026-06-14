// Package empty_fix_red is a testdata fixture for INV-3
// (GOVERNANCE-RULE-ERROR-FIX-FIELD-01) negative case — the fix-arg scan path.
//
// Shape tested: a newError call whose fix argument (the last positional arg)
// is an empty string literal. Every error constructor must carry a non-empty
// remediation string; an empty fix is the forbidden shape.
//
// The fake newError mirrors the production *locator method name + arg layout
// (code, typ, file, field, msg, fix) so the scan matches it by name. Its body
// returns a zero ValidationResult via a var declaration (not a composite
// literal) so this fixture exercises ONLY the fix-arg path, not the
// raw-composite funnel ban.
//
// Expected: 1 violation from the fix-arg scan path.
package empty_fix_red

import gov "github.com/ghbvf/gocell/framework/kernel/governance"

type fakeLocator struct{}

func (l *fakeLocator) newError(code gov.RuleCode, typ gov.IssueType, file, field, msg, fix string) gov.ValidationResult {
	var r gov.ValidationResult
	return r
}

// violateEmptyFix calls newError with an empty fix argument. INV-3 must flag it.
func (l *fakeLocator) violateEmptyFix() gov.ValidationResult {
	return l.newError("X-01", gov.IssueInvalid, "fixture.yaml", "field.path", "problem statement", "")
}
