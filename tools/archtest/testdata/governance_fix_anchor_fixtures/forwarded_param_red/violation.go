// Package forwarded_param_red is a testdata fixture for INV-3
// (GOVERNANCE-RULE-ERROR-MESSAGE-FIX-SUFFIX-01) negative case.
//
// Shape tested: newResultAt callsite where the message arg is a non-const
// function parameter ident. After removing the helper-forwarding skip from
// INV-3, this pattern must be caught — wrappers that forward a message
// parameter are forbidden under the Hard funnel form uniqueness rule.
// Expected: 1 violation from the CallExpr scan path.
package forwarded_param_red

import gov "github.com/ghbvf/gocell/kernel/governance"

type fakeValidator struct{}

// newResultAt mirrors the production method name and arg layout so the INV-3
// CallExpr scan matches by selector name. Args[1] is gov.SeverityError
// (matched via isSeverityErrorArg — index 1 = second positional arg,
// same as production newResultAt(code, sev, ...)). The last arg (msg) is
// a non-const ident — the forbidden forwarding pattern INV-3 must now reject.
func (fv *fakeValidator) newResultAt(code string, sev gov.Severity, msg string) gov.ValidationResult {
	return gov.ValidationResult{Severity: sev, Message: msg}
}

// violateForwardedParam calls newResultAt with a function parameter as the
// message — the forbidden forwarding pattern. INV-3 must flag this site.
func (fv *fakeValidator) violateForwardedParam(msg string) gov.ValidationResult {
	return fv.newResultAt("DOC-NAME-01", gov.SeverityError, msg)
}
