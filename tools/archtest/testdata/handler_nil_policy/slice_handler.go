// Negative fixture for HANDLER-POLICY-REQUIRED-01.
// This file intentionally violates the rule outside cell.go to pin scan breadth.
package cellviolator

// violatorSvc is a stub service type.
type sliceViolatorSvc struct{}

// nilpolicyslice simulates the missing-policy pattern from slice handler wiring.
var nilpolicyslice = foocontract.NewHandler(sliceViolatorSvc{}, nil)
