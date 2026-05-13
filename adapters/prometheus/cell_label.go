// INVARIANT: PROM-CELL-LABEL-FUNNEL-01 — every CounterVec/HistogramVec
// WithLabelValues call in this package that emits a cell_id must route the
// cell id through promCellLabel.

package prometheus

import (
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
)

// promCellLabel is the single typed-function funnel for writing a cell id
// into a Prometheus label. Upstream invariants (schemas/cell.schema.json
// id.pattern, governance rule FMT-C1) already guarantee every cell id in
// flight matches metadata.CellIDPattern; this function is an A-class
// unreachable defense in the sense of the panic taxonomy
// (docs/architecture/202604270030-architectural-panic-whitelist.md §4.1).
// The archtest PROM-CELL-LABEL-FUNNEL-01 enforces form uniqueness so a
// future caller cannot bypass the funnel and silently introduce a
// non-conforming cell_id series — Bypass `WithLabelValues(...string)`'s
// unbounded-string operation is closed at CI time, matching the panic
// funnel pattern (PANIC-REGISTERED-01) charter §4 Wave 2.
//
// ref: pkg/panicregister.Approved — typed marker funnel for panic
// ref: tools/archtest/panic_invariants_test.go — Hard form-uniqueness pattern
func promCellLabel(id string) string {
	if !metadata.MatchCellID(id) {
		panic(panicregister.Approved("prom-cell-label-invalid",
			errcode.Assertion("prom adapter received cell id violating CellIDPattern")))
	}
	return id
}
