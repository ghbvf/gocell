package governance

// INVARIANT: PROJECTION-CONSISTENCY-01
//
// This file implements PROJECTION-CONSISTENCY-01: every contract with
// kind=projection must declare consistencyLevel >= L3 (WorkflowEventual).
//
// AI-robust evaluation:
//
//   - Schema enum upper gate (Hard): contract.schema.json projection if/then
//     block restricts consistencyLevel to ["L3","L4"] via JSON Schema enum.
//     Any YAML file with a projection contract at L0/L1/L2 is rejected at
//     parse time by the schema validator before it reaches governance.
//
//   - Governance rule (Medium, this file): covers in-memory ProjectMeta
//     fixtures that bypass the parser (e.g. unit tests constructing
//     ContractMeta directly). Without this rule, a test or programmatic
//     fixture could silently declare a projection at L2 and pass validation.
//
//   - Blind spot: the schema if/then only fires when the "kind" key is present
//     in the YAML document. This rule covers the residual class: in-memory
//     ContractMeta with Kind=="projection" and an empty/low ConsistencyLevel
//     that was never validated by the JSON Schema layer.

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
//
// The JSON Schema enum gate ("L3"|"L4") in contract.schema.json is the Hard
// upper layer; this rule is the Medium second layer covering in-memory
// fixtures and any path that bypasses the parser.
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
			results = append(results, v.newResult(
				codePROJECTIONCONSISTENCY01, SeverityError, IssueInvalid,
				contractFile(c),
				"consistencyLevel",
				fmt.Sprintf(
					"projection contract %q has empty consistencyLevel; "+
						"in-memory ProjectMeta must declare consistencyLevel for governance check; "+
						"fix: set consistencyLevel to L3|L4",
					c.ID,
				),
			))
			continue
		}
		level, err := cellvocab.ParseLevel(c.ConsistencyLevel)
		if err != nil {
			// Invalid level string is already flagged by SLICE-CONSISTENCY-01.
			continue
		}
		if level < cellvocab.L3 {
			results = append(results, v.newResult(
				codePROJECTIONCONSISTENCY01, SeverityError, IssueInvalid,
				contractFile(c),
				"consistencyLevel",
				fmt.Sprintf(
					"projection contract %q declares consistencyLevel=%q; "+
						"projection contracts require L3 (WorkflowEventual) or L4 (DeviceLatent) "+
						"because they depend on cross-cell eventual consistency; "+
						"fix: raise consistencyLevel to L3|L4",
					c.ID, c.ConsistencyLevel,
				),
			))
		}
	}
	return results
}
