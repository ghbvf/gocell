//go:build archtest_fixture

// Package boundaryl1 models an L1 cell that does not call outbox.CheckNotNoop.
// The CELL-L2-INIT-CHECKNOTNOOP-CALLED-01 archtest only applies to L2+ and
// must NOT emit a diagnostic for this package.
package boundaryl1

import (
	"context"
)

// BoundaryL1Cell is a fake L1 cell type — Phase A drops it before Phase B
// scans, so the test passes an empty target list to mirror that behavior.
type BoundaryL1Cell struct{}

// Init lacks CheckNotNoop; for an L1 cell this is fine.
func (c *BoundaryL1Cell) Init(ctx context.Context, reg any) error {
	_ = ctx
	_ = reg
	return nil
}
