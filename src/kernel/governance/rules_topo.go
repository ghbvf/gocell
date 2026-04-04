package governance

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// contractProvider returns the provider cell/actor for a contract based on its kind.
func contractProvider(c *metadata.ContractMeta) string {
	switch cell.ContractKind(c.Kind) {
	case cell.ContractHTTP:
		return c.Endpoints.Server
	case cell.ContractEvent:
		return c.Endpoints.Publisher
	case cell.ContractCommand:
		return c.Endpoints.Handler
	case cell.ContractProjection:
		return c.Endpoints.Provider
	default:
		return ""
	}
}

// contractConsumers returns the consumer cell/actor list for a contract based on its kind.
func contractConsumers(c *metadata.ContractMeta) []string {
	switch cell.ContractKind(c.Kind) {
	case cell.ContractHTTP:
		return c.Endpoints.Clients
	case cell.ContractEvent:
		return c.Endpoints.Subscribers
	case cell.ContractCommand:
		return c.Endpoints.Invokers
	case cell.ContractProjection:
		return c.Endpoints.Readers
	default:
		return nil
	}
}

// validateTOPO01 checks that contractUsages[].role is valid for the contract's kind.
func (v *Validator) validateTOPO01() []ValidationResult {
	var results []ValidationResult
	for key, s := range v.project.Slices {
		for i, cu := range s.ContractUsages {
			c, ok := v.project.Contracts[cu.Contract]
			if !ok {
				continue // REF-02 covers missing contracts
			}
			validRoles := cell.ValidRolesForKind(cell.ContractKind(c.Kind))
			if !containsRole(validRoles, cell.ContractRole(cu.Role)) {
				results = append(results, ValidationResult{
					Code:      "TOPO-01",
					Severity:  SeverityError,
					IssueType: IssueInvalid,
					File:      sliceFile(key),
					Field:     fmt.Sprintf("contractUsages[%d].role", i),
					Message:   fmt.Sprintf("role %q is not valid for contract kind %q (contract %q)", cu.Role, c.Kind, cu.Contract),
				})
			}
		}
	}
	return results
}

// validateTOPO02 checks that a provider-role slice's belongsToCell matches the contract's provider.
func (v *Validator) validateTOPO02() []ValidationResult {
	var results []ValidationResult
	for key, s := range v.project.Slices {
		for i, cu := range s.ContractUsages {
			if !cell.IsProviderRole(cell.ContractRole(cu.Role)) {
				continue
			}
			c, ok := v.project.Contracts[cu.Contract]
			if !ok {
				continue
			}
			provider := contractProvider(c)
			if provider != "" && s.BelongsToCell != provider {
				results = append(results, ValidationResult{
					Code:      "TOPO-02",
					Severity:  SeverityError,
					IssueType: IssueMismatch,
					File:      sliceFile(key),
					Field:     fmt.Sprintf("contractUsages[%d].role", i),
					Message: fmt.Sprintf(
						"slice %q (cell %q) has provider role %q but contract %q provider is %q",
						s.ID, s.BelongsToCell, cu.Role, cu.Contract, provider,
					),
				})
			}
		}
	}
	return results
}

// validateTOPO03 checks that a consumer-role slice's belongsToCell is in the contract's consumers.
func (v *Validator) validateTOPO03() []ValidationResult {
	var results []ValidationResult
	for key, s := range v.project.Slices {
		for i, cu := range s.ContractUsages {
			if !cell.IsConsumerRole(cell.ContractRole(cu.Role)) {
				continue
			}
			c, ok := v.project.Contracts[cu.Contract]
			if !ok {
				continue
			}
			consumers := contractConsumers(c)
			if len(consumers) > 0 && !containsString(consumers, s.BelongsToCell) {
				results = append(results, ValidationResult{
					Code:      "TOPO-03",
					Severity:  SeverityError,
					IssueType: IssueMismatch,
					File:      sliceFile(key),
					Field:     fmt.Sprintf("contractUsages[%d].role", i),
					Message: fmt.Sprintf(
						"slice %q (cell %q) has consumer role %q but is not in contract %q consumers %v",
						s.ID, s.BelongsToCell, cu.Role, cu.Contract, consumers,
					),
				})
			}
		}
	}
	return results
}

// validateTOPO04 checks that contract.consistencyLevel does not exceed ownerCell's consistencyLevel.
func (v *Validator) validateTOPO04() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		ownerCell, ok := v.project.Cells[c.OwnerCell]
		if !ok {
			continue // REF-03 covers missing ownerCell
		}
		contractLevel, err := cell.ParseLevel(c.ConsistencyLevel)
		if err != nil {
			continue // FMT-03 covers invalid levels
		}
		cellLevel, err := cell.ParseLevel(ownerCell.ConsistencyLevel)
		if err != nil {
			continue
		}
		if contractLevel > cellLevel {
			results = append(results, ValidationResult{
				Code:      "TOPO-04",
				Severity:  SeverityError,
				IssueType: IssueMismatch,
				File:      contractFile(c.ID),
				Field:     "consistencyLevel",
				Message: fmt.Sprintf(
					"contract %q consistencyLevel %s exceeds ownerCell %q level %s",
					c.ID, c.ConsistencyLevel, c.OwnerCell, ownerCell.ConsistencyLevel,
				),
			})
		}
	}
	return results
}

// validateTOPO05 checks that L0 cells do not appear in any contract's endpoints.
func (v *Validator) validateTOPO05() []ValidationResult {
	var results []ValidationResult

	// Build set of L0 cells.
	l0Cells := make(map[string]bool)
	for _, c := range v.project.Cells {
		if c.ConsistencyLevel == "L0" {
			l0Cells[c.ID] = true
		}
	}
	if len(l0Cells) == 0 {
		return nil
	}

	for _, ct := range v.project.Contracts {
		provider := contractProvider(ct)
		if l0Cells[provider] {
			results = append(results, ValidationResult{
				Code:      "TOPO-05",
				Severity:  SeverityError,
				IssueType: IssueForbidden,
				File:      contractFile(ct.ID),
				Field:     "endpoints",
				Message:   fmt.Sprintf("L0 cell %q must not appear as provider in contract %q", provider, ct.ID),
			})
		}
		for _, consumer := range contractConsumers(ct) {
			if l0Cells[consumer] {
				results = append(results, ValidationResult{
					Code:      "TOPO-05",
					Severity:  SeverityError,
					IssueType: IssueForbidden,
					File:      contractFile(ct.ID),
					Field:     "endpoints",
					Message:   fmt.Sprintf("L0 cell %q must not appear as consumer in contract %q", consumer, ct.ID),
				})
			}
		}
	}
	return results
}

// validateTOPO06 checks that each cell belongs to at most one assembly.
func (v *Validator) validateTOPO06() []ValidationResult {
	var results []ValidationResult
	cellAssembly := make(map[string]string) // cellID -> assemblyID
	for _, a := range v.project.Assemblies {
		for i, cellRef := range a.Cells {
			if existing, ok := cellAssembly[cellRef]; ok {
				results = append(results, ValidationResult{
					Code:      "TOPO-06",
					Severity:  SeverityError,
					IssueType: IssueDuplicate,
					File:      assemblyFile(a.ID),
					Field:     fmt.Sprintf("cells[%d]", i),
					Message: fmt.Sprintf(
						"cell %q is already assigned to assembly %q, cannot also be in %q",
						cellRef, existing, a.ID,
					),
				})
			} else {
				cellAssembly[cellRef] = a.ID
			}
		}
	}
	return results
}

// --- helpers ---

func containsRole(roles []cell.ContractRole, target cell.ContractRole) bool {
	for _, r := range roles {
		if r == target {
			return true
		}
	}
	return false
}

func containsString(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}
