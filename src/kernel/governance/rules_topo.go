package governance

import (
	"fmt"
	"sort"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/metadata"
)

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
			if len(consumers) > 0 && !containsString(consumers, "*") && !containsString(consumers, s.BelongsToCell) {
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

// validateTOPO04 checks that contract.consistencyLevel does not exceed the
// actual provider's consistencyLevel. The provider is determined from
// endpoints (not ownerCell, which is a governance field that may differ).
func (v *Validator) validateTOPO04() []ValidationResult {
	// Build actor lookup for external providers.
	actorMaxLevel := make(map[string]cell.Level)
	actorMalformed := make(map[string]string) // ID -> raw invalid value
	for _, a := range v.project.Actors {
		if a.MaxConsistencyLevel == "" {
			continue // no constraint declared — unconstrained
		}
		lvl, err := cell.ParseLevel(a.MaxConsistencyLevel)
		if err != nil {
			actorMalformed[a.ID] = a.MaxConsistencyLevel
			continue
		}
		actorMaxLevel[a.ID] = lvl
	}

	var results []ValidationResult
	for _, c := range v.project.Contracts {
		contractLevel, err := cell.ParseLevel(c.ConsistencyLevel)
		if err != nil {
			continue // FMT-03 covers invalid levels
		}

		providerID := contractProvider(c)
		if providerID == "" {
			continue // REF covers missing provider
		}

		// Check if provider is a Cell.
		if providerCell, ok := v.project.Cells[providerID]; ok {
			providerLevel, err := cell.ParseLevel(providerCell.ConsistencyLevel)
			if err != nil {
				continue
			}
			if contractLevel > providerLevel {
				results = append(results, ValidationResult{
					Code:      "TOPO-04",
					Severity:  SeverityError,
					IssueType: IssueMismatch,
					File:      contractFile(c.ID),
					Field:     "consistencyLevel",
					Message: fmt.Sprintf(
						"contract %q consistencyLevel %s exceeds provider cell %q level %s",
						c.ID, c.ConsistencyLevel, providerID, providerCell.ConsistencyLevel,
					),
				})
			}
			continue
		}

		// Check if provider is an external Actor with malformed level.
		if rawVal, malformed := actorMalformed[providerID]; malformed {
			results = append(results, ValidationResult{
				Code:      "TOPO-04",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      "actors.yaml",
				Field:     "maxConsistencyLevel",
				Message: fmt.Sprintf(
					"cannot verify contract %q consistency: external actor %q has invalid maxConsistencyLevel %q (must be L0-L4)",
					c.ID, providerID, rawVal,
				),
			})
			continue
		}

		// Check if provider is an external Actor with valid level.
		if maxLvl, ok := actorMaxLevel[providerID]; ok {
			if contractLevel > maxLvl {
				results = append(results, ValidationResult{
					Code:      "TOPO-04",
					Severity:  SeverityError,
					IssueType: IssueMismatch,
					File:      contractFile(c.ID),
					Field:     "consistencyLevel",
					Message: fmt.Sprintf(
						"contract %q consistencyLevel %s exceeds external actor %q maxConsistencyLevel %s",
						c.ID, c.ConsistencyLevel, providerID, maxLvl,
					),
				})
			}
		}
		// If provider is neither a Cell nor an Actor, REF rules cover that.
	}
	return results
}

// validateTOPO05 checks that L0 cells do not appear in any contract's endpoints.
func (v *Validator) validateTOPO05() []ValidationResult {
	var results []ValidationResult

	// Build set of L0 cells.
	l0Cells := make(map[string]bool)
	for _, c := range v.project.Cells {
		level, err := cell.ParseLevel(c.ConsistencyLevel)
		if err != nil {
			continue // FMT-03 covers invalid levels
		}
		if level == cell.L0 {
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

// validateTOPO07 checks that contract.consistencyLevel does not exceed the
// maxConsistencyLevel of any external actor referenced as a consumer in the
// contract's endpoints. TOPO-04 covers the provider side; this rule covers the
// consumer side. If an actor has no maxConsistencyLevel, it is unconstrained.
func (v *Validator) validateTOPO07() []ValidationResult {
	actorMaxLevel, actorMalformed := v.buildActorConsumerLookup()
	if len(actorMaxLevel) == 0 && len(actorMalformed) == 0 {
		return nil
	}

	var results []ValidationResult
	for _, c := range v.project.Contracts {
		contractLevel, err := cell.ParseLevel(c.ConsistencyLevel)
		if err != nil {
			continue // FMT-03 covers invalid levels
		}
		results = append(results, v.checkConsumerActors(c, contractLevel, actorMaxLevel, actorMalformed)...)
	}
	return results
}

// buildActorConsumerLookup builds lookup maps for external actor maxConsistencyLevel.
func (v *Validator) buildActorConsumerLookup() (maxLevel map[string]cell.Level, malformed map[string]string) {
	maxLevel = make(map[string]cell.Level)
	malformed = make(map[string]string)
	for _, a := range v.project.Actors {
		if a.MaxConsistencyLevel == "" {
			continue // no constraint declared
		}
		lvl, err := cell.ParseLevel(a.MaxConsistencyLevel)
		if err != nil {
			malformed[a.ID] = a.MaxConsistencyLevel
			continue
		}
		maxLevel[a.ID] = lvl
	}
	return maxLevel, malformed
}

// checkConsumerActors checks each consumer actor of a contract against maxConsistencyLevel constraints.
func (v *Validator) checkConsumerActors(
	c *metadata.ContractMeta, contractLevel cell.Level,
	actorMaxLevel map[string]cell.Level, actorMalformed map[string]string,
) []ValidationResult {
	var results []ValidationResult
	consumers := contractConsumers(c)
	for i, consumerID := range consumers {
		if consumerID == "*" {
			continue
		}
		if _, isCell := v.project.Cells[consumerID]; isCell {
			continue // cells are not constrained by maxConsistencyLevel
		}
		if rawVal, ok := actorMalformed[consumerID]; ok {
			results = append(results, ValidationResult{
				Code:      "TOPO-07",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      "actors.yaml",
				Field:     "maxConsistencyLevel",
				Message: fmt.Sprintf(
					"cannot verify contract %q consistency: external actor %q has invalid maxConsistencyLevel %q (must be L0-L4)",
					c.ID, consumerID, rawVal,
				),
			})
			continue
		}
		if maxLvl, ok := actorMaxLevel[consumerID]; ok && contractLevel > maxLvl {
			results = append(results, ValidationResult{
				Code:      "TOPO-07",
				Severity:  SeverityError,
				IssueType: IssueMismatch,
				File:      contractFile(c.ID),
				Field:     fmt.Sprintf("endpoints.%s[%d]", consumerFieldName(c.Kind), i),
				Message: fmt.Sprintf(
					"contract %q consistencyLevel %s exceeds consumer actor %q maxConsistencyLevel %s",
					c.ID, c.ConsistencyLevel, consumerID, maxLvl,
				),
			})
		}
	}
	return results
}

// validateTOPO08 checks that no slice references a deprecated contract.
// A deprecated contract's lifecycle signals it should no longer be consumed;
// any slice still using it via contractUsages is a blocking error.
func (v *Validator) validateTOPO08() []ValidationResult {
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
				ownerCell := ""
				if c, ok := v.project.Contracts[cu.Contract]; ok {
					ownerCell = c.OwnerCell
				}
				results = append(results, ValidationResult{
					Code:      "TOPO-08",
					Severity:  SeverityError,
					IssueType: IssueForbidden,
					File:      sliceFile(key),
					Field:     fmt.Sprintf("contractUsages[%d].contract", i),
					Message:   fmt.Sprintf("slice %q references deprecated contract %q (ownerCell: %q); check the contract description or contact the ownerCell team for the replacement", s.ID, cu.Contract, ownerCell),
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

	// Sort assembly keys for deterministic error output.
	assemblyKeys := make([]string, 0, len(v.project.Assemblies))
	for k := range v.project.Assemblies {
		assemblyKeys = append(assemblyKeys, k)
	}
	sort.Strings(assemblyKeys)

	for _, key := range assemblyKeys {
		a := v.project.Assemblies[key]
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

