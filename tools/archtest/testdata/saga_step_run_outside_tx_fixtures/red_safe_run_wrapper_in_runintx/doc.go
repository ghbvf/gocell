//go:build archtest_fixture

// Package redsaferunwrapperinrunintx is a RED fixture for
// SAGA-STEP-RUN-OUTSIDE-TX-01 A2 (transitive): three RunInTx closures reach
// safeRun INDIRECTLY — via a named helper func (wrap), via a package-level
// FuncLit-valued var (vwrap), and via a func-literal-valued var passed as the
// RunInTx callback itself (cb). The OLD direct-identifier / inline-only scan
// missed all three; the transitive taint-graph A2 with callback-var resolution
// flags every wrapper callsite (gh #980 incl. cx-1; callback-var via gh #1998
// review F2). Expect three diagnostics from A2.
package redsaferunwrapperinrunintx
