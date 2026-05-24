package governance

// INVARIANT: PROJECTION-CONSISTENCY-01
//
// This file implements PROJECTION-CONSISTENCY-01: every contract with
// kind=projection must declare consistencyLevel >= L3 (WorkflowEventual).
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
