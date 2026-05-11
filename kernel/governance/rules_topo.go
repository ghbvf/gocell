package governance

import (
	"fmt"
	"sort"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// validateTOPO01 checks that contractUsages[].role is valid for the contract's kind.
func (v *Validator) validateTOPO01() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		for i, cu := range s.ContractUsages {
			c, ok := v.project.Contracts[cu.Contract]
			if !ok {
				continue // REF-02 covers missing contracts
			}
			validRoles := cellvocab.ValidRolesForKind(cellvocab.ContractKind(c.Kind))
			if !containsRole(validRoles, cellvocab.ContractRole(cu.Role)) {
				results = append(results, v.newResult(
					"TOPO-01", SeverityError, IssueInvalid,
					sliceFile(s),
					fmt.Sprintf("contractUsages[%d].role", i),
					fmt.Sprintf("role %q is not valid for contract kind %q (contract %q)", cu.Role, c.Kind, cu.Contract),
				))
			}
		}
	}
	return results
}

// validateTOPO02 checks that a provider-role slice's belongsToCell matches the contract's provider.
func (v *Validator) validateTOPO02() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		for i, cu := range s.ContractUsages {
			if !cellvocab.IsProviderRole(cellvocab.ContractRole(cu.Role)) {
				continue
			}
			c, ok := v.project.Contracts[cu.Contract]
			if !ok {
				continue
			}
			provider := contractProvider(c)
			if provider != "" && s.BelongsToCell != provider {
				results = append(results, v.newResult(
					"TOPO-02", SeverityError, IssueMismatch,
					sliceFile(s),
					fmt.Sprintf("contractUsages[%d].role", i),
					fmt.Sprintf(
						"slice %q (cell %q) has provider role %q but contract %q provider is %q",
						s.ID, s.BelongsToCell, cu.Role, cu.Contract, provider,
					),
				))
			}
		}
	}
	return results
}

// validateTOPO03 checks that a consumer-role slice's belongsToCell is in the contract's consumers.
func (v *Validator) validateTOPO03() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		for i, cu := range s.ContractUsages {
			if !cellvocab.IsConsumerRole(cellvocab.ContractRole(cu.Role)) {
				continue
			}
			c, ok := v.project.Contracts[cu.Contract]
			if !ok {
				continue
			}
			consumers := contractConsumers(c)
			if len(consumers) > 0 && !cellMatchesConsumer(consumers, s.BelongsToCell) {
				results = append(results, v.newResult(
					"TOPO-03", SeverityError, IssueMismatch,
					sliceFile(s),
					fmt.Sprintf("contractUsages[%d].role", i),
					fmt.Sprintf(
						"slice %q (cell %q) has consumer role %q but is not in contract %q consumers %v",
						s.ID, s.BelongsToCell, cu.Role, cu.Contract, consumers,
					),
				))
			}
		}
	}
	return results
}

// validateTOPO04 checks that contract.consistencyLevel does not exceed the
// actual provider's consistencyLevel. The provider is determined from
// endpoints (not ownerCell, which is a governance field that may differ).
func (v *Validator) validateTOPO04() []ValidationResult {
	actorMaxLevel, actorMalformed := v.buildActorLevelMaps()

	var results []ValidationResult
	for _, c := range v.project.Contracts {
		contractLevel, err := cellvocab.ParseLevel(c.ConsistencyLevel)
		if err != nil {
			continue // FMT-03 covers invalid levels
		}
		providerID := contractProvider(c)
		if providerID == "" {
			continue // REF covers missing provider
		}
		results = append(results, v.checkContractProviderLevel(c, contractLevel, providerID, actorMaxLevel, actorMalformed)...)
	}
	return results
}

// buildActorLevelMaps scans all actors and returns two maps:
//   - actorMaxLevel: actor ID → parsed Level (valid entries only)
//   - actorMalformed: actor ID → raw invalid maxConsistencyLevel string
func (v *Validator) buildActorLevelMaps() (actorMaxLevel map[string]cellvocab.Level, actorMalformed map[string]string) {
	actorMaxLevel = make(map[string]cellvocab.Level)
	actorMalformed = make(map[string]string)
	for _, a := range v.project.Actors {
		if a.MaxConsistencyLevel == "" {
			continue // no constraint declared — unconstrained
		}
		lvl, err := cellvocab.ParseLevel(a.MaxConsistencyLevel)
		if err != nil {
			actorMalformed[a.ID] = a.MaxConsistencyLevel
			continue
		}
		actorMaxLevel[a.ID] = lvl
	}
	return actorMaxLevel, actorMalformed
}

// checkContractProviderLevel returns TOPO-04 findings for a single contract,
// considering whether the provider is a Cell, a malformed-level Actor, or a
// valid-level Actor.
func (v *Validator) checkContractProviderLevel(
	c *metadata.ContractMeta,
	contractLevel cellvocab.Level,
	providerID string,
	actorMaxLevel map[string]cellvocab.Level,
	actorMalformed map[string]string,
) []ValidationResult {
	// Check if provider is a Cell.
	if providerCell, ok := v.project.Cells[providerID]; ok {
		providerLevel, err := cellvocab.ParseLevel(providerCell.ConsistencyLevel)
		if err != nil {
			return nil
		}
		if contractLevel > providerLevel {
			return []ValidationResult{v.newResult(
				"TOPO-04", SeverityError, IssueMismatch,
				contractFile(c),
				"consistencyLevel",
				fmt.Sprintf(
					"contract %q consistencyLevel %s exceeds provider cell %q level %s",
					c.ID, c.ConsistencyLevel, providerID, providerCell.ConsistencyLevel,
				),
			)}
		}
		return nil
	}

	// Check if provider is an external Actor with malformed level.
	if rawVal, malformed := actorMalformed[providerID]; malformed {
		return []ValidationResult{v.newResult(
			"TOPO-04", SeverityError, IssueInvalid,
			"actors.yaml",
			actorFieldPath(v.project.Actors, providerID, "maxConsistencyLevel"),
			fmt.Sprintf(
				"cannot verify contract %q consistency: external actor %q has invalid maxConsistencyLevel %q (must be L0-L4)",
				c.ID, providerID, rawVal,
			),
		)}
	}

	// Check if provider is an external Actor with valid level.
	if maxLvl, ok := actorMaxLevel[providerID]; ok && contractLevel > maxLvl {
		return []ValidationResult{v.newResult(
			"TOPO-04", SeverityError, IssueMismatch,
			contractFile(c),
			"consistencyLevel",
			fmt.Sprintf(
				"contract %q consistencyLevel %s exceeds external actor %q maxConsistencyLevel %s",
				c.ID, c.ConsistencyLevel, providerID, maxLvl,
			),
		)}
	}
	// If provider is neither a Cell nor an Actor, REF rules cover that.
	return nil
}

// validateTOPO05 checks that L0 cells do not appear in any contract's endpoints.
func (v *Validator) validateTOPO05() []ValidationResult {
	var results []ValidationResult

	// Build set of L0 cells.
	l0Cells := make(map[string]bool)
	for _, c := range v.project.Cells {
		level, err := cellvocab.ParseLevel(c.ConsistencyLevel)
		if err != nil {
			continue // FMT-03 covers invalid levels
		}
		if level == cellvocab.L0 {
			l0Cells[c.ID] = true
		}
	}
	if len(l0Cells) == 0 {
		return nil
	}

	for _, ct := range v.project.Contracts {
		provider := contractProvider(ct)
		if l0Cells[provider] {
			results = append(results, v.newResult(
				"TOPO-05", SeverityError, IssueForbidden,
				contractFile(ct),
				"endpoints",
				fmt.Sprintf("L0 cell %q must not appear as provider in contract %q", provider, ct.ID),
			))
		}
		for _, consumer := range contractConsumers(ct) {
			if l0Cells[consumer] {
				results = append(results, v.newResult(
					"TOPO-05", SeverityError, IssueForbidden,
					contractFile(ct),
					"endpoints",
					fmt.Sprintf("L0 cell %q must not appear as consumer in contract %q", consumer, ct.ID),
				))
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
		contractLevel, err := cellvocab.ParseLevel(c.ConsistencyLevel)
		if err != nil {
			continue // FMT-03 covers invalid levels
		}
		results = append(results, v.checkConsumerActors(c, contractLevel, actorMaxLevel, actorMalformed)...)
	}
	return results
}

// buildActorConsumerLookup builds lookup maps for external actor maxConsistencyLevel.
func (v *Validator) buildActorConsumerLookup() (maxLevel map[string]cellvocab.Level, malformed map[string]string) {
	maxLevel = make(map[string]cellvocab.Level)
	malformed = make(map[string]string)
	for _, a := range v.project.Actors {
		if a.MaxConsistencyLevel == "" {
			continue // no constraint declared
		}
		lvl, err := cellvocab.ParseLevel(a.MaxConsistencyLevel)
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
	c *metadata.ContractMeta, contractLevel cellvocab.Level,
	actorMaxLevel map[string]cellvocab.Level, actorMalformed map[string]string,
) []ValidationResult {
	var results []ValidationResult
	consumers := contractConsumers(c)
	for i, consumerID := range consumers {
		if isWildcardConsumer(consumerID) {
			continue
		}
		if _, isCell := v.project.Cells[consumerID]; isCell {
			continue // cells are not constrained by maxConsistencyLevel
		}
		if rawVal, ok := actorMalformed[consumerID]; ok {
			results = append(results, v.newResult(
				"TOPO-07", SeverityError, IssueInvalid,
				"actors.yaml",
				actorFieldPath(v.project.Actors, consumerID, "maxConsistencyLevel"),
				fmt.Sprintf(
					"cannot verify contract %q consistency: external actor %q has invalid maxConsistencyLevel %q (must be L0-L4)",
					c.ID, consumerID, rawVal,
				),
			))
			continue
		}
		if maxLvl, ok := actorMaxLevel[consumerID]; ok && contractLevel > maxLvl {
			results = append(results, v.newResult(
				"TOPO-07", SeverityError, IssueMismatch,
				contractFile(c),
				fmt.Sprintf("endpoints.%s[%d]", consumerFieldName(c.Kind), i),
				fmt.Sprintf(
					"contract %q consistencyLevel %s exceeds consumer actor %q maxConsistencyLevel %s",
					c.ID, c.ConsistencyLevel, consumerID, maxLvl,
				),
			))
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

	for _, s := range v.project.Slices {
		for i, cu := range s.ContractUsages {
			if deprecated[cu.Contract] {
				ownerCell := ""
				if c, ok := v.project.Contracts[cu.Contract]; ok {
					ownerCell = c.OwnerCell
				}
				results = append(results, v.newResult(
					"TOPO-08", SeverityError, IssueForbidden,
					sliceFile(s),
					fmt.Sprintf("contractUsages[%d].contract", i),
					fmt.Sprintf(
						"slice %q references deprecated contract %q (ownerCell: %q);"+
							" check the contract description or contact the ownerCell team for the replacement",
						s.ID, cu.Contract, ownerCell,
					),
				))
			}
		}
	}
	return results
}

// validateTOPO09 asserts that an assembly's derived MaxConsistencyLevel matches
// the maximum ConsistencyLevel of its member cells. The derivation lives in
// kernel/metadata.applyAssemblyDerivations (single source of truth); this rule
// is a read-only safeguard that catches accidental drift between derive and
// governance assertion. Skips assemblies with no cells or with cells the
// validator cannot resolve (REF-* rules cover those cases).
func (v *Validator) validateTOPO09() []ValidationResult {
	var results []ValidationResult

	// Sort assembly keys for deterministic output.
	keys := make([]string, 0, len(v.project.Assemblies))
	for k := range v.project.Assemblies {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, asmID := range keys {
		asm := v.project.Assemblies[asmID]
		if asm == nil || len(asm.Cells) == 0 {
			continue
		}
		expected, ok := computeExpectedMax(v.project, asm)
		if !ok {
			continue // unknown cell ref or invalid level — covered by REF/FMT
		}
		if asm.MaxConsistencyLevel != expected.String() {
			results = append(results, v.newResult(
				"TOPO-09", SeverityError, IssueMismatch,
				assemblyFile(asm),
				"maxConsistencyLevel",
				fmt.Sprintf(
					"assembly %q maxConsistencyLevel %q does not match cells max %q",
					asm.ID, asm.MaxConsistencyLevel, expected.String(),
				),
			))
		}
	}
	return results
}

// computeExpectedMax returns the highest ConsistencyLevel among an assembly's
// member cells. Returns (_, false) when any cell ID is unknown or has an
// unparseable level.
func computeExpectedMax(pm *metadata.ProjectMeta, asm *metadata.AssemblyMeta) (cellvocab.Level, bool) {
	var maxLvl cellvocab.Level
	for i, cellID := range asm.Cells {
		c, found := pm.Cells[cellID]
		if !found {
			return 0, false
		}
		lvl, err := cellvocab.ParseLevel(c.ConsistencyLevel)
		if err != nil {
			return 0, false
		}
		if i == 0 || lvl > maxLvl {
			maxLvl = lvl
		}
	}
	return maxLvl, true
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
				results = append(results, v.newResult(
					"TOPO-06", SeverityError, IssueDuplicate,
					assemblyFile(a),
					fmt.Sprintf("cells[%d]", i),
					fmt.Sprintf(
						"cell %q is already assigned to assembly %q, cannot also be in %q",
						cellRef, existing, a.ID,
					),
				))
			} else {
				cellAssembly[cellRef] = a.ID
			}
		}
	}
	return results
}
