//go:build archtest_fixture

// Package redcomputedid_test is a RED fixture for CONTRACTTEST-LOADBYID-LITERAL-01.
// It calls contracttest.LoadByID with a non-literal (computed) contract ID.
// The archtest MUST report this as a violation.
package redcomputedid_test

import (
	"testing"

	"github.com/ghbvf/gocell/tests/contracttest"
)

// computeContractID simulates a dynamic contract ID to create a non-literal
// argument to contracttest.LoadByID.
func computeContractID() string {
	return "http.test.paramcoverage.v1"
}

// TestFixtureComputedContractID deliberately passes a non-literal (computed)
// string to LoadByID. CONTRACTTEST-LOADBYID-LITERAL-01 must flag this.
func TestFixtureComputedContractID(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	computedID := computeContractID()
	_ = contracttest.LoadByID(t, root, computedID) // non-literal: MUST be flagged
}
