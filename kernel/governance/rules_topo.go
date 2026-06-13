package governance

import (
	"errors"
	"fmt"
	"sort"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/errcode"
)

const fieldContractUsagesContractFmt = "contractUsages[%d].contract"

const fieldContractUsagesRoleFmt = "contractUsages[%d].role"

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
				results = append(results, v.newError(
					codeTOPO01, IssueInvalid,
					sliceFile(s),
					fmt.Sprintf(fieldContractUsagesRoleFmt, i),
					fmt.Sprintf("role %q is not valid for contract kind %q (contract %q)", cu.Role, c.Kind, cu.Contract),
					"use a valid role for this contract kind",
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
				results = append(results, v.newError(
					codeTOPO02, IssueMismatch,
					sliceFile(s),
					fmt.Sprintf(fieldContractUsagesRoleFmt, i),
					fmt.Sprintf(
						"slice %q (cell %q) has provider role %q but contract %q provider is %q",
						s.ID, s.BelongsToCell, cu.Role, cu.Contract, provider,
					),
					"move this slice to the owning cell or remove the provider role",
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
				results = append(results, v.newError(
					codeTOPO03, IssueMismatch,
					sliceFile(s),
					fmt.Sprintf(fieldContractUsagesRoleFmt, i),
					fmt.Sprintf(
						"slice %q (cell %q) has consumer role %q but is not in contract %q consumers %v",
						s.ID, s.BelongsToCell, cu.Role, cu.Contract, consumers,
					),
					"add this cell to the contract's consumers or remove the consumer role from the slice",
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
			return []ValidationResult{v.newError(
				codeTOPO04, IssueMismatch,
				contractFile(c),
				"consistencyLevel",
				fmt.Sprintf(
					"contract %q consistencyLevel %s exceeds provider cell %q level %s",
					c.ID, c.ConsistencyLevel, providerID, providerCell.ConsistencyLevel,
				),
				"lower the contract consistencyLevel or raise the provider cell's level",
			)}
		}
		return nil
	}

	// Check if provider is an external Actor with malformed level.
	if rawVal, malformed := actorMalformed[providerID]; malformed {
		return []ValidationResult{v.newError(
			codeTOPO04, IssueInvalid,
			"actors.yaml",
			actorFieldPath(v.project.Actors, providerID, "maxConsistencyLevel"),
			fmt.Sprintf(
				"cannot verify contract %q consistency: external actor %q has invalid maxConsistencyLevel %q (must be L0-L4)",
				c.ID, providerID, rawVal,
			),
			"set a valid maxConsistencyLevel (L0-L4) in actors.yaml for this actor",
		)}
	}

	// Check if provider is an external Actor with valid level.
	if maxLvl, ok := actorMaxLevel[providerID]; ok && contractLevel > maxLvl {
		return []ValidationResult{v.newError(
			codeTOPO04, IssueMismatch,
			contractFile(c),
			"consistencyLevel",
			fmt.Sprintf(
				"contract %q consistencyLevel %s exceeds external actor %q maxConsistencyLevel %s",
				c.ID, c.ConsistencyLevel, providerID, maxLvl,
			),
			"lower the contract consistencyLevel or raise the actor's maxConsistencyLevel",
		)}
	}
	// If provider is neither a Cell nor an Actor, REF rules cover that.
	return nil
}

// validateTOPO05 checks that L0 cells do not appear in contract endpoints that
// require cross-cell or externally visible side effects. Inbound webhooks are
// the exception: the owner cell is listed as the provider/receiver endpoint, but
// its implementing role is webhook-receive and may be a pure L0 receiver.
func (v *Validator) validateTOPO05() []ValidationResult {
	l0Cells := v.buildL0CellSet()
	if len(l0Cells) == 0 {
		return nil
	}

	var results []ValidationResult
	for _, ct := range v.project.Contracts {
		results = append(results, v.validateTOPO05Contract(ct, l0Cells)...)
	}
	return results
}

func (v *Validator) buildL0CellSet() map[string]bool {
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
	return l0Cells
}

func (v *Validator) validateTOPO05Contract(ct *metadata.ContractMeta, l0Cells map[string]bool) []ValidationResult {
	var results []ValidationResult

	provider := contractProvider(ct)
	if v.isForbiddenL0Endpoint(ct, provider, l0Cells) {
		results = append(results, v.newTOPO05ProviderError(ct, provider))
	}
	for _, consumer := range contractConsumers(ct) {
		if v.isForbiddenL0Endpoint(ct, consumer, l0Cells) {
			results = append(results, v.newTOPO05ConsumerError(ct, consumer))
		}
	}
	return results
}

func (v *Validator) isForbiddenL0Endpoint(
	ct *metadata.ContractMeta,
	cellID string,
	l0Cells map[string]bool,
) bool {
	return l0Cells[cellID] && !v.inboundWebhookL0Receiver(ct, cellID)
}

func (v *Validator) newTOPO05ProviderError(ct *metadata.ContractMeta, provider string) ValidationResult {
	return v.newError(
		codeTOPO05, IssueForbidden,
		contractFile(ct),
		"endpoints",
		fmt.Sprintf("L0 cell %q must not appear as provider in contract %q", provider, ct.ID),
		"remove this cell from the contract endpoints or change the contract kind",
	)
}

func (v *Validator) newTOPO05ConsumerError(ct *metadata.ContractMeta, consumer string) ValidationResult {
	return v.newError(
		codeTOPO05, IssueForbidden,
		contractFile(ct),
		"endpoints",
		fmt.Sprintf("L0 cell %q must not appear as consumer in contract %q", consumer, ct.ID),
		"remove this cell from the contract endpoints",
	)
}

// inboundWebhookL0Receiver reports whether an L0 owner cell is the legitimate
// receive-side implementation of an inbound webhook contract. The external
// sender is the real data provider; the owner cell only verifies/decodes the
// delivery through a webhook-receive slice, so TOPO-05 should not force it to
// pretend to be LocalTx.
func (v *Validator) inboundWebhookL0Receiver(ct *metadata.ContractMeta, provider string) bool {
	return cellvocab.ContractKind(ct.Kind) == cellvocab.ContractWebhook &&
		ct.Direction == string(cellvocab.DirectionInbound) &&
		v.hasImplementingSlice(ct, provider)
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
			results = append(results, v.newError(
				codeTOPO07, IssueInvalid,
				"actors.yaml",
				actorFieldPath(v.project.Actors, consumerID, "maxConsistencyLevel"),
				fmt.Sprintf(
					"cannot verify contract %q consistency: external actor %q has invalid maxConsistencyLevel %q (must be L0-L4)",
					c.ID, consumerID, rawVal,
				),
				"set a valid maxConsistencyLevel (L0-L4) in actors.yaml for this actor",
			))
			continue
		}
		if maxLvl, ok := actorMaxLevel[consumerID]; ok && contractLevel > maxLvl {
			results = append(results, v.newError(
				codeTOPO07, IssueMismatch,
				contractFile(c),
				fmt.Sprintf("endpoints.%s[%d]", consumerFieldName(c.Kind), i),
				fmt.Sprintf(
					"contract %q consistencyLevel %s exceeds consumer actor %q maxConsistencyLevel %s",
					c.ID, c.ConsistencyLevel, consumerID, maxLvl,
				),
				"lower the contract consistencyLevel or raise the actor's maxConsistencyLevel",
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
				results = append(results, v.newError(
					codeTOPO08, IssueForbidden,
					sliceFile(s),
					fmt.Sprintf("contractUsages[%d].contract", i),
					fmt.Sprintf(
						"slice %q references deprecated contract %q (ownerCell: %q);"+
							" check the contract description or contact the ownerCell team for the replacement",
						s.ID, cu.Contract, ownerCell,
					),
					"migrate to the replacement contract and remove this contractUsage",
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
			results = append(results, v.newError(
				codeTOPO09, IssueMismatch,
				assemblyFile(asm),
				"maxConsistencyLevel",
				fmt.Sprintf(
					"assembly %q maxConsistencyLevel %q does not match cells max %q",
					asm.ID, asm.MaxConsistencyLevel, expected.String(),
				),
				"run 'gocell generate' to recompute maxConsistencyLevel",
			))
		}
	}
	return results
}

// validateTOPO10 checks that each assembly's topology section is structurally
// valid, delegating all set-logic to metadata.ValidateTopologyStructure (single
// source of truth). An empty topology is always valid (all-colocated default).
//
// F8: ValidateTopologyStructure returns an errcode error with WithDetails
// carrying "cellID" and "field" keys. TOPO-10 uses errors.As + FindAttr to
// surface these in the ValidationResult.Message and field anchor — no logic
// duplication with ValidateTopologyStructure.
func (v *Validator) validateTOPO10() []ValidationResult {
	var results []ValidationResult

	keys := make([]string, 0, len(v.project.Assemblies))
	for k := range v.project.Assemblies {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, asmID := range keys {
		asm := v.project.Assemblies[asmID]
		if asm == nil {
			continue
		}
		if err := metadata.ValidateTopologyStructure(asm); err != nil {
			fieldAnchor, msg := extractTOPO10Location(err)
			results = append(results, v.newError(
				codeTOPO10, IssueInvalid,
				assemblyFile(asm),
				fieldAnchor,
				msg,
				"topology.colocated ∪ remote must mutually-exclusively and exhaustively partition cells;"+
					" remote endpoints must be bare host:port or http/https URL with host",
			))
		}
	}
	return results
}

// extractTOPO10Location extracts the field anchor and message from a
// ValidateTopologyStructure error. When the error is an *errcode.Error with
// "field" and "cellID" public details, those values are used to produce a
// precise anchor and message — no duplication with ValidateTopologyStructure.
// Falls back to "topology" + err.Error() for non-errcode paths.
func extractTOPO10Location(err error) (fieldAnchor, msg string) {
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		return "topology", err.Error()
	}
	field := stringAttr(ec, "field")
	if field == "" {
		return "topology", err.Error()
	}
	if cellID := stringAttr(ec, "cellID"); cellID != "" {
		return field, fmt.Sprintf("%s (cellID: %q)", ec.Message, cellID)
	}
	return field, ec.Message
}

// stringAttr returns the string-valued public detail under key, or "" if absent
// or non-string. Single helper so extractTOPO10Location stays within the
// cognitive-complexity budget (the FindAttr→string-assert pattern repeats).
func stringAttr(ec *errcode.Error, key string) string {
	d, ok := ec.FindAttr(key)
	if !ok {
		return ""
	}
	s, _ := d.Value().(string)
	return s
}

// validateTOPO11 checks that for every contract consumed by a cell in an
// assembly, the contract's provider cell is reachable (Local or Remote) within
// that assembly's topology. A provider that is Missing (∉ colocated ∪ remote)
// is a deployment-time error — the consumer will never be able to reach it.
//
// Skip conditions (delegated to other rules):
//   - non-consumer roles (provider roles are not the consumer side)
//   - contract not found in project (REF-02 owns missing-contract errors)
//   - framework-owned provider (provider-agnostic; c.Owner().Cell() returns ok=false)
//   - external actor provider (actors are out-of-process; topology only governs cells)
func (v *Validator) validateTOPO11() []ValidationResult {
	var results []ValidationResult

	keys := make([]string, 0, len(v.project.Assemblies))
	for k := range v.project.Assemblies {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, asmID := range keys {
		asm := v.project.Assemblies[asmID]
		if asm == nil {
			continue
		}
		results = append(results, v.checkTOPO11Assembly(asm)...)
	}
	return results
}

// checkTOPO11Assembly checks provider reachability for all consumer slices in one assembly.
func (v *Validator) checkTOPO11Assembly(asm *metadata.AssemblyMeta) []ValidationResult {
	// Build the set of cells that belong to this assembly.
	asmCellSet := make(map[string]struct{}, len(asm.Cells))
	for _, ref := range asm.Cells {
		asmCellSet[ref.ID] = struct{}{}
	}

	var results []ValidationResult
	for _, s := range v.project.Slices {
		if _, inAsm := asmCellSet[s.BelongsToCell]; !inAsm {
			continue // slice's cell is not in this assembly
		}
		results = append(results, v.checkTOPO11Slice(asm, s)...)
	}
	return results
}

// checkTOPO11Slice checks provider reachability for each consumer contract usage in one slice.
// F3: uses contractProvider(c) (ProviderEndpoint, actual serving cell) — not c.Owner().Cell()
// (definitional owner) — per CONTRACT-OWNER-CELL-FUNNEL-01.
// Skip conditions:
//   - non-consumer roles (provider roles are not the consumer side)
//   - contract not found in project (REF-02 owns missing-contract errors)
//   - framework-owned contract (provider-agnostic; c.Owner().IsFramework())
//   - no provider endpoint declared (e.g. draft with no endpoints.server)
//   - external actor provider (actors are out-of-process; topology only governs cells)
func (v *Validator) checkTOPO11Slice(asm *metadata.AssemblyMeta, s *metadata.SliceMeta) []ValidationResult {
	var results []ValidationResult
	for i, cu := range s.ContractUsages {
		if !cellvocab.IsConsumerRole(cellvocab.ContractRole(cu.Role)) {
			continue // only consumer roles trigger reachability checks
		}
		c, ok := v.project.Contracts[cu.Contract]
		if !ok {
			continue // REF-02 owns missing-contract errors
		}
		if c.Owner().IsFramework() {
			continue // framework-owned — provider-agnostic, skip
		}
		provider := contractProvider(c) // actual serving cell (ProviderEndpoint)
		if provider == "" {
			continue // no provider declared (e.g. draft with no endpoints)
		}
		if _, knownCell := v.project.Cells[provider]; !knownCell {
			continue // external actor provider — not subject to assembly topology
		}
		loc := metadata.ClassifyCell(asm, provider)
		if loc.IsMissing() {
			msg := fmt.Sprintf(
				"slice %q (cell %q) consumes contract %q whose provider cell %q"+
					" is neither co-located nor a declared remote endpoint in assembly %q topology",
				s.ID, s.BelongsToCell, cu.Contract, provider, asm.ID,
			)
			results = append(results, v.newError(
				codeTOPO11, IssueRefNotFound,
				sliceFile(s),
				fmt.Sprintf(fieldContractUsagesContractFmt, i),
				msg,
				// F4: hint mentions assembly.cells requirement before topology placement
				"add the provider cell to assembly.cells, then place it in topology.colocated"+
					" or topology.remote (with an endpoint)",
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
	for i, ref := range asm.Cells {
		c, found := pm.Cells[ref.ID]
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
		for i, ref := range a.Cells {
			if existing, ok := cellAssembly[ref.ID]; ok {
				results = append(results, v.newError(
					codeTOPO06, IssueDuplicate,
					assemblyFile(a),
					fmt.Sprintf("cells[%d]", i),
					fmt.Sprintf(
						"cell %q is already assigned to assembly %q, cannot also be in %q",
						ref.ID, existing, a.ID,
					),
					"remove this cell from one of the two assemblies",
				))
			} else {
				cellAssembly[ref.ID] = a.ID
			}
		}
	}
	return results
}

// validateTOPO12 is the INTERIM fail-close gate for topology.remote.
// Until US4 #1963 wires cross-process transport, a non-empty topology.remote
// declaration cannot be honored — the cell would still be composed locally
// (silent degrade). This rule rejects any assembly that declares topology.remote
// at gocell validate time. US4 REMOVES this rule (+ its const + the codegen
// call site) when it makes composition honor the partition.
// The runtime DeploymentTopology API and ValidateTopologyStructure intentionally
// still accept remote (US4-ready schema shape preserved).
func (v *Validator) validateTOPO12() []ValidationResult {
	var results []ValidationResult

	keys := make([]string, 0, len(v.project.Assemblies))
	for k := range v.project.Assemblies {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, asmID := range keys {
		asm := v.project.Assemblies[asmID]
		if asm == nil || len(asm.Topology.Remote) == 0 {
			continue
		}
		results = append(results, v.newError(
			codeTOPO12, IssueForbidden,
			assemblyFile(asm),
			"topology.remote",
			fmt.Sprintf(
				"assembly %q declares topology.remote (%d cell(s))"+
					" which is not yet supported — cross-process transport lands in US4 #1963;"+
					" cells are still composed locally (silent degrade)",
				asm.ID, len(asm.Topology.Remote),
			),
			"remove topology.remote (only colocated is supported until US4 #1963),"+
				" or keep all cells in topology.colocated",
		))
	}
	return results
}
