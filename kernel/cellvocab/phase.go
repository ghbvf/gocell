package cellvocab

import (
	"fmt"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Phase is the governance maturity phase of a Cell or Slice, declared in
// cell.yaml / slice.yaml `lifecycle`. It is orthogonal to four other "lifecycle"
// concepts in the codebase:
//
//   - the runtime cellState (New/Initialized/Started/Stopped) in kernel/cell —
//     where a cell is in its boot/shutdown sequence;
//   - cellvocab.Lifecycle (draft/active/deprecated) — a Contract's wire-stability
//     state;
//   - JourneyMeta.Lifecycle (active/experimental) — a Journey's delivery status.
//
// Phase is a maturity axis (how production-ready the cell/slice is), not a
// runtime state machine: a cell.yaml declares one static phase, and there is no
// runtime "phase transition" event (advancing maturity is a human edit). It is
// therefore a plain ordered enum — there is deliberately no transition map /
// Transition() here. The ordering (Phases) feeds PhaseRank, which the
// LIFECYCLE-PHASE-01 governance rule uses for the slice≤cell consistency check.
//
// ref: Team Topologies maturity vocabulary (experimental → asset).
// ref: docs/architecture/202605262100-adr-cell-slice-lifecycle-phase.md §Decision-A (why no transition map).
type Phase string

const (
	PhaseExperimental Phase = "experimental" // early development, may change/disappear
	PhaseCandidate    Phase = "candidate"    // stabilizing, approaching production use
	PhaseAsset        Phase = "asset"        // stable, production-relied-upon
	PhaseMaintenance  Phase = "maintenance"  // stable but no longer actively evolved
	PhaseRetired      Phase = "retired"      // end of life
)

// Phases lists the canonical maturity phases in ascending rank order.
// Index = rank. Single source of truth for PhaseRank.
//
// It is an array (not a slice) so its length is fixed at compile time;
// CELL-PHASE-RANK-COMPLETENESS-01 archtest verifies every Phase const above
// appears here, so a forgotten entry (which would make PhaseRank return -1 and
// silently break the governance ordering) fails CI.
//
// Callers MUST NOT mutate Phases — it is the authoritative ordered set.
var Phases = [...]Phase{
	PhaseExperimental, PhaseCandidate, PhaseAsset, PhaseMaintenance, PhaseRetired,
}

// PhaseRank returns the ascending rank of phase string s, or -1 if s is not a
// known phase. The string form is canonical because governance reads the raw
// cell.yaml / slice.yaml `lifecycle` strings before any Phase value is bound
// (mirrors cellvocab.Rank for consistency levels).
func PhaseRank(s string) int {
	for i, p := range Phases {
		if Phase(s) == p {
			return i
		}
	}
	return -1
}

// Rank returns the ascending rank of p, or -1 if p is not a known phase.
func (p Phase) Rank() int { return PhaseRank(string(p)) }

// ParsePhase parses a string into a Phase.
// Returns errcode.ErrValidationFailed for unrecognized input.
func ParsePhase(s string) (Phase, error) {
	if PhaseRank(s) >= 0 {
		return Phase(s), nil
	}
	return "", errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
		"invalid lifecycle phase",
		errcode.WithInternal(fmt.Sprintf(internalValueQuotedFmt, s)))
}
