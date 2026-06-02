package governance

// INVARIANT: PROJECTION-CONSISTENCY-01
//
// This file implements PROJECTION-CONSISTENCY-01: every contract with
// kind=projection must declare consistencyLevel >= L3 (WorkflowEventual).
//
// AI-robust evaluation (honest grading — see ADR L3-EXAMPLE-PROJECTION-01):
// this is a Hard-main-gate + Medium-backstop funnel (same shape as saga's
// "contractgen builder delegation = Hard 主门控 / governance rule = Medium 兜底",
// see SAGA-CONTRACT-RETRY-TIMEOUT-01).
//
//   - Hard main gate (contractgen codegen funnel, gh #960): for a codegen=true
//     projection contract, contractgen emits into types_gen.go a compile-time
//     guard `const _ = uint(cellvocab.<level> - cellvocab.L3)` (types.tmpl). A
//     level below L3 makes the subtraction negative and overflows uint at
//     compile time, so an invalid projection contract cannot exist in a
//     buildable tree — the violation is unrepresentable, not merely reported.
//     This is the AI-robust Hard archetype (codegen funnel + compile-error
//     downstream). NB: the issue's original framing ("parser load-time
//     jsonschema.Validate") was evaluated and rejected — a parse-time validator
//     is a runtime guard (Medium), and it does not cover the in-memory vector.
//
//   - Medium backstop (governance rule, this file): `gocell validate` runs in
//     CI and blocks projection contracts the codegen funnel cannot reach —
//     codegen=false contracts and in-memory ProjectMeta fixtures with Kind==
//     "projection" and an empty/low ConsistencyLevel that never produced
//     generated Go. Same archetype as SLICE-CONSISTENCY-02 (publish→≥L2): a
//     validate-time lower-bound guard, Medium by the AI-robust charter.
//
//   - Schema enum (documentation + test layer, NOT a parse-time gate): the
//     projection if/then block in contract.schema.json restricts
//     consistencyLevel to ["L3","L4"]. The metadata parser does NOT run
//     jsonschema.Validate at load time, so this enum is enforced by
//     contract_schema_test.go (TestProjectionConsistencyLevelSchemaEnum) and
//     serves as IDE/tooling documentation only — the Hard gate is the codegen
//     funnel above, not the schema.

import (
	"fmt"

	"github.com/ghbvf/gocell/kernel/cellvocab"
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
