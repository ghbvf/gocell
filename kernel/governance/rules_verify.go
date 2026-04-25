package governance

import (
	"fmt"
	"strings"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// validateVERIFY01 checks that every contractUsage has a matching
// verify.contract entry or a valid waiver.
//
// verify.contract format: "contract.{contractID}.{role}"
// waiver match: waiver.Contract == contractUsage.Contract
func (v *Validator) validateVERIFY01() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		results = append(results, v.validateSliceVERIFY01(s)...)
	}
	return results
}

// validateSliceVERIFY01 checks a single slice's contractUsages against its
// verify.contract entries and active waivers.
func (v *Validator) validateSliceVERIFY01(s *metadata.SliceMeta) []ValidationResult {
	verifySet := make(map[string]bool, len(s.Verify.Contract))
	for _, vc := range s.Verify.Contract {
		verifySet[vc] = true
	}
	waiverSet := v.buildActiveWaiverSet(s)

	var results []ValidationResult
	for i, cu := range s.ContractUsages {
		verifyKey := fmt.Sprintf("contract.%s.%s", cu.Contract, cu.Role)
		if !verifySet[verifyKey] && !waiverSet[cu.Contract] {
			results = append(results, v.newResult(
				"VERIFY-01", SeverityError, IssueRequired,
				sliceFile(s),
				fmt.Sprintf("contractUsages[%d]", i),
				fmt.Sprintf(
					"usage of contract %q (role %q) in slice %q has no verify.contract entry or valid waiver",
					cu.Contract, cu.Role, s.ID,
				),
			))
		}
	}
	return results
}

// buildActiveWaiverSet returns the set of contract IDs covered by a
// non-expired, parseable waiver in the slice.
func (v *Validator) buildActiveWaiverSet(s *metadata.SliceMeta) map[string]bool {
	waiverSet := make(map[string]bool, len(s.Verify.Waivers))
	for _, w := range s.Verify.Waivers {
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
	return waiverSet
}

// validateVERIFY02 checks waiver required fields and expiry.
// Required: contract, owner, reason, expiresAt (valid date, not expired).
func (v *Validator) validateVERIFY02() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		for i, w := range s.Verify.Waivers {
			results = append(results, v.validateWaiverVERIFY02(sliceFile(s), i, w)...)
		}
	}
	return results
}

// validateWaiverVERIFY02 validates a single waiver's required fields and expiry.
func (v *Validator) validateWaiverVERIFY02(file string, i int, w metadata.WaiverMeta) []ValidationResult {
	var results []ValidationResult
	if w.Contract == "" {
		results = append(results, v.newResult(
			"VERIFY-02", SeverityError, IssueRequired,
			file,
			fmt.Sprintf("verify.waivers[%d].contract", i),
			"waiver.contract is required",
		))
	}
	if w.Owner == "" {
		results = append(results, v.newResult(
			"VERIFY-02", SeverityError, IssueRequired,
			file,
			fmt.Sprintf("verify.waivers[%d].owner", i),
			fmt.Sprintf("waiver.owner is required for contract %q", w.Contract),
		))
	}
	if w.Reason == "" {
		results = append(results, v.newResult(
			"VERIFY-02", SeverityError, IssueRequired,
			file,
			fmt.Sprintf("verify.waivers[%d].reason", i),
			fmt.Sprintf("waiver.reason is required for contract %q", w.Contract),
		))
	}
	if w.ExpiresAt == "" {
		results = append(results, v.newResult(
			"VERIFY-02", SeverityError, IssueRequired,
			file,
			fmt.Sprintf("verify.waivers[%d].expiresAt", i),
			fmt.Sprintf("waiver.expiresAt is required for contract %q", w.Contract),
		))
		return results
	}
	t, err := time.Parse("2006-01-02", w.ExpiresAt)
	if err != nil {
		results = append(results, v.newResult(
			"VERIFY-02", SeverityError, IssueInvalid,
			file,
			fmt.Sprintf("verify.waivers[%d].expiresAt", i),
			fmt.Sprintf("waiver expiresAt %q is not a valid date (expected YYYY-MM-DD)", w.ExpiresAt),
		))
		return results
	}
	if t.Before(v.now().UTC().Truncate(24 * time.Hour)) {
		results = append(results, v.newResult(
			"VERIFY-02", SeverityError, IssueInvalid,
			file,
			fmt.Sprintf("verify.waivers[%d].expiresAt", i),
			fmt.Sprintf("waiver for contract %q expired on %s", w.Contract, w.ExpiresAt),
		))
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
				results = append(results, v.newResult(
					"VERIFY-03", SeverityError, IssueMismatch,
					cellFile(c),
					fmt.Sprintf("l0Dependencies[%d].cell", i),
					fmt.Sprintf(
						"cell %q declares l0Dependency on %q but target has consistencyLevel %s (expected L0)",
						c.ID, dep.Cell, target.ConsistencyLevel,
					),
				))
			}
		}
	}
	return results
}

// validateVERIFY04 checks that every active contract whose provider is a
// Cell has at least one provider-role slice. Without this, a contract is
// "published but nobody provides it" — a ghost capability.
// Contracts served by external actors are skipped (actors have no slices).
func (v *Validator) validateVERIFY04() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Lifecycle != "active" {
			continue
		}
		providerID := contractProvider(c)
		if providerID == "" {
			continue // REF rules cover missing provider
		}
		// Only check cell-backed contracts; external actors have no slices.
		if _, isCell := v.project.Cells[providerID]; !isCell {
			continue
		}
		if !v.hasProviderSlice(c.ID, providerID) {
			results = append(results, v.newResult(
				"VERIFY-04", SeverityError, IssueRequired,
				contractFile(c),
				"lifecycle",
				fmt.Sprintf(
					"active contract %q has no provider-role slice in cell %q",
					c.ID, providerID,
				),
			))
		}
	}
	return results
}

// hasProviderSlice returns true if any slice belonging to providerCell declares
// a provider-role contractUsage for the given contract ID.
func (v *Validator) hasProviderSlice(contractID, providerCell string) bool {
	for _, s := range v.project.Slices {
		if s.BelongsToCell != providerCell {
			continue
		}
		for _, cu := range s.ContractUsages {
			if cu.Contract == contractID && cell.IsProviderRole(cell.ContractRole(cu.Role)) {
				return true
			}
		}
	}
	return false
}

// validRefPrefixes is the set of allowed first segments in a verify ref.
var validRefPrefixes = map[string]bool{
	"journey":  true,
	"smoke":    true,
	"unit":     true,
	"contract": true,
}

// validateVerifyRef checks a single ref string for format compliance.
// Rules: at least 3 dot-separated segments; first segment must be a known prefix.
// For smoke refs, second segment must be a cellID present in the project.
func (v *Validator) validateVerifyRef(ref, file, field string) []ValidationResult {
	var results []ValidationResult
	parts := strings.SplitN(ref, ".", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		results = append(results, v.newResult(
			"VERIFY-05", SeverityError, IssueInvalid,
			file,
			field,
			fmt.Sprintf(
				"ref %q must have at least 3 non-empty dot-separated segments", ref,
			),
		))
		return results
	}

	prefix := parts[0]
	if !validRefPrefixes[prefix] {
		results = append(results, v.newResult(
			"VERIFY-05", SeverityError, IssueInvalid,
			file,
			field,
			fmt.Sprintf(
				"ref %q has unknown prefix %q; expected journey, smoke, unit, or contract", ref, prefix,
			),
		))
		return results
	}

	// For smoke refs, the second segment must be an existing cellID.
	if prefix == "smoke" {
		cellID := parts[1]
		if _, ok := v.project.Cells[cellID]; !ok {
			results = append(results, v.newResult(
				"VERIFY-05", SeverityError, IssueRefNotFound,
				file,
				field,
				fmt.Sprintf(
					"smoke ref %q references non-existent cell %q", ref, cellID,
				),
			))
		}
	}

	return results
}

// validateVERIFY05 checks that all verify refs (cell.verify.smoke,
// slice.verify.unit, slice.verify.contract, journey.passCriteria[].checkRef)
// use the structured ref format: {prefix}.{scope}.{suffix}, where prefix is
// one of journey/smoke/unit/contract. For smoke refs the scope must be an
// existing cellID.
func (v *Validator) validateVERIFY05() []ValidationResult {
	var results []ValidationResult

	// cell.yaml verify.smoke refs
	for _, c := range v.project.Cells {
		file := cellFile(c)
		for i, ref := range c.Verify.Smoke {
			field := fmt.Sprintf("verify.smoke[%d]", i)
			results = append(results, v.validateVerifyRef(ref, file, field)...)
		}
	}

	// slice.yaml verify.unit + verify.contract refs
	for _, s := range v.project.Slices {
		file := sliceFile(s)
		for i, ref := range s.Verify.Unit {
			field := fmt.Sprintf("verify.unit[%d]", i)
			results = append(results, v.validateVerifyRef(ref, file, field)...)
		}
		for i, ref := range s.Verify.Contract {
			field := fmt.Sprintf("verify.contract[%d]", i)
			results = append(results, v.validateVerifyRef(ref, file, field)...)
		}
	}

	// journey passCriteria[].checkRef
	for _, j := range v.project.Journeys {
		file := journeyFile(j)
		for i, pc := range j.PassCriteria {
			if pc.CheckRef == "" {
				continue
			}
			field := fmt.Sprintf("passCriteria[%d].checkRef", i)
			results = append(results, v.validateVerifyRef(pc.CheckRef, file, field)...)
		}
	}

	return results
}
