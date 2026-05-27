package cellvocab

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// CellLifecycle is the governance maturity lifecycle of a Cell or Slice, declared
// in cell.yaml / slice.yaml `lifecycle`. It is orthogonal to three other "lifecycle"
// concepts in the codebase:
//
//   - the runtime cellState (New/Initialized/Started/Stopped) in kernel/cell —
//     where a cell is in its boot/shutdown sequence;
//   - cellvocab.ContractLifecycle (draft/active/deprecated) — a Contract's wire-stability
//     state;
//   - JourneyMeta.Lifecycle (active/experimental) — a Journey's delivery status.
//
// CellLifecycle is a maturity axis (how production-ready the cell/slice is), not a
// runtime state machine: a cell.yaml declares one static lifecycle value, and there
// is no runtime "lifecycle transition" event (advancing maturity is a human edit). It
// is therefore a plain ordered enum — there is deliberately no transition map /
// Transition() here. The ordering (cellLifecycles) feeds CellLifecycleRank, which the
// CELL-LIFECYCLE-01 governance rule uses for the slice≤cell consistency check.
//
// ref: Team Topologies maturity vocabulary (experimental → asset).
// ref: docs/architecture/202605262100-adr-cell-slice-lifecycle-phase.md §Decision-A (why no transition map).
type CellLifecycle string

const (
	CellLifecycleExperimental CellLifecycle = "experimental" // early development, may change/disappear
	CellLifecycleCandidate    CellLifecycle = "candidate"    // stabilizing, approaching production use
	CellLifecycleAsset        CellLifecycle = "asset"        // stable, production-relied-upon
	CellLifecycleMaintenance  CellLifecycle = "maintenance"  // stable but no longer actively evolved
	CellLifecycleRetired      CellLifecycle = "retired"      // end of life
)

// cellLifecycles lists the canonical maturity lifecycles in ascending rank order.
// Index = rank. Single source of truth for CellLifecycleRank.
//
// It is an array (not a slice) so its length is fixed at compile time;
// CELL-LIFECYCLE-RANK-COMPLETENESS-01 archtest verifies every CellLifecycle const
// above appears here, so a forgotten entry (which would make CellLifecycleRank return
// -1 and silently break the governance ordering) fails CI.
//
// The private var ensures callers cannot mutate the authoritative ordered set;
// use AllCellLifecycles() to obtain a read-only copy.
var cellLifecycles = [...]CellLifecycle{
	CellLifecycleExperimental, CellLifecycleCandidate, CellLifecycleAsset, CellLifecycleMaintenance, CellLifecycleRetired,
}

// AllCellLifecycles returns the canonical ordered set of cell/slice maturity
// lifecycles as a value copy (callers cannot mutate the authoritative source).
// Index = ascending rank; use CellLifecycleRank to look up a rank by string.
func AllCellLifecycles() [5]CellLifecycle { return cellLifecycles }

// CellLifecycleRank returns the ascending rank of lifecycle string s, or -1 if s
// is not a known cell lifecycle. The string form is canonical because governance
// reads the raw cell.yaml / slice.yaml `lifecycle` strings before any CellLifecycle
// value is bound (mirrors cellvocab.Rank for consistency levels).
func CellLifecycleRank(s string) int {
	for i, p := range cellLifecycles {
		if CellLifecycle(s) == p {
			return i
		}
	}
	return -1
}

// Rank returns the ascending rank of p, or -1 if p is not a known cell lifecycle.
func (p CellLifecycle) Rank() int { return CellLifecycleRank(string(p)) }

// ParseCellLifecycle parses a string into a CellLifecycle.
// Returns errcode.ErrValidationFailed for unrecognized input.
func ParseCellLifecycle(s string) (CellLifecycle, error) {
	if CellLifecycleRank(s) >= 0 {
		return CellLifecycle(s), nil
	}
	return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"invalid lifecycle phase",
		errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf(internalValueQuotedFmt, s))))
}
