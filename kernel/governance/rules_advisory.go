package governance

import "fmt"

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
				journeyFile(j.ID),
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
	for key, s := range v.project.Slices {
		// Build set of contracts used by this slice.
		usedContracts := make(map[string]bool, len(s.ContractUsages))
		for _, cu := range s.ContractUsages {
			usedContracts[cu.Contract] = true
		}
		for i, w := range s.Verify.Waivers {
			if w.Contract != "" && !usedContracts[w.Contract] {
				results = append(results, v.newResult(
					"ADV-03", SeverityWarning, IssueRefNotFound,
					sliceFile(key),
					fmt.Sprintf("verify.waivers[%d].contract", i),
					fmt.Sprintf("waiver for contract %q has no matching contractUsage in slice %q", w.Contract, s.ID),
				))
			}
		}
	}
	return results
}

// validateADV04 checks that status-board entries reference existing journeys.
func (v *Validator) validateADV04() []ValidationResult {
	var results []ValidationResult
	for i, entry := range v.project.StatusBoard {
		if _, ok := v.project.Journeys[entry.JourneyID]; !ok {
			results = append(results, v.newResult(
				"ADV-04", SeverityWarning, IssueRefNotFound,
				"journeys/status-board.yaml",
				fmt.Sprintf("entries[%d].journeyId", i),
				fmt.Sprintf("status-board entry references unknown journey %q", entry.JourneyID),
			))
		}
	}
	return results
}
