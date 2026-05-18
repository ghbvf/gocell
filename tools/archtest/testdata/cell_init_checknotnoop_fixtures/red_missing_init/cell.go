//go:build archtest_fixture

// Package redmissinginit models an L2+ cell that declares goStructName in
// cell.yaml but provides NO `Init` method on the receiver type. Phase B
// must emit the "missing Init method" diagnostic (distinct from the
// "does not call CheckNotNoop" branch) — F5 fix in PR #576 round-2 review.
package redmissinginit

// RedMissingInitCell is the fake L2+ cell type. The archtest matches it
// via cell.yaml goStructName == "RedMissingInitCell", but no Init method
// is declared — Phase B's initFuncDecl returns nil, triggering the
// missing-Init diagnostic branch.
type RedMissingInitCell struct{}

// (Intentionally NO Init method — the fixture pins the missing-Init
// diagnostic branch of scanCellsForInitCheckNotNoop.)
