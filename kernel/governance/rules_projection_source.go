package governance

// INVARIANT: PROJECTION-SAGA-SOURCE-NEEDS-PROJECTION-01
//
// This file implements one PhaseBase governance rule that closes the hole opened
// by widening ValidRolesForKind(saga) to include RoleSubscribe (EPIC #1609 PR-05).
//
// PROJECTION-SAGA-SOURCE-NEEDS-PROJECTION-01: a bidirectional coupling between the
// saga-journal projection source and a saga contract, enforced in BOTH directions:
//
//	(a) a role=subscribe CU whose contract resolves to kind=saga MUST carry a
//	    non-empty Projection AND ProjectionSource == "saga-journal". A saga has no
//	    outbox topic, so the ONLY legal subscribe target on a saga contract is its
//	    journal-as-projection-source. A bare subscribe on a saga contract (no
//	    projection) passes TOPO-01 — the role IS legal for the kind now — but is
//	    meaningless; this rule rejects it.
//
//	(b) a CU declaring ProjectionSource == "saga-journal" MUST reference a
//	    kind=saga contract. The saga-journal source reads the GLOBAL saga journal
//	    (stream saga.journal.v1); pointing it at a non-saga contract is a lineage
//	    error (the contract anchor is for governance/lineage, not a runtime filter).
//
// Why this rule, and not the parser:
//
// The parser's per-CU placement check (checkProjectionSourcePlacement) enforces
// the CU-LOCAL projectionSource rules (required-when-projection, enum membership,
// source-without-projection, saga-journal-forbids-onReset). It deliberately does
// NOT resolve the contract's KIND — the parser works on a single CU in isolation
// and has no project-wide contract registry. Resolving kind=saga (direction a)
// and the saga-journal⟹kind=saga implication (direction b) requires the validator's
// cross-file v.project.Contracts map, so it can only live as a governance rule.
//
// Skip semantics (mirror SAGA-CELL-LEVEL-L3-DECLARE-01 / TOPO-01 ownership):
//   - CU whose referenced contract is missing → skip (REF owns "unknown contract").
//   - direction (a) only fires on contracts that exist and resolve to kind=saga;
//     any other kind on a subscribe CU is outside this rule's scope.
//
// AI-robust evaluation (Medium — governance rule, the permanent ceiling for a
// YAML cross-reference coupling that resolves a contract's kind from the project
// registry; same archetype as PROJECTION-PROVIDE-NEEDS-WRITE-CU-01 and
// SAGA-CELL-LEVEL-L3-DECLARE-01):
//
//   - The invariant links a slice.contractUsages field (role/projection/
//     projectionSource) to ANOTHER file's contract.kind. That cross-file value
//     coupling cannot be expressed in the Go type system, a sealed constructor, a
//     codegen funnel, or a reflect field freeze — the only enforcement surface is
//     validate-time cross-file inspection. Governance rule is therefore the highest
//     achievable archetype; no Hard path exists.
//
//   - Blind spot covered: in-memory ProjectMeta fixtures (tests/fakes) that
//     construct a saga subscribe without projection, or a saga-journal source on a
//     non-saga contract, are caught here because the rule reads the in-memory model
//     directly — no parser round-trip needed.
//
//   - Anti-vacuity: TestProjectionSagaSourceNeedsProjection covers both firing
//     directions (saga subscribe without projection; saga-journal on a non-saga
//     contract) AND two non-firing happy paths (saga subscribe with
//     projection+saga-journal; a normal outbox event projection), proving the rule
//     is neither always-fire nor never-fire.

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// validatePROJECTIONSAGASOURCENEEDSPROJECTION01 enforces
// PROJECTION-SAGA-SOURCE-NEEDS-PROJECTION-01 over every subscribe CU and every CU
// declaring projectionSource=saga-journal.
func (v *Validator) validatePROJECTIONSAGASOURCENEEDSPROJECTION01() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		for i, cu := range s.ContractUsages {
			results = append(results, v.checkSagaProjectionSource(s, cu, i)...)
		}
	}
	return results
}

// checkSagaProjectionSource applies both directions of the saga-journal coupling
// to a single CU. Extracted to keep the loop body within cognitive complexity ≤ 15.
func (v *Validator) checkSagaProjectionSource(
	s *metadata.SliceMeta, cu metadata.ContractUsage, idx int,
) []ValidationResult {
	c, found := v.project.Contracts[cu.Contract]
	if !found {
		return nil // REF owns the missing-contract finding.
	}
	isSaga := cellvocab.ContractKind(c.Kind) == cellvocab.ContractSaga
	isSubscribe := cellvocab.ContractRole(cu.Role) == cellvocab.RoleSubscribe
	isSagaJournal := cu.ProjectionSource == string(cellvocab.ProjectionSourceSagaJournal)

	var results []ValidationResult
	// Direction (a): subscribe + kind=saga ⟹ projection + saga-journal source.
	if isSubscribe && isSaga && (cu.Projection == "" || !isSagaJournal) {
		results = append(results, v.sagaSubscribeNeedsProjection(s, cu, idx))
	}
	// Direction (b): saga-journal source ⟹ kind=saga contract.
	if isSagaJournal && !isSaga {
		results = append(results, v.sagaJournalNeedsSagaContract(s, cu, idx, c.Kind))
	}
	return results
}

// sagaSubscribeNeedsProjection reports direction (a): a subscribe CU on a saga
// contract that does not declare projection+saga-journal.
func (v *Validator) sagaSubscribeNeedsProjection(
	s *metadata.SliceMeta, cu metadata.ContractUsage, idx int,
) ValidationResult {
	return v.newError(
		codePROJECTIONSAGASOURCENEEDSPROJECTION01, IssueRequired,
		sliceFile(s),
		fmt.Sprintf(fieldContractUsagesRoleFmt, idx),
		fmt.Sprintf(
			"slice %q has role=subscribe on saga contract %q but does not declare "+
				"projection + projectionSource=saga-journal; a saga has no outbox topic, "+
				"so the only legal subscribe target is its journal-as-projection-source",
			s.ID, cu.Contract,
		),
		"set a non-empty projection and projectionSource=saga-journal on this "+
			"contractUsage, or remove the subscribe CU",
	)
}

// sagaJournalNeedsSagaContract reports direction (b): a CU with
// projectionSource=saga-journal pointing at a non-saga contract.
func (v *Validator) sagaJournalNeedsSagaContract(
	s *metadata.SliceMeta, cu metadata.ContractUsage, idx int, kind string,
) ValidationResult {
	return v.newError(
		codePROJECTIONSAGASOURCENEEDSPROJECTION01, IssueMismatch,
		sliceFile(s),
		fmt.Sprintf("contractUsages[%d].projectionSource", idx),
		fmt.Sprintf(
			"slice %q declares projectionSource=saga-journal on contract %q (kind=%q) "+
				"but the saga-journal source requires a kind=saga contract",
			s.ID, cu.Contract, kind,
		),
		"point this contractUsage at a kind=saga contract, or change projectionSource "+
			"to outbox if it reads an event topic",
	)
}
