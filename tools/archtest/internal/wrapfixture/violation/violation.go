//go:build archtest_fixture

// Package violation is a deliberate CELL-RAW-INFRA-WRAPPER-LOCATION-01
// negative fixture loaded only when the archtest_fixture build tag is set.
//
// The build tag excludes this package from `go build ./...` and `go test
// ./...` so it never pollutes real-repo scans. It is loaded explicitly by
// TestCellRawInfraWrapperLocation01_ScannerDetectsViolation via
//
//	typeseval.SharedResolver(root, false, []string{"archtest_fixture"},
//	    "./tools/archtest/internal/wrapfixture/violation")
//
// The scan must report the WrapForCell call below as a violation because
// tools/archtest/internal/wrapfixture/violation is NOT in the
// wrapper-call allowlist (cmd/* + examples/<demo>/main.go +
// examples/<demo>/app.go + *_test.go + the wrapper definitions).
package violation

import (
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// CallWrapForCell deliberately calls persistence.WrapForCell from a
// non-allowlisted location. The CELL-RAW-INFRA-WRAPPER-LOCATION-01
// scanner must catch this call.
func CallWrapForCell(tr persistence.TxRunner) persistence.CellTxManager {
	return persistence.WrapForCell(tr)
}

// CallWrapPublisherForCell mirrors CallWrapForCell for the publisher
// wrapper — gives the scanner-detection test a negative case for the
// outbox.WrapPublisherForCell function leg of the wrapper-location
// invariant.
func CallWrapPublisherForCell(p outbox.Publisher) outbox.CellPublisher {
	return outbox.WrapPublisherForCell(p)
}

// CallWrapWriterForCell mirrors CallWrapForCell for the writer wrapper.
func CallWrapWriterForCell(w outbox.Writer) outbox.CellWriter {
	return outbox.WrapWriterForCell(w)
}
