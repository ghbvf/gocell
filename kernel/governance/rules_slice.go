package governance

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/cell"
)

// validateSliceConsistency checks that if a slice declares an explicit
// consistencyLevel, it must be ≤ the parent cell's consistencyLevel
// (a slice can downgrade but never upgrade beyond its cell's contract).
//
// Rule: SLICE-CONSISTENCY-01
// Rationale: slice.consistencyLevel allows a cell to host slices with weaker
// guarantees (e.g., a L2 cell with an L1 slice that doesn't emit events);
// upgrading would silently break the cell-level contract.
func (v *Validator) validateSliceConsistency() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		if s.ConsistencyLevel == "" {
			// empty means inherit cell — always valid
			continue
		}
		sliceLevel, err := cell.ParseLevel(s.ConsistencyLevel)
		if err != nil {
			results = append(results, v.newResult(
				"SLICE-CONSISTENCY-01", SeverityError, IssueInvalid,
				sliceFile(s),
				"consistencyLevel",
				fmt.Sprintf(
					"slice %q declares consistencyLevel %q which is not valid (must be L0-L4)",
					s.ID, s.ConsistencyLevel,
				),
			))
			continue
		}
		parentCell, ok := v.project.Cells[s.BelongsToCell]
		if !ok {
			// REF-01 already catches missing parent cell; skip here
			continue
		}
		cellLevel, err := cell.ParseLevel(parentCell.ConsistencyLevel)
		if err != nil {
			// FMT-03 already catches invalid cell consistencyLevel; skip here
			continue
		}
		if sliceLevel > cellLevel {
			results = append(results, v.newResult(
				"SLICE-CONSISTENCY-01", SeverityError, IssueInvalid,
				sliceFile(s),
				"consistencyLevel",
				fmt.Sprintf(
					"slice %q declares consistencyLevel %q which is stronger than parent cell %q (%q); "+
						"a slice can downgrade but not upgrade",
					s.ID, s.ConsistencyLevel, parentCell.ID, parentCell.ConsistencyLevel,
				),
			))
		}
	}
	return results
}
