package governance

// INVARIANT: PROJECTION-CONSISTENCY-01
// INVARIANT: PROJECTION-PROVIDE-NEEDS-WRITE-CU-01
//
// This file implements two PhaseBase governance rules for projection contracts:
//
// PROJECTION-CONSISTENCY-01: every contract with kind=projection must declare
// consistencyLevel >= L3 (WorkflowEventual).
//
// PROJECTION-PROVIDE-NEEDS-WRITE-CU-01: a slice with role=provide referencing
// a kind=projection contract must belong to a cell that also has at least one
// subscribe CU with a non-empty Projection field. Without the write side the
// projection silently degrades to a plain subscription (no checkpoint / rebuild).
//
// AI-robust evaluation (honest grading — see ADR L3-EXAMPLE-PROJECTION-01):
//
//   - Governance rule (Medium, this file): the real enforcement. `gocell
//     validate` runs in CI and blocks any projection contract declared at
//     L0/L1/L2 (or with an empty level for in-memory ProjectMeta fixtures).
//     This is the same archetype as SLICE-CONSISTENCY-02 (publish→≥L2) — a
//     validate-time lower-bound guard, which is Medium by the AI-robust
//     charter (violations need a runtime/validate guard to surface, not a
//     compile-time / type-system impossibility).
//
//   - Schema enum (documentation + test layer, NOT a parse-time gate): the
//     projection if/then block in contract.schema.json restricts
//     consistencyLevel to ["L3","L4"]. The metadata parser does NOT run
//     jsonschema.Validate at load time, so this enum is enforced by
//     contract_schema_test.go (TestProjectionConsistencyLevelSchemaEnum) and
//     serves as IDE/tooling documentation — it is the single source the
//     governance rule mirrors, not an independent runtime gate.
//
//   - Blind spot covered: in-memory ContractMeta with Kind=="projection" and an
//     empty/low ConsistencyLevel that never passed through the YAML parser. The
//     governance rule reports it explicitly so a programmatic fixture cannot
//     silently declare a projection at L2.
//
//   - Hard 化路径：在 metadata parser Load 时运行 jsonschema.Validate 可将
//     schema enum gate 升级为 parse-time 强制（基础设施已有）。
//     升级追踪：gh issue（PR #937 review 待开）。

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// validateProjectionConsistency enforces PROJECTION-CONSISTENCY-01:
// contracts with kind=projection must declare consistencyLevel >= L3.
//
// Read-model projections inherently depend on cross-cell eventual consistency
// (L3 WorkflowEventual) or higher. Declaring L0/L1/L2 is a semantic error:
// the projection consumer would behave as if it owns a local-only or
// transactional invariant, masking the actual L3+ coupling.
func (v *Validator) validateProjectionConsistency() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != string(cellvocab.ContractProjection) {
			continue
		}
		// Empty consistencyLevel: in-memory ProjectMeta fixtures may reach this
		// point without parser validation. Report explicitly so they don't
		// silently bypass the lower-bound check (same pattern as SLICE-CONSISTENCY-02).
		if c.ConsistencyLevel == "" {
			results = append(results, v.newError(
				codePROJECTIONCONSISTENCY01, IssueInvalid,
				contractFile(c),
				"consistencyLevel",
				fmt.Sprintf(
					"projection contract %q has empty consistencyLevel; "+
						"in-memory ProjectMeta must declare consistencyLevel for governance check",
					c.ID,
				),
				"set consistencyLevel to L3 or L4",
			))
			continue
		}
		level, err := cellvocab.ParseLevel(c.ConsistencyLevel)
		if err != nil {
			// Invalid level string is already flagged by SLICE-CONSISTENCY-01.
			continue
		}
		if level < cellvocab.L3 {
			results = append(results, v.newError(
				codePROJECTIONCONSISTENCY01, IssueInvalid,
				contractFile(c),
				"consistencyLevel",
				fmt.Sprintf(
					"projection contract %q declares consistencyLevel=%q; "+
						"projection contracts require L3 (WorkflowEventual) or L4 (DeviceLatent) "+
						"because they depend on cross-cell eventual consistency",
					c.ID, c.ConsistencyLevel,
				),
				"raise consistencyLevel to L3 or L4",
			))
		}
	}
	return results
}

// validatePROJECTIONPROVIDENEEDSWRITECU01 enforces
// PROJECTION-PROVIDE-NEEDS-WRITE-CU-01:
// a slice with role=provide referencing a kind=projection contract must belong
// to a cell that also has at least one subscribe CU with a non-empty Projection
// field (the write side that causes cellgen to emit reg.RegisterProjection
// instead of reg.Subscribe).
//
// The implication is ONE-WAY:
//
//	provide → kind:projection ⟹ cell has ≥1 projection write CU.
//
// The inverse is intentionally NOT enforced: a cell may have a subscribe+projection
// CU without a provide CU (e.g. during incremental rollout, or if the read-side
// is owned by a different cell).
//
// Skip semantics (mirror SAGA-CELL-LEVEL-L3-DECLARE-01):
//   - provide CU whose referenced contract is missing → skip (REF owns "unknown contract").
//   - provide CU whose referenced contract exists but is NOT kind=projection → skip
//     (TOPO-01 owns role/kind legality).
//   - slice whose BelongsToCell is not in v.project.Cells → skip (REF-01 owns).
//
// One finding is emitted per offending provide CU (not per cell), so a cell with
// multiple provide-projection CUs produces multiple findings. This mirrors the
// per-CU granularity of SAGA-CELL-LEVEL-L3-DECLARE-01 and pinpoints exactly which
// contractUsage is unmatched, aiding remediation.
//
// Known limitation (coarse, existence-only — NOT projection-identity-bound):
// the rule only checks that the cell owns AT LEAST ONE projection write CU; it
// does NOT verify that the write CU's Projection identity corresponds to the
// specific projection contract being provided. A cell that provides projection
// contract A (read side) while only declaring a subscribe+projection write CU
// for projection B would pass — a false negative that surfaces only when a
// single cell hosts multiple distinct projections. Binding the two requires a
// metadata-model link between a provide projection contract and a projection
// identity (the provide references a contract ID like
// "projection.order.status-summary.v1" while the write CU's Projection field is
// an identity like "order_status" — different namespaces with no current key to
// match). Tightening this is tracked in gh #1443.
//
// AI-robust evaluation (Medium — governance rule, same permanent ceiling as
// PROJECTION-CONSISTENCY-01 and SAGA-CELL-LEVEL-L3-DECLARE-01):
//
//   - The invariant links a YAML cross-reference coupling (slice.contractUsages
//     role=provide ↔ another slice.contractUsages role=subscribe+projection) that
//     cannot be expressed in the Go type system or as a compile-time constraint.
//     The only enforcement surface is validate-time cross-file inspection —
//     governance rule is therefore the highest achievable archetype.
//
//   - Hard path does not exist: no codegen funnel, no type marker, no sealed
//     interface, no reflect field freeze can close the gap between two YAML files'
//     values. The rule is Medium by definition per ai-robust.md §AI-robust
//     三档分级 ("violations need a runtime/validate guard to surface, not a
//     compile-time / type-system impossibility").
//
//   - Blind spot covered: in-memory ProjectMeta fixtures (in tests or fakes) that
//     construct a provide CU against a projection contract without also supplying a
//     matching subscribe+projection CU are caught here, because the rule reads the
//     in-memory model directly — no parser round-trip needed.
//
//   - Reverse self-check: the negative fixture cases in
//     TestProjectionProvideNeedsWriteCU01 (missing contract, non-projection kind,
//     missing cell, write CU present) assert that the rule does NOT fire, providing
//     the required blind-spot coverage evidence.
func (v *Validator) validatePROJECTIONPROVIDENEEDSWRITECU01() []ValidationResult {
	// Pre-compute the set of cells that have at least one projection write CU
	// (subscribe + non-empty Projection field). A single pass over all slices.
	cellsWithWriteCU := v.cellsWithProjectionWriteCU()

	var results []ValidationResult
	for _, s := range v.project.Slices {
		results = append(results, v.checkProvideProjectionWriteCU(s, cellsWithWriteCU)...)
	}
	return results
}

// cellsWithProjectionWriteCU returns a set of cell IDs that own at least one
// slice with a subscribe CU whose Projection field is non-empty. Extracted to
// keep validatePROJECTIONPROVIDENEEDSWRITECU01 within cognitive complexity ≤ 15.
func (v *Validator) cellsWithProjectionWriteCU() map[string]struct{} {
	cellSet := make(map[string]struct{})
	for _, s := range v.project.Slices {
		for _, cu := range s.ContractUsages {
			if cellvocab.ContractRole(cu.Role) == cellvocab.RoleSubscribe && cu.Projection != "" {
				cellSet[s.BelongsToCell] = struct{}{}
			}
		}
	}
	return cellSet
}

// checkProvideProjectionWriteCU checks all provide CUs of a single slice for
// the provide→projection ⟹ write-CU invariant. Extracted to keep
// validatePROJECTIONPROVIDENEEDSWRITECU01 within cognitive complexity ≤ 15.
func (v *Validator) checkProvideProjectionWriteCU(
	s *metadata.SliceMeta,
	cellsWithWriteCU map[string]struct{},
) []ValidationResult {
	// If BelongsToCell is unknown, skip — REF-01 owns the missing cell finding.
	if _, cellExists := v.project.Cells[s.BelongsToCell]; !cellExists {
		return nil
	}

	_, hasWriteCU := cellsWithWriteCU[s.BelongsToCell]
	var results []ValidationResult

	for i, cu := range s.ContractUsages {
		if cellvocab.ContractRole(cu.Role) != cellvocab.RoleProvide {
			continue
		}
		// If the contract is missing, skip — REF owns "unknown contract".
		c, found := v.project.Contracts[cu.Contract]
		if !found {
			continue
		}
		// If it's not a projection contract, skip — TOPO-01 owns role/kind legality.
		if cellvocab.ContractKind(c.Kind) != cellvocab.ContractProjection {
			continue
		}
		// Cell provides a projection but has no projection write CU.
		if !hasWriteCU {
			results = append(results, v.newError(
				codePROJECTIONPROVIDENEEDSWRITECU01, IssueRequired,
				sliceFile(s),
				fmt.Sprintf(fieldContractUsagesRoleFmt, i),
				fmt.Sprintf(
					"slice %q (cell %q) has role=provide on projection contract %q "+
						"but cell %q has no subscribe CU with a non-empty projection field; "+
						"without the write side, cellgen emits reg.Subscribe (no checkpoint/rebuild) "+
						"instead of reg.RegisterProjection",
					s.ID, s.BelongsToCell, cu.Contract, s.BelongsToCell,
				),
				fmt.Sprintf(
					"add a subscribe contractUsage with a non-empty projection field in a slice "+
						"belonging to cell %q, or if the write side is intentionally absent, "+
						"remove the provide CU and use role=read instead",
					s.BelongsToCell,
				),
			))
		}
	}
	return results
}
