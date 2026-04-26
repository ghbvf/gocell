package governance

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// validateADV01 checks that every journey has a corresponding entry in the status board.
func (v *Validator) validateADV01() []ValidationResult {
	var results []ValidationResult

	// Build a set of journey IDs present in the status board.
	sbJourneys := make(map[string]bool, len(v.project.StatusBoard))
	for _, entry := range v.project.StatusBoard {
		sbJourneys[entry.JourneyID] = true
	}

	for _, j := range v.project.Journeys {
		if !sbJourneys[j.ID] {
			results = append(results, v.newResult(
				"ADV-01", SeverityWarning, IssueRefNotFound,
				journeyFile(j),
				"id",
				fmt.Sprintf("journey %q has no entry in status-board.yaml", j.ID),
			))
		}
	}
	return results
}

// validateADV03 checks that waivers reference contracts that appear in the slice's contractUsages.
func (v *Validator) validateADV03() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		// Build set of contracts used by this slice.
		usedContracts := make(map[string]bool, len(s.ContractUsages))
		for _, cu := range s.ContractUsages {
			usedContracts[cu.Contract] = true
		}
		for i, w := range s.Verify.Waivers {
			if w.Contract != "" && !usedContracts[w.Contract] {
				results = append(results, v.newResult(
					"ADV-03", SeverityWarning, IssueRefNotFound,
					sliceFile(s),
					fmt.Sprintf("verify.waivers[%d].contract", i),
					fmt.Sprintf("waiver for contract %q has no matching contractUsage in slice %q", w.Contract, s.ID),
				))
			}
		}
	}
	return results
}

// validateADV04 checks that status-board entries reference existing journeys.
// status-board.yaml's root is a YAML sequence (no "entries" wrapper), so the
// field path uses the locator's root-index form "[i].journeyId".
func (v *Validator) validateADV04() []ValidationResult {
	var results []ValidationResult
	for i, entry := range v.project.StatusBoard {
		if _, ok := v.project.Journeys[entry.JourneyID]; !ok {
			results = append(results, v.newResult(
				"ADV-04", SeverityWarning, IssueRefNotFound,
				"journeys/status-board.yaml",
				fmt.Sprintf("[%d].journeyId", i),
				fmt.Sprintf("status-board entry references unknown journey %q", entry.JourneyID),
			))
		}
	}
	return results
}

// validateADV05 checks that active event contracts have at least one subscriber.
// An event contract with lifecycle "active" and an empty (or nil) subscribers list
// is a dead event — it will never be consumed and its producer is publishing to
// no one. The contract must either be wired with subscribers or moved out of active.
//
// Only lifecycle "active" is checked. Draft contracts are allowed to be unwired
// during the design phase — subscribers are not required until the contract
// transitions to active. Deprecated contracts are on their way out and are also
// exempt. Non-event contracts (http, command, projection) are not checked.
func (v *Validator) validateADV05() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != "event" {
			continue
		}
		if c.Lifecycle != "active" {
			continue
		}
		if len(c.Endpoints.Subscribers) == 0 {
			results = append(results, v.newResult(
				"ADV-05", SeverityError, IssueForbidden,
				contractFile(c),
				"endpoints.subscribers",
				fmt.Sprintf("event contract %q is active but has no subscribers; mark lifecycle: deprecated or add at least one cell or actor to endpoints.subscribers in the contract.yaml", c.ID),
			))
		}
	}
	return results
}

// validateADV06 detects subscription declaration drift between contract.yaml's
// endpoints.subscribers and slice.yaml's contractUsages[role=subscribe].
//
// ADV-06 checks that contract.yaml and slice.yaml agree on which cells subscribe
// to a given active event contract. Drift (contract lists a cell that has no
// matching subscribe usage, or a slice declares subscribe but the contract does
// not list its cell) means the audit/event-consumer declaration is out of sync
// with the implementation — the "declaration ≠ implementation" anti-pattern.
// This is as critical as ADV-05 (active event with no subscriber) and must be
// CI fail-closed.
//
// The two YAML files must agree on which cells subscribe to a given event:
//
//   - Direction A (contract → slice): when a contract's endpoints.subscribers
//     names cell C, at least one slice belonging to C must declare
//     contractUsage{contract: <id>, role: "subscribe"}. Otherwise the contract
//     advertises a subscriber that the cell has not registered.
//
//   - Direction B (slice → contract): when a slice declares a subscribe usage
//     for contract X, X's endpoints.subscribers must list the slice's owning
//     cell. Otherwise the cell silently subscribes to an event that the
//     contract does not acknowledge.
//
// Only lifecycle "active" event contracts are checked. Draft contracts are
// allowed to be misaligned during the design phase; deprecated contracts are
// on their way out. External actors in subscribers are skipped because actors
// do not own slices and therefore cannot carry contractUsages.
//
// Non-event contracts are not checked: this rule targets the
// contract.subscribers ↔ slice.contractUsages.subscribe pair specifically.
// Other endpoint roles (clients, invokers, readers) have their own consistency
// rules elsewhere.
func (v *Validator) validateADV06() []ValidationResult {
	cellSubscribes := buildCellSubscribeIndex(v.project.Slices)
	results := v.adv06ContractToSlice(cellSubscribes)
	results = append(results, v.adv06SliceToContract()...)
	return results
}

// isActiveEvent reports whether the contract is a non-nil active event,
// which is the precondition shared by ADV-05 and ADV-06 for active drift checks.
func isActiveEvent(c *metadata.ContractMeta) bool {
	return c != nil &&
		cell.ContractKind(c.Kind) == cell.ContractEvent &&
		c.Lifecycle == string(cell.LifecycleActive)
}

// buildCellSubscribeIndex maps each cell ID to the set of contract IDs that
// any of its slices declare with role=subscribe. Keeps direction A linear
// instead of O(contracts × slices) per subscriber.
//
// Only used by adv06ContractToSlice (direction A); adv06SliceToContract
// (direction B) iterates slices directly.
func buildCellSubscribeIndex(slices map[string]*metadata.SliceMeta) map[string]map[string]bool {
	idx := make(map[string]map[string]bool, len(slices))
	for _, s := range slices {
		for _, cu := range s.ContractUsages {
			if cell.ContractRole(cu.Role) != cell.RoleSubscribe {
				continue
			}
			set, ok := idx[s.BelongsToCell]
			if !ok {
				set = make(map[string]bool)
				idx[s.BelongsToCell] = set
			}
			set[cu.Contract] = true
		}
	}
	return idx
}

// adv06ContractToSlice flags active event contracts whose endpoints.subscribers
// names a cell that has no matching subscribe contractUsage in any of its slices.
//
// Note: when ADV-05 fires (subscribers is empty), direction A produces no
// findings because there are no cell subscribers to check; direction B still
// runs independently.
func (v *Validator) adv06ContractToSlice(cellSubscribes map[string]map[string]bool) []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if !isActiveEvent(c) {
			continue
		}
		for i, subscriber := range c.Endpoints.Subscribers {
			if _, isCell := v.project.Cells[subscriber]; !isCell {
				continue
			}
			if cellSubscribes[subscriber][c.ID] {
				continue
			}
			results = append(results, v.newResult(
				"ADV-06", SeverityError, IssueMismatch,
				contractFile(c),
				fmt.Sprintf("endpoints.subscribers[%d]", i),
				fmt.Sprintf("event contract %q lists cell %q as subscriber, but no slice in %q declares contractUsage{contract: %q, role: subscribe}; add this contractUsage to a slice in %q (e.g. cells/%s/slices/<slice>/slice.yaml) or remove %q from endpoints.subscribers", c.ID, subscriber, subscriber, c.ID, subscriber, subscriber, subscriber),
			))
		}
	}
	return results
}

// adv06SliceToContract flags subscribe contractUsages whose target contract is
// active and exists, but its endpoints.subscribers does not list the slice's cell.
func (v *Validator) adv06SliceToContract() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		for i, cu := range s.ContractUsages {
			if cell.ContractRole(cu.Role) != cell.RoleSubscribe {
				continue
			}
			c := v.project.Contracts[cu.Contract]
			if !isActiveEvent(c) {
				continue
			}
			if containsString(c.Endpoints.Subscribers, s.BelongsToCell) {
				continue
			}
			results = append(results, v.newResult(
				"ADV-06", SeverityError, IssueMismatch,
				sliceFile(s),
				fmt.Sprintf("contractUsages[%d].contract", i),
				fmt.Sprintf("slice %q declares contractUsage{contract: %q, role: subscribe}, but the contract's endpoints.subscribers does not list cell %q; add %q to the contract's endpoints.subscribers or remove the subscribe contractUsage from this slice", s.ID, cu.Contract, s.BelongsToCell, s.BelongsToCell),
			))
		}
	}
	return results
}
