//go:build archtest_fixture

// Package redsaferunninruntintx is a RED fixture for SAGA-STEP-RUN-OUTSIDE-TX-01
// A2: a file calls safeRun() directly inside a TxRunner.RunInTx closure body.
// Proves that A2's structural check fires on the offending pattern (user step
// code would otherwise run with a DB transaction held open).
// Expect one diagnostic from A2.
package redsaferunninruntintx
