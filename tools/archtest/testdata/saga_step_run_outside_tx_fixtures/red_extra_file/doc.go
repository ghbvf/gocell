//go:build archtest_fixture

// Package redextrafile is a RED fixture for SAGA-STEP-RUN-OUTSIDE-TX-01 A1:
// a file that is NOT coordinator.go calls a kernel/saga.StepFunc directly
// outside the safeRun body. Proves that A1's scope extension (all
// runtime/saga/ production files, not just coordinator.go) fires correctly.
// Expect one diagnostic from A1.
package redextrafile
