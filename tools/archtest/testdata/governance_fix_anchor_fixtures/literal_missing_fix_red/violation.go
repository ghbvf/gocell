// Package literal_missing_fix_red is a testdata fixture for INV-3
// (GOVERNANCE-RULE-ERROR-MESSAGE-FIX-SUFFIX-01) negative case.
//
// Shape tested: newResultAt callsite where the message arg is a string literal
// that lacks the required "; fix:" anchor.
// Expected: 1 violation from the CallExpr scan path.
package literal_missing_fix_red

import gov "github.com/ghbvf/gocell/kernel/governance"

type fakeValidator struct{}

// newResultAt mirrors the production method name and arg layout so the INV-3
// CallExpr scan matches by selector name. Args[1] is gov.SeverityError
// (index 1 = second positional arg, same as production newResultAt(code, sev, ...)).
// The last arg is a plain string literal that lacks the "; fix:" anchor.
func (fv *fakeValidator) newResultAt(code string, sev gov.Severity, msg string) gov.ValidationResult {
	return gov.ValidationResult{Severity: sev, Message: msg}
}

// violateLiteralMissingFix calls newResultAt with a string literal that has
// no "; fix:" anchor. INV-3 must flag this call site.
func (fv *fakeValidator) violateLiteralMissingFix() gov.ValidationResult {
	return fv.newResultAt("DOC-NAME-01", gov.SeverityError, "active document contains legacy literal")
}
