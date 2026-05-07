// Negative fixture for HANDLER-POLICY-REQUIRED-01.
// This file intentionally violates the rule — do not edit to remove the violation.
package cellviolator

// violatorSvc is a stub service type.
type violatorSvc struct{}

// nilpolicyfoo simulates the missing-policy pattern from the enqueue/status wiring.
var nilpolicyfoo = foocontract.NewHandler(violatorSvc{}, nil)
