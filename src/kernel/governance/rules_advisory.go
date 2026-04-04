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
