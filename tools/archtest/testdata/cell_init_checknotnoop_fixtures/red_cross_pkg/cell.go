//go:build archtest_fixture

// Package redcrosspkg models an L2+ cell whose Init reaches
// outbox.CheckNotNoop only via a cross-package helper. The
// CELL-L2-INIT-CHECKNOTNOOP-CALLED-01 archtest restricts BFS to same-package
// callees (K#04 hand-written hook convention); cross-package indirection is
// rejected as a Soft-ification escape and the archtest must emit one
// diagnostic for this package.
package redcrosspkg

import (
	"context"

	"github.com/ghbvf/gocell/tools/archtest/testdata/cell_init_checknotnoop_fixtures/red_cross_pkg/internal/wrapcheck"
)

// RedCrossPkgCell is the fake L2+ cell type. The archtest matches it via
// cell.yaml goStructName == "RedCrossPkgCell".
type RedCrossPkgCell struct{}

// Init delegates the durability check to a cross-package helper. Phase B's
// same-package BFS does NOT descend into wrapcheck, so the archtest must
// report this cell as missing the call.
func (c *RedCrossPkgCell) Init(ctx context.Context, reg any) error {
	_ = ctx
	_ = reg
	return wrapcheck.Check()
}
