//go:build archtest_fixture

// Package redfakesaferun is a RED fixture for SAGA-STEP-RUN-OUTSIDE-TX-01 A1:
// a same-named `safeRun` helper that is NOT runtime/saga/executor.safeRun calls
// a StepFunc directly. A1 binds the sanctioned range to the executor package by
// *types.Func identity (collectExecutorSafeRunRanges), so this impostor opens no
// sanctioned range and the StepFunc call inside it is still flagged — proving a
// name match alone is not a sanction (gh #1998 review F1).
// Expect one diagnostic from A1.
package redfakesaferun
