//go:build archtest_fixture

// Package kernelcellsibling is a deliberate CELL-RAW-INFRA-WRAPPER-LOCATION-01
// negative fixture loaded only when the archtest_fixture build tag is set.
//
// This fixture simulates a file that lives in the kernel/cell directory (as a
// sibling to the allowlisted kernel/cell/demo_tx_runner.go) but is NOT itself
// allowlisted. The scanner must detect that a file whose relative path is
// kernel/cell/sibling_helper.go (not demo_tx_runner.go) is NOT in the
// wrapper-call allowlist and must report the WrapForCell call as a violation.
//
// This exercises the boundary of the allowlist: demo_tx_runner.go is the ONLY
// kernel/cell file allowed to call WrapForCell; any other kernel/cell file is a
// violation.
package kernelcellsibling

import (
	"github.com/ghbvf/gocell/kernel/persistence"
)

// CallWrapForCellFromKernelCellSibling deliberately calls persistence.WrapForCell
// from a non-allowlisted location that resembles a kernel/cell sibling file.
// CELL-RAW-INFRA-WRAPPER-LOCATION-01 must catch this call.
func CallWrapForCellFromKernelCellSibling(tr persistence.TxRunner) persistence.CellTxManager {
	return persistence.WrapForCell(tr)
}
