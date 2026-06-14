//go:build archtest_fixture

// Package violation is a deliberate PROM-CELL-LABEL-FUNNEL-01 negative
// fixture loaded only when the archtest_fixture build tag is set.
//
// The build tag excludes this package from `go build ./...` and `go test
// ./...` so it never pollutes real-repo scans. It is loaded explicitly by
// TestPromCellLabelFunnel_ScannerDetectsViolation via
//
//	archtest.Run(t, archtest.Fixture(archtest.FixtureOpts{Tests: false},
//	    []string{"./tools/archtest/internal/promcelllabelfixture/violation"}), rule)
//
// The scan must report the ev.CellID read below as a violation because it
// reads cell.HookEvent.CellID directly (NOT via promCellLabel), which is
// what PROM-CELL-LABEL-FUNNEL-01 forbids.
package violation

import "github.com/ghbvf/gocell/framework/kernel/cell"

// leak reads HookEvent.CellID directly (NOT via promCellLabel) — a
// PROM-CELL-LABEL-FUNNEL-01 violation.
func leak(ev cell.HookEvent) string { return ev.CellID }
