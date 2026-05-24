//go:build archtest_fixture

// Package wrapcheck is a cross-package helper invoked from
// redcrosspkg.RedCrossPkgCell.Init. It calls outbox.CheckNotNoop on behalf of
// the cell. The CELL-L2-INIT-CHECKNOTNOOP-CALLED-01 archtest must still
// report the cell as missing the call because the BFS is same-package only.
package wrapcheck

import (
	"github.com/ghbvf/gocell/kernel/outbox"
)

// Check delegates the durability-mode guard to kernel/outbox.CheckNotNoop.
// Calling this from another package does NOT satisfy the archtest's same-
// package BFS — by design.
func Check() error {
	return outbox.CheckNotNoop(outbox.DurabilityDemo, "fixture-red_cross_pkg")
}
