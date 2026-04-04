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
			results = append(results, ValidationResult{
				Code:      "ADV-01",
				Severity:  SeverityWarning,
				IssueType: IssueRefNotFound,
				File:      journeyFile(j.ID),
				Field:     "id",
				Message:   fmt.Sprintf("journey %q has no entry in status-board.yaml", j.ID),
			})
		}
	}
	return results
}

// validateADV02 checks that deprecated contracts still referenced by slices produce a warning.
func (v *Validator) validateADV02() []ValidationResult {
	var results []ValidationResult

	// Build a set of deprecated contract IDs.
	deprecated := make(map[string]bool)
	for _, c := range v.project.Contracts {
		if c.Lifecycle == "deprecated" {
			deprecated[c.ID] = true
		}
	}
	if len(deprecated) == 0 {
		return nil
	}

	for key, s := range v.project.Slices {
		for i, cu := range s.ContractUsages {
			if deprecated[cu.Contract] {
				results = append(results, ValidationResult{
					Code:      "ADV-02",
					Severity:  SeverityWarning,
					IssueType: IssueForbidden,
					File:      sliceFile(key),
					Field:     fmt.Sprintf("contractUsages[%d].contract", i),
					Message:   fmt.Sprintf("slice %q uses deprecated contract %q", s.ID, cu.Contract),
				})
			}
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
				results = append(results, ValidationResult{
					Code:      "ADV-03",
					Severity:  SeverityWarning,
					IssueType: IssueRefNotFound,
					File:      sliceFile(key),
					Field:     fmt.Sprintf("verify.waivers[%d].contract", i),
					Message:   fmt.Sprintf("waiver for contract %q has no matching contractUsage in slice %q", w.Contract, s.ID),
				})
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
			results = append(results, ValidationResult{
				Code:      "ADV-04",
				Severity:  SeverityWarning,
				IssueType: IssueRefNotFound,
				File:      "journeys/status-board.yaml",
				Field:     fmt.Sprintf("entries[%d].journeyId", i),
				Message:   fmt.Sprintf("status-board entry references unknown journey %q", entry.JourneyID),
			})
		}
	}
	return results
}
