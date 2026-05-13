// Package filename_bypass_red is a testdata fixture for the INV-2
// negative fixture (GOVERNANCE-RULE-CODE-CONST-SINGLE-SOURCE-01).
//
// rulecodes.go declares a legitimate RuleCode const (codeGood).
// fake_rules.go declares codeBad — which is outside rulecodes.go and
// must be excluded by the filename guard added to collectRuleCodeConsts.
package filename_bypass_red

// RuleCode is a named string type that mirrors kernel/governance.RuleCode.
type RuleCode string

// codeGood is declared here in rulecodes.go — the single-source file.
// The filename guard must INCLUDE this const.
const codeGood RuleCode = "FMT-99"
