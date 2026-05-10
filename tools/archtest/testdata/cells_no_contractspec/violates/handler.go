// Fixture for CELLS-NO-WRAPPER-CONTRACTSPEC-IMPORT-01 negative test.
// This file intentionally imports kernel/wrapper and references ContractSpec
// to trigger the archtest scanner violation.
package violates

import "github.com/ghbvf/gocell/kernel/contractspec"

// RegisterRoutes is a stub that uses contractspec.ContractSpec directly —
// the pattern forbidden in non-generated cells/ files post W3.
func Register() contractspec.ContractSpec {
	return contractspec.ContractSpec{
		ID:   "http.bad.cell.v1",
		Kind: "http",
	}
}
