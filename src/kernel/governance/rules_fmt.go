package governance

import (
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/kernel/cell"
)

// validateFMT01 checks that contract.lifecycle is one of {draft, active, deprecated}.
func (v *Validator) validateFMT01() []ValidationResult {
	var results []ValidationResult
	validLifecycles := map[string]bool{
		string(cell.LifecycleDraft):      true,
		string(cell.LifecycleActive):     true,
		string(cell.LifecycleDeprecated): true,
	}
	for _, c := range v.project.Contracts {
		if !validLifecycles[c.Lifecycle] {
			results = append(results, ValidationResult{
				Code:      "FMT-01",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      contractFile(c.ID),
				Field:     "lifecycle",
				Message:   fmt.Sprintf("contract %q lifecycle %q is not valid (must be draft, active, or deprecated)", c.ID, c.Lifecycle),
			})
		}
	}
	return results
}

// validateFMT02 checks that cell.type is one of {core, edge, support}.
func (v *Validator) validateFMT02() []ValidationResult {
	var results []ValidationResult
	validTypes := map[string]bool{
		string(cell.CellTypeCore):    true,
		string(cell.CellTypeEdge):    true,
		string(cell.CellTypeSupport): true,
	}
	for _, c := range v.project.Cells {
		if !validTypes[c.Type] {
			results = append(results, ValidationResult{
				Code:      "FMT-02",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      cellFile(c.ID),
				Field:     "type",
				Message:   fmt.Sprintf("cell %q type %q is not valid (must be core, edge, or support)", c.ID, c.Type),
			})
		}
	}
	return results
}

// validateFMT03 checks that consistencyLevel is valid (L0-L4) for both cells and contracts.
func (v *Validator) validateFMT03() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		if _, err := cell.ParseLevel(c.ConsistencyLevel); err != nil {
			results = append(results, ValidationResult{
				Code:      "FMT-03",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      cellFile(c.ID),
				Field:     "consistencyLevel",
				Message:   fmt.Sprintf("cell %q consistencyLevel %q is not valid (must be L0-L4)", c.ID, c.ConsistencyLevel),
			})
		}
	}
	for _, c := range v.project.Contracts {
		if _, err := cell.ParseLevel(c.ConsistencyLevel); err != nil {
			results = append(results, ValidationResult{
				Code:      "FMT-03",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      contractFile(c.ID),
				Field:     "consistencyLevel",
				Message:   fmt.Sprintf("contract %q consistencyLevel %q is not valid (must be L0-L4)", c.ID, c.ConsistencyLevel),
			})
		}
	}
	return results
}

// validateFMT04 checks that event-type contracts include replayable, idempotencyKey, deliverySemantics.
func (v *Validator) validateFMT04() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if cell.ContractKind(c.Kind) != cell.ContractEvent {
			continue
		}
		if c.Replayable == nil {
			results = append(results, ValidationResult{
				Code:      "FMT-04",
				Severity:  SeverityError,
				IssueType: IssueRequired,
				File:      contractFile(c.ID),
				Field:     "replayable",
				Message:   fmt.Sprintf("event contract %q must specify replayable", c.ID),
			})
		}
		if c.IdempotencyKey == "" {
			results = append(results, ValidationResult{
				Code:      "FMT-04",
				Severity:  SeverityError,
				IssueType: IssueRequired,
				File:      contractFile(c.ID),
				Field:     "idempotencyKey",
				Message:   fmt.Sprintf("event contract %q must specify idempotencyKey", c.ID),
			})
		}
		if c.DeliverySemantics == "" {
			results = append(results, ValidationResult{
				Code:      "FMT-04",
				Severity:  SeverityError,
				IssueType: IssueRequired,
				File:      contractFile(c.ID),
				Field:     "deliverySemantics",
				Message:   fmt.Sprintf("event contract %q must specify deliverySemantics", c.ID),
			})
		}
	}
	return results
}

// validateFMT05 checks that contractUsages[].role is one of the 8 valid roles.
func (v *Validator) validateFMT05() []ValidationResult {
	var results []ValidationResult
	validRoles := map[string]bool{
		string(cell.RoleServe):     true,
		string(cell.RoleCall):      true,
		string(cell.RolePublish):   true,
		string(cell.RoleSubscribe): true,
		string(cell.RoleHandle):    true,
		string(cell.RoleInvoke):    true,
		string(cell.RoleProvide):   true,
		string(cell.RoleRead):      true,
	}
	for key, s := range v.project.Slices {
		for i, cu := range s.ContractUsages {
			if !validRoles[cu.Role] {
				results = append(results, ValidationResult{
					Code:      "FMT-05",
					Severity:  SeverityError,
					IssueType: IssueInvalid,
					File:      sliceFile(key),
					Field:     fmt.Sprintf("contractUsages[%d].role", i),
					Message:   fmt.Sprintf("role %q is not a valid contract role", cu.Role),
				})
			}
		}
	}
	return results
}

// validateFMT06 checks that non-L0 cells must have schema.primary.
func (v *Validator) validateFMT06() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		level, err := cell.ParseLevel(c.ConsistencyLevel)
		if err != nil {
			continue // FMT-03 covers invalid levels
		}
		if level != cell.L0 && c.Schema.Primary == "" {
			results = append(results, ValidationResult{
				Code:      "FMT-06",
				Severity:  SeverityError,
				IssueType: IssueRequired,
				File:      cellFile(c.ID),
				Field:     "schema.primary",
				Message:   fmt.Sprintf("non-L0 cell %q must have schema.primary", c.ID),
			})
		}
	}
	return results
}

// validateFMT07 checks that the contract provider endpoint is populated based on kind.
func (v *Validator) validateFMT07() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		provider := contractProvider(c)
		if provider == "" {
			var field string
			switch cell.ContractKind(c.Kind) {
			case cell.ContractHTTP:
				field = "endpoints.server"
			case cell.ContractEvent:
				field = "endpoints.publisher"
			case cell.ContractCommand:
				field = "endpoints.handler"
			case cell.ContractProjection:
				field = "endpoints.provider"
			default:
				field = "endpoints"
			}
			results = append(results, ValidationResult{
				Code:      "FMT-07",
				Severity:  SeverityError,
				IssueType: IssueRequired,
				File:      contractFile(c.ID),
				Field:     field,
				Message:   fmt.Sprintf("contract %q (kind %q) must have a provider endpoint", c.ID, c.Kind),
			})
		}
	}
	return results
}

// validateFMT09 checks that contract.kind is one of {http, event, command, projection}.
func (v *Validator) validateFMT09() []ValidationResult {
	var results []ValidationResult
	validKinds := map[string]bool{
		string(cell.ContractHTTP):       true,
		string(cell.ContractEvent):      true,
		string(cell.ContractCommand):    true,
		string(cell.ContractProjection): true,
	}
	for _, c := range v.project.Contracts {
		if !validKinds[c.Kind] {
			results = append(results, ValidationResult{
				Code:      "FMT-09",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      contractFile(c.ID),
				Field:     "kind",
				Message:   fmt.Sprintf("contract %q kind %q is not valid (must be http, event, command, or projection)", c.ID, c.Kind),
			})
		}
	}
	return results
}

// validateFMT08 checks that the first segment of a contract ID matches the contract's kind.
// Contract ID format: "{kind}.{domain}.{version}"; the prefix before the first "." should equal kind.
func (v *Validator) validateFMT08() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		parts := strings.SplitN(c.ID, ".", 2)
		if len(parts) < 2 {
			results = append(results, ValidationResult{
				Code:      "FMT-08",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      contractFile(c.ID),
				Field:     "id",
				Message:   fmt.Sprintf("contract ID %q format is invalid (missing '.' separator)", c.ID),
			})
			continue
		}
		prefix := parts[0]
		if prefix != c.Kind {
			results = append(results, ValidationResult{
				Code:      "FMT-08",
				Severity:  SeverityError,
				IssueType: IssueMismatch,
				File:      contractFile(c.ID),
				Field:     "kind",
				Message:   fmt.Sprintf("contract %q ID prefix %q does not match kind %q", c.ID, prefix, c.Kind),
			})
		}
	}
	return results
}
