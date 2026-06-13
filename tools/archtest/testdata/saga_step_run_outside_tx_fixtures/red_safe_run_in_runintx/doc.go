//go:build archtest_fixture

// Package redsaferunninruntintx is a RED fixture for SAGA-STEP-RUN-OUTSIDE-TX-01
// A2: a file calls safeRun() DIRECTLY inside a TxRunner.RunInTx closure body.
// Proves the transitive taint A2 still fires on the direct case (safeRun is the
// trivial taint-set member), so the transitive entry subsumes the old direct
// scan (user step code would otherwise run with a DB transaction held open).
// Expect one diagnostic from A2.
package redsaferunninruntintx
