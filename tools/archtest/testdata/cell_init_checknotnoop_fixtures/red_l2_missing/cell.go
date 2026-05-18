//go:build archtest_fixture

// Package redl2missing models an L2+ cell whose Init body and same-package
// transitive callees never invoke cell.CheckNotNoop. The
// CELL-L2-INIT-CHECKNOTNOOP-CALLED-01 archtest must emit one diagnostic for
// this package.
package redl2missing

import "context"

// RedL2Cell is the fake L2+ cell type. The archtest matches it via cell.yaml
// goStructName == "RedL2Cell".
type RedL2Cell struct{}

// Init intentionally lacks any CheckNotNoop call — this is the RED behavior
// the archtest must surface. The signature mirrors a real cell.Cell.Init but
// uses `any` for the registry argument so the fixture does not need to
// import kernel/cell (smaller blast radius for the fixture package).
func (c *RedL2Cell) Init(ctx context.Context, reg any) error {
	_ = ctx
	_ = reg
	_ = doSomeOtherWork() // exercises BFS over same-package callees
	return nil
}

// doSomeOtherWork is a same-package transitive callee that also lacks
// CheckNotNoop. The BFS must descend into it before declaring the target
// unsatisfied.
func doSomeOtherWork() error {
	return nil
}
