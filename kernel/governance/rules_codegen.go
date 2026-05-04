package governance

import "fmt"

// validateCODEGEN01 enforces that every slice routeMount references a listener
// declared in the parent cell's listeners. Without this gate, the only place
// a typo in slice.yaml `routeMounts[i].listener` is caught is at codegen time
// (cellgen builder) — `gocell validate` would silently pass and leave the
// failure to surface only when a developer invokes `gocell generate cell`.
//
// The gate runs at parse-time alongside other ADV-* / FMT-* checks so that
// `gocell validate --strict` fails fast on listener-reference typos exactly
// like it would fail on contract-reference typos (REF series).
//
// ref: docs/plans/202605011500-029-master-roadmap.md K#04 IMP-9 (PR-1 review)
func (v *Validator) validateCODEGEN01() []ValidationResult {
	var results []ValidationResult

	for _, s := range v.project.Slices {
		if len(s.RouteMounts) == 0 {
			continue
		}
		// Each slice must belong to a cell that declares a matching listener.
		cell, ok := v.project.Cells[s.BelongsToCell]
		if !ok {
			// REF-* already flags slices with unknown belongsToCell; skip
			// here to avoid double-reporting and to keep CODEGEN-01 focused
			// on the listener-reference invariant.
			continue
		}
		declared := make(map[string]bool, len(cell.Listeners))
		for _, l := range cell.Listeners {
			declared[l.Ref] = true
		}
		for i, m := range s.RouteMounts {
			if !declared[m.Listener] {
				results = append(results, v.newResult(
					"CODEGEN-01", SeverityError, IssueRefNotFound,
					sliceFile(s),
					fmt.Sprintf("routeMounts[%d].listener", i),
					fmt.Sprintf("slice %q routeMount references undeclared listener %q "+
						"(declare it in cells/%s/cell.yaml listeners[].ref)",
						s.ID, m.Listener, s.BelongsToCell),
				))
			}
		}
	}
	return results
}
