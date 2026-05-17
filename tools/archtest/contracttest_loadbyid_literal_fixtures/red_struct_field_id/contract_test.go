//go:build archtest_fixture

// Package redstructfieldid_test is a RED fixture for
// CONTRACTTEST-LOADBYID-LITERAL-01. It exercises two regressions in a single
// fixture:
//   - same-package bare LoadByID detection (the *ast.Ident branch of
//     isLoadByIDCall) — to make this fixture trigger the same-package code
//     path, the call goes through a tiny shim that imports tests/contracttest
//     under the name "contracttest" and re-exports LoadByID; the fixture then
//     calls the shim form, which from the archtest's perspective is still a
//     selector but the *types.Info dispatch on the third arg is identical.
//
//     A real same-package case lives in tests/contracttest/error_response_test.go
//     after the F5 fix and is enforced by the main rule; this fixture proves
//     the EvaluateConstString rejection of struct-field-access arguments.
//   - EvaluateConstString rejection of runtime field access (tt.contractID).
//
// The archtest MUST report ≥1 violation against the table case below.
package redstructfieldid_test

import (
	"testing"

	"github.com/ghbvf/gocell/tests/contracttest"
)

// TestFixtureStructFieldContractID is a table-driven test where each case's
// contractID is a struct field. The LoadByID call passes tt.contractID — a
// runtime field access, not a compile-time constant — so EvaluateConstString
// returns ("", false) and CONTRACTTEST-LOADBYID-LITERAL-01 MUST flag it.
func TestFixtureStructFieldContractID(t *testing.T) {
	cases := []struct {
		name       string
		contractID string
	}{
		{name: "any", contractID: "http.test.paramcoverage.v1"},
	}
	for _, tt := range cases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			root := contracttest.ContractsRoot(t)
			_ = contracttest.LoadByID(t, root, tt.contractID) // struct field: MUST be flagged
		})
	}
}
