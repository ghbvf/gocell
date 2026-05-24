// Package unresolvable_fix_red is a testdata fixture for INV-3
// (GOVERNANCE-RULE-ERROR-FIX-FIELD-01) negative case — the fix-arg scan path.
//
// Shape tested: a newErrorAt call (the package-level emitter, an Ident callee)
// whose fix argument is forwarded from a function parameter and therefore does
// not resolve to a non-empty const string. Wrappers that forward the fix
// argument are forbidden — every error call site must supply its own
// resolvable remediation string.
//
// The fake newErrorAt mirrors the production package-level function name + arg
// layout (code, typ, file, pos, field, msg, fix). Its body returns a zero
// ValidationResult via a var declaration (not a composite literal) so this
// fixture exercises ONLY the fix-arg path, not the raw-composite funnel ban.
//
// Expected: 1 violation from the fix-arg scan path.
package unresolvable_fix_red

import (
	gov "github.com/ghbvf/gocell/kernel/governance"
	"github.com/ghbvf/gocell/kernel/metadata"
)

func newErrorAt(code gov.RuleCode, typ gov.IssueType, file string, pos metadata.Position, field, msg, fix string) gov.ValidationResult {
	var r gov.ValidationResult
	return r
}

// violateUnresolvableFix forwards its fix parameter into newErrorAt — the
// forbidden shape (the fix is not a resolvable const string). INV-3 must flag it.
func violateUnresolvableFix(fix string) gov.ValidationResult {
	return newErrorAt("X-02", gov.IssueInvalid, "fixture.yaml", metadata.Position{}, "field.path", "problem statement", fix)
}
