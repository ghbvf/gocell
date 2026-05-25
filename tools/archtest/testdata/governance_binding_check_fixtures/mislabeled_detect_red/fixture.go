// Package mislabeled_detect_red is a testdata fixture for
// GOVERNANCE-RULE-CODE-DETECT-BINDING-01 negative case.
//
// Shape tested: an allRules entry pairs Code: codeB with
// Detect: (*Validator).methodA, but methodA only emits codeA — never codeB.
// The binding check must report the mismatch.
//
// Expected: the binding check surfaces the entry as mislabeled
// (entry Code "B-01" not in emitted set {"A-01"}).
package mislabeled_detect_red

// RuleCode is the typed rule-code string (mirrors kernel/governance.RuleCode).
type RuleCode string

// ValidationResult mirrors kernel/governance.ValidationResult shape so that
// ruleShapeSignature accepts the return type by name.
type ValidationResult struct {
	Code RuleCode
}

// Validator is the registration anchor.
type Validator struct{}

// Rule is a minimal Rule struct with Code + Detect.
type Rule struct {
	Code   RuleCode
	Detect func(v *Validator) []ValidationResult
}

const (
	codeA RuleCode = "A-01"
	codeB RuleCode = "B-01"
)

// newError is a minimal constructor that accepts a RuleCode as first arg.
// It mimics the governance newError/newWarning/newScopedError/newErrorAt funnel.
func (v *Validator) newError(code RuleCode, msg string) ValidationResult {
	return ValidationResult{Code: code}
}

// methodA emits codeA — it is correctly paired with codeA.
func (v *Validator) methodA() []ValidationResult {
	return []ValidationResult{v.newError(codeA, "found something")}
}

// methodB emits codeB — it is correctly paired with codeB.
func (v *Validator) methodB() []ValidationResult {
	return []ValidationResult{v.newError(codeB, "found something else")}
}

// allRules deliberately mislabels: Code is codeB but Detect is methodA.
// methodA only emits codeA, so the binding check must fire.
var allRules = []Rule{
	{Code: codeB, Detect: (*Validator).methodA},
}
