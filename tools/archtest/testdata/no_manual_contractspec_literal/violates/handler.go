// Fixture for NO-MANUAL-CONTRACTSPEC-LITERAL-01 negative test.
// This file intentionally contains a manual contractspec.ContractSpec{} literal
// in a non-generated, non-test file to trigger the archtest violation.
package violates

import "github.com/ghbvf/gocell/kernel/contractspec"

// badSpec is a manually declared ContractSpec literal — forbidden outside generated code.
var badSpec = contractspec.ContractSpec{
	ID:        "http.bad.example.v1",
	Kind:      "http",
	Transport: "http",
	Method:    "GET",
	Path:      "/api/v1/bad",
}
