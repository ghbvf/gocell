package governance

import (
	"fmt"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
)

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
			// Only non-empty, parseable, and not-yet-expired waivers are valid.
			if w.ExpiresAt == "" {
				continue // missing expiresAt, invalid waiver
			}
			t, err := time.Parse("2006-01-02", w.ExpiresAt)
			if err != nil {
				continue // unparseable expiresAt, invalid waiver
			}
			if t.Before(v.now().UTC().Truncate(24 * time.Hour)) {
				continue // expired waiver, not valid
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

// validateVERIFY02 checks waiver required fields and expiry.
// Required: contract, owner, reason, expiresAt (valid date, not expired).
func (v *Validator) validateVERIFY02() []ValidationResult {
	var results []ValidationResult
	for key, s := range v.project.Slices {
		for i, w := range s.Verify.Waivers {
			if w.Contract == "" {
				results = append(results, ValidationResult{
					Code:      "VERIFY-02",
					Severity:  SeverityError,
					IssueType: IssueRequired,
					File:      sliceFile(key),
					Field:     fmt.Sprintf("verify.waivers[%d].contract", i),
					Message:   "waiver.contract is required",
				})
			}
			if w.Owner == "" {
				results = append(results, ValidationResult{
					Code:      "VERIFY-02",
					Severity:  SeverityError,
					IssueType: IssueRequired,
					File:      sliceFile(key),
					Field:     fmt.Sprintf("verify.waivers[%d].owner", i),
					Message:   fmt.Sprintf("waiver.owner is required for contract %q", w.Contract),
				})
			}
			if w.Reason == "" {
				results = append(results, ValidationResult{
					Code:      "VERIFY-02",
					Severity:  SeverityError,
					IssueType: IssueRequired,
					File:      sliceFile(key),
					Field:     fmt.Sprintf("verify.waivers[%d].reason", i),
					Message:   fmt.Sprintf("waiver.reason is required for contract %q", w.Contract),
				})
			}
			if w.ExpiresAt == "" {
				results = append(results, ValidationResult{
					Code:      "VERIFY-02",
					Severity:  SeverityError,
					IssueType: IssueRequired,
					File:      sliceFile(key),
					Field:     fmt.Sprintf("verify.waivers[%d].expiresAt", i),
					Message:   fmt.Sprintf("waiver.expiresAt is required for contract %q", w.Contract),
				})
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
			if t.Before(v.now().UTC().Truncate(24 * time.Hour)) {
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
			targetLevel, parseErr := cell.ParseLevel(target.ConsistencyLevel)
			if parseErr != nil {
				continue // FMT-03 covers invalid levels
			}
			if targetLevel != cell.L0 {
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
