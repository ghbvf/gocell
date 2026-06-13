//go:build archtest_fixture

// Package redsaferunwrapperinrunintx is a RED fixture for
// SAGA-STEP-RUN-OUTSIDE-TX-01 A2 (transitive): two RunInTx closures reach
// safeRun INDIRECTLY — one via a named helper func (wrap), one via a
// package-level FuncLit-valued var (vwrap). The OLD direct-identifier A2 scan
// misses both (neither closure body contains a literal safeRun() callsite);
// the transitive taint-graph A2 flags both wrapper callsites. Proves the
// helper-function + FuncLit-var transitivity upgrade (gh #980, incl. cx-1).
// Expect two diagnostics from A2.
package redsaferunwrapperinrunintx
