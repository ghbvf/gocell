//go:build archtest_fixture

// Package boundarytransitive models an L2+ cell whose CheckNotNoop call
// lives in a same-package initInternal callee (the K#04 hand-written hook
// convention used by configcore/accesscore/auditcore). The
// CELL-L2-INIT-CHECKNOTNOOP-CALLED-01 archtest's BFS must descend into
// initInternal and find the call — no diagnostic expected.
package boundarytransitive

import (
	"context"

	"github.com/ghbvf/gocell/kernel/cell"
)

// BoundaryTransitiveCell is the fake L2+ cell type. The archtest matches it
// via cell.yaml goStructName == "BoundaryTransitiveCell".
type BoundaryTransitiveCell struct{}

// Init mirrors the codegen Init shape: it delegates to a hand-written hook
// (initInternal) defined later in the same package.
func (c *BoundaryTransitiveCell) Init(ctx context.Context, reg any) error {
	_ = ctx
	_ = reg
	return c.initInternal()
}

// initInternal is the K#04 hand-written hook that performs the actual
// durability guard. The BFS must reach this function via same-package
// callee resolution from Init.
func (c *BoundaryTransitiveCell) initInternal() error {
	return cell.CheckNotNoop(cell.DurabilityDemo, "fixture-boundary_transitive")
}
