package governance

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// lifecyclePhaseFix is the remediation guidance for invalid lifecycle phase
// findings (membership check). Used in both the cell and slice error messages
// to keep them in sync.
const lifecyclePhaseFix = "set lifecycle to one of: experimental, candidate, asset, maintenance, retired"

// validateLifecyclePhase implements LIFECYCLE-PHASE-01.
//
// Two checks on the cell.yaml / slice.yaml `lifecycle` maturity phase:
//
//  1. Membership: a non-empty lifecycle on a cell or slice must be one of the
//     declared phases (experimental|candidate|asset|maintenance|retired). This
//     mirrors the cell.schema.json enum for in-memory ProjectMeta fixtures that
//     bypass the parser, and gives a locatable, fix-bearing finding.
//  2. Consistency: a slice's phase must not be more mature than its parent
//     cell's phase — a slice cannot be a stable "asset" while its containing
//     cell is still "experimental". Empty defaults to experimental (the same
//     default NewBaseCell applies), so an undeclared cell with a declared mature
//     slice is correctly flagged.
//
// Blocking (error) severity, matching SLICE-CONSISTENCY-01 (its maturity-axis
// sibling: that rule bounds consistencyLevel, this one bounds maturity phase).
// Note there is no runtime "phase transition" to validate — a declarative
// single-value field only admits static legality (membership + slice≤cell),
// which is exactly what this rule covers.
func (v *Validator) validateLifecyclePhase() []ValidationResult {
	var results []ValidationResult

	for _, c := range v.project.Cells {
		if c.Lifecycle == "" {
			continue
		}
		if _, err := cellvocab.ParsePhase(c.Lifecycle); err != nil {
			results = append(results, v.newError(
				codeLIFECYCLEPHASE01, IssueInvalid,
				cellFile(c), "lifecycle",
				fmt.Sprintf("cell %q declares lifecycle %q which is not a valid maturity phase", c.ID, c.Lifecycle),
				lifecyclePhaseFix,
			))
		}
	}

	for _, s := range v.project.Slices {
		if s.Lifecycle != "" {
			if _, err := cellvocab.ParsePhase(s.Lifecycle); err != nil {
				results = append(results, v.newError(
					codeLIFECYCLEPHASE01, IssueInvalid,
					sliceFile(s), "lifecycle",
					fmt.Sprintf("slice %q declares lifecycle %q which is not a valid maturity phase", s.ID, s.Lifecycle),
					lifecyclePhaseFix,
				))
				continue
			}
		}
		results = append(results, v.checkSlicePhaseNotAboveCell(s)...)
	}

	return results
}

// checkSlicePhaseNotAboveCell returns a finding when slice s declares (or
// defaults to) a maturity phase strictly above its parent cell's, else nil. The
// parent cell's missing/invalid phase is skipped here (REF-01 / the membership
// check above already cover those).
func (v *Validator) checkSlicePhaseNotAboveCell(s *metadata.SliceMeta) []ValidationResult {
	parentCell, ok := v.project.Cells[s.BelongsToCell]
	if !ok {
		return nil // REF-01 covers the missing parent cell
	}
	cellRank := effectivePhaseRank(parentCell.Lifecycle)
	sliceRank := effectivePhaseRank(s.Lifecycle)
	// negative rank = invalid phase already flagged by the membership check
	// above; skip to avoid a duplicate finding (distinct from the
	// missing-cell/REF-01 case).
	if cellRank < 0 || sliceRank < 0 || sliceRank <= cellRank {
		return nil
	}
	return []ValidationResult{v.newError(
		codeLIFECYCLEPHASE01, IssueInvalid,
		sliceFile(s), "lifecycle",
		fmt.Sprintf(
			"slice %q lifecycle %q is more mature than parent cell %q (%q); a slice cannot exceed its cell's maturity",
			s.ID, effectivePhaseLabel(s.Lifecycle), parentCell.ID, effectivePhaseLabel(parentCell.Lifecycle),
		),
		"lower the slice lifecycle to the cell's phase or below, or raise the cell's lifecycle",
	)}
}

// effectivePhaseRank returns the maturity rank of a declared lifecycle string,
// treating empty as experimental (the NewBaseCell construction default) so the
// governance comparison matches runtime semantics. Returns -1 for a non-empty
// invalid value (flagged separately by the membership check).
func effectivePhaseRank(lifecycle string) int {
	if lifecycle == "" {
		return cellvocab.PhaseExperimental.Rank()
	}
	return cellvocab.PhaseRank(lifecycle)
}

// effectivePhaseLabel renders a lifecycle string for error messages,
// substituting the experimental default for an empty value.
func effectivePhaseLabel(lifecycle string) string {
	if lifecycle == "" {
		return string(cellvocab.PhaseExperimental)
	}
	return lifecycle
}
