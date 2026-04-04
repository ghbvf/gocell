package governance

import (
	"fmt"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
)

// nowFunc is the function used to get the current time. It can be overridden in tests.
var nowFunc = time.Now

// validateVERIFY01 checks that every provider-role contractUsage has a matching
// verify.contract entry or a valid waiver.
//
// verify.contract format: "contract.{contractID}.{role}"
// waiver match: waiver.Contract == contractUsage.Contract
func (v *Validator) validateVERIFY01() []ValidationResult {
	var results []ValidationResult
	for key, s := range v.project.Slices {
		// Build lookup sets for verify.contract entries and waivers.
		verifySet := make(map[string]bool)
		for _, vc := range s.Verify.Contract {
			verifySet[vc] = true
		}
		waiverSet := make(map[string]bool)
		for _, w := range s.Verify.Waivers {
			// Check expiry before considering the waiver valid.
			if w.ExpiresAt != "" {
				t, err := time.Parse("2006-01-02", w.ExpiresAt)
				if err == nil && t.Before(nowFunc().Truncate(24*time.Hour)) {
					continue // expired waiver, not valid
				}
			}
			waiverSet[w.Contract] = true
		}

		for i, cu := range s.ContractUsages {
			if !cell.IsProviderRole(cell.ContractRole(cu.Role)) {
				continue
			}
			verifyKey := fmt.Sprintf("contract.%s.%s", cu.Contract, cu.Role)
			if !verifySet[verifyKey] && !waiverSet[cu.Contract] {
				results = append(results, ValidationResult{
					Code:      "VERIFY-01",
					Severity:  SeverityError,
					IssueType: IssueRequired,
					File:      sliceFile(key),
					Field:     fmt.Sprintf("contractUsages[%d]", i),
					Message: fmt.Sprintf(
						"provider-role usage of contract %q (role %q) in slice %q has no verify.contract entry or valid waiver",
						cu.Contract, cu.Role, s.ID,
					),
				})
			}
		}
	}
	return results
}

// validateVERIFY02 checks that waiver.expiresAt is not in the past.
func (v *Validator) validateVERIFY02() []ValidationResult {
	var results []ValidationResult
	for key, s := range v.project.Slices {
		for i, w := range s.Verify.Waivers {
			if w.ExpiresAt == "" {
				continue
			}
			t, err := time.Parse("2006-01-02", w.ExpiresAt)
			if err != nil {
				results = append(results, ValidationResult{
					Code:      "VERIFY-02",
					Severity:  SeverityError,
					IssueType: IssueInvalid,
					File:      sliceFile(key),
					Field:     fmt.Sprintf("verify.waivers[%d].expiresAt", i),
					Message:   fmt.Sprintf("waiver expiresAt %q is not a valid date (expected YYYY-MM-DD)", w.ExpiresAt),
				})
				continue
			}
			if t.Before(nowFunc().Truncate(24 * time.Hour)) {
				results = append(results, ValidationResult{
					Code:      "VERIFY-02",
					Severity:  SeverityError,
					IssueType: IssueInvalid,
					File:      sliceFile(key),
					Field:     fmt.Sprintf("verify.waivers[%d].expiresAt", i),
					Message:   fmt.Sprintf("waiver for contract %q expired on %s", w.Contract, w.ExpiresAt),
				})
			}
		}
	}
	return results
}

// validateVERIFY03 checks that l0Dependencies[].cell targets an L0-level cell.
func (v *Validator) validateVERIFY03() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		for i, dep := range c.L0Dependencies {
			target, ok := v.project.Cells[dep.Cell]
			if !ok {
				continue // REF-09 covers missing cells
			}
			if target.ConsistencyLevel != "L0" {
				results = append(results, ValidationResult{
					Code:      "VERIFY-03",
					Severity:  SeverityError,
					IssueType: IssueMismatch,
					File:      cellFile(c.ID),
					Field:     fmt.Sprintf("l0Dependencies[%d].cell", i),
					Message: fmt.Sprintf(
						"cell %q declares l0Dependency on %q but target has consistencyLevel %s (expected L0)",
						c.ID, dep.Cell, target.ConsistencyLevel,
					),
				})
			}
		}
	}
	return results
}
