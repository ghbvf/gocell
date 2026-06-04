// INVARIANT: COMMAND-CONTRACT-SCHEMA-REF-01

package governance

// This file implements one PhaseBase governance rule for kind=command contracts.
//
// COMMAND-CONTRACT-SCHEMA-REF-01 enforces that every command contract declares
// both schemaRefs.request and schemaRefs.response. These refs are the typed
// Request/Response DTOs required by contractgen to generate the command handler
// stub and the command result type. A command contract missing either ref makes
// contractgen unable to produce compilable typed glue code.
//
// AI-robust evaluation:
//
//   - Governance rule (Medium, this file): the real CI gate. `gocell validate`
//     runs in CI and covers in-memory ProjectMeta fixtures that never pass
//     through the YAML parser, including command contracts from codegen:false
//     sources.
//
//   - Hard primary gate: contractgen buildCommandSpec fail-closed — if either
//     schemaRef is absent, contractgen refuses to generate the command handler
//     binding (same class as SAGA-CONTRACT-STEP-SCHEMA-REF-01's "contractgen
//     builder delegation is the Hard primary gate"). This governance rule is the
//     Medium backstop that covers the in-memory fixture vector.
//
//   - Blind spots covered: in-memory ContractMeta with Kind=="command" and
//     missing schemaRefs that never pass through the YAML parser is reported
//     explicitly, preventing programmatic fixtures from silently bypassing the
//     format requirement.

import (
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/kernel/cellvocab"
)

// validateCOMMANDCONTRACTSCHEMAREF01 enforces COMMAND-CONTRACT-SCHEMA-REF-01:
// every command contract must declare non-empty schemaRefs.request and
// schemaRefs.response.
//
// The request and response schemas are the typed DTOs for the command
// handler: contractgen uses schemaRefs.request to generate the typed
// Request parameter and schemaRefs.response to generate the typed Result
// return value. A command contract missing either ref makes codegen unable
// to produce compilable typed glue, leaving the command bus binding incomplete.
//
// AI-robust evaluation: Medium governance rule (gocell validate CI gate +
// in-memory fixture coverage). Hard primary gate = contractgen buildCommandSpec
// fail-closed on missing schemaRef (same class as SAGA-CONTRACT-STEP-SCHEMA-REF-01).
func (v *Validator) validateCOMMANDCONTRACTSCHEMAREF01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != string(cellvocab.ContractCommand) {
			continue
		}
		if strings.TrimSpace(c.SchemaRefs.Request) == "" {
			results = append(results, v.newError(
				codeCOMMANDCONTRACTSCHEMAREF01, IssueRequired,
				contractFile(c),
				"schemaRefs.request",
				fmt.Sprintf(
					"command contract %q has no schemaRefs.request; "+
						"codegen requires a typed Request DTO schema reference",
					c.ID,
				),
				"add schemaRefs.request pointing to the JSON Schema file for the command request DTO "+
					"(e.g. request.schema.json relative to this contract.yaml)",
			))
		}
		if strings.TrimSpace(c.SchemaRefs.Response) == "" {
			results = append(results, v.newError(
				codeCOMMANDCONTRACTSCHEMAREF01, IssueRequired,
				contractFile(c),
				"schemaRefs.response",
				fmt.Sprintf(
					"command contract %q has no schemaRefs.response; "+
						"codegen requires a typed Response DTO schema reference",
					c.ID,
				),
				"add schemaRefs.response pointing to the JSON Schema file for the command response DTO "+
					"(e.g. response.schema.json relative to this contract.yaml)",
			))
		}
	}
	return results
}
