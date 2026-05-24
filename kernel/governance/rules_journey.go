package governance

// rules_journey.go: journey-themed cross-file consistency rules.
//
// JOURNEY-CONTRACT-EXISTENCE-01 (Error, inverse of REF-07):
//
//	every active non-deprecated platform contract must be referenced by at
//	least one journey.contracts[]. examples/ contracts exempt — example
//	projects manage their own journey/contract closure.
//
// JOURNEY-STATUS-LIFECYCLE-01 (Error/Warning):
//
//	status-board[i].state × J-*.yaml.lifecycle matrix enforces progress↔
//	maturity alignment. board.state and yaml.lifecycle are orthogonal axes
//	(work-progress vs production-maturity); the matrix below codifies the
//	legal combinations rather than collapsing them into a single field.
//
//	  "todo"  → {"experimental"}              — not yet started, must be experimental
//	  "doing" → {"experimental", "active"}    — in progress, lifecycle can be either
//	  "done"  → {"active"}                    — finished, must have promoted to active
//
//	The matrix tracks the lifecycle vocab actually accepted by FMT (rules_fmt.go
//	validJourneyLifecycles) and the journey.schema.json enum — currently
//	{active, experimental} only. If a future PR introduces a stable lifecycle
//	state, it must (1) extend the schema enum, (2) extend validJourneyLifecycles,
//	and (3) extend this matrix simultaneously — anything less leaves a phantom
//	state in the matrix that the schema layer rejects unreachable.
//
// AI-robust grade: Medium.

import (
	"fmt"
	"sort"
	"strings"
)

// validBoardLifecycleMatrix maps board.state → set of allowed J-*.yaml
// lifecycle values. Package-private and treated as immutable after init.
//
// Reverse-lookup intuition for readers: `state: "todo"` requires
// `lifecycle: "experimental"`; `state: "done"` requires `lifecycle: "active"`;
// `state: "doing"` accepts either. The matrix entries are kept in lock-step
// with rules_fmt.go validJourneyLifecycles so an unsupported lifecycle
// value cannot show up here as a "phantom" target.
var validBoardLifecycleMatrix = map[string]map[string]bool{
	"todo":  {"experimental": true},
	"doing": {"experimental": true, "active": true},
	"done":  {"active": true},
}

// validateJOURNEYCONTRACTEXISTENCE01 enforces the inverse of REF-07: every
// platform active contract must be referenced by at least one
// journey.contracts[] entry. Closes the "dead contract" gap where a
// contract is declared and lifecycle: active but no journey covers it.
//
// Exemptions:
//   - lifecycle != "active" — experimental/deprecated/stable contracts
//     do not require journey coverage (experimental is design-phase
//     unsettled; deprecated is on the way out; stable will be covered by
//     stable journey lifecycle).
//   - contracts under examples/ — example projects manage their own
//     coverage; platform governance does not span their closure.
func (v *Validator) validateJOURNEYCONTRACTEXISTENCE01() []ValidationResult {
	referenced := make(map[string]bool, len(v.project.Contracts))
	for _, j := range v.project.Journeys {
		for _, cid := range j.Contracts {
			referenced[cid] = true
		}
	}
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Lifecycle != "active" {
			continue
		}
		if strings.HasPrefix(c.File, "examples/") {
			continue
		}
		if referenced[c.ID] {
			continue
		}
		results = append(results, v.newError(
			codeJOURNEYCONTRACTEXISTENCE01, IssueRequired,
			contractFile(c), "id",
			fmt.Sprintf(
				"active platform contract %q is not referenced by any journey.contracts[];"+
					" every active contract must appear in at least one journey/J-*.yaml contracts[]",
				c.ID,
			),
			fmt.Sprintf("add %q to an existing journey contracts[] or change contract lifecycle to experimental/deprecated", c.ID),
		))
	}
	return results
}

// validateJOURNEYSTATUSLIFECYCLE01 enforces the board.state × yaml.lifecycle
// matrix. Three skip conditions:
//   - journey ID in status-board not in project.Journeys → ADV-04 handles
//   - board.state not in matrix keys → FMT-22 handles enum membership
//   - board.state in matrix but yaml.lifecycle not in allowed set → Error
//   - lifecycle: active + state: doing → Warning (in-transit reminder)
//
// The active+doing Warning is intentional: an active journey should
// progress to done over time. Long-running active+doing combinations
// often signal stuck work or a missed promote-to-stable opportunity, but
// the combination itself is legal so it is non-blocking.
func (v *Validator) validateJOURNEYSTATUSLIFECYCLE01() []ValidationResult {
	var results []ValidationResult
	for i, e := range v.project.StatusBoard {
		j, ok := v.project.Journeys[e.JourneyID]
		if !ok {
			continue // ADV-04 covers orphan board entry
		}
		allowed, knownState := validBoardLifecycleMatrix[e.State]
		if !knownState {
			continue // FMT-22 covers invalid state enum
		}
		if !allowed[j.Lifecycle] {
			results = append(results, v.newError(
				codeJOURNEYSTATUSLIFECYCLE01, IssueMismatch,
				"journeys/status-board.yaml",
				fmt.Sprintf("[%d].state", i),
				fmt.Sprintf(
					"status-board entry for %q has state %q but journey lifecycle is %q;"+
						" allowed lifecycles for state %q: %s",
					e.JourneyID, e.State, j.Lifecycle, e.State,
					sortedAllowedLifecycles(allowed),
				),
				fmt.Sprintf("align journey lifecycle in journeys/%s.yaml or update board state to match", e.JourneyID),
			))
			continue
		}
		if j.Lifecycle == "active" && e.State == "doing" {
			results = append(results, v.newWarning(
				codeJOURNEYSTATUSLIFECYCLE01, IssueMismatch,
				"journeys/status-board.yaml",
				fmt.Sprintf("[%d].state", i),
				fmt.Sprintf(
					"journey %q is lifecycle: active but board state is %q;"+
						" active journeys should progress toward done;"+
						" advance the board to done when all passCriteria stabilize, or revert lifecycle to experimental if work has reopened",
					e.JourneyID, e.State,
				),
			))
		}
	}
	return results
}

// sortedAllowedLifecycles returns a deterministic comma-separated list of
// allowed lifecycle keys for use in error messages. Sorting prevents
// flaky-looking test failures when message ordering depends on Go map
// iteration order.
func sortedAllowedLifecycles(m map[string]bool) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}
