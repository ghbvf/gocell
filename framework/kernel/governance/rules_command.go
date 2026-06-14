// INVARIANT: COMMAND-CONTRACT-SCHEMA-REF-01
// INVARIANT: COMMAND-CONTRACT-CONSISTENCY-LEVEL-01

package governance

// This file implements two PhaseBase governance rules for kind=command contracts.
//
// COMMAND-CONTRACT-SCHEMA-REF-01 enforces that every command contract declares
// both schemaRefs.request and schemaRefs.response. These refs are the typed
// Request/Response DTOs required by contractgen to generate the command handler
// stub and the command result type. A command contract missing either ref makes
// contractgen unable to produce compilable typed glue code.
//
// COMMAND-CONTRACT-CONSISTENCY-LEVEL-01 enforces a lower bound: a command
// contract declaring a consistencyLevel must declare L1 (LocalTx) or higher.
// A command crosses the local boundary and needs at least single-cell
// transactional atomicity; L0 (LocalOnly) is structurally inapplicable.
//
// Both rules are the Medium half of a two-layer enforcement (the same pattern as
// PROJECTION-CONSISTENCY-01's ≥L3 lower bound):
//
//   - Governance rule (Medium, this file): the real CI gate. `gocell validate`
//     runs in CI and covers in-memory ProjectMeta fixtures that never pass
//     through the YAML parser, including command contracts from codegen:false
//     sources.
//
//   - Hard primary gate (contractgen): for SCHEMA-REF-01, buildCommandSpec
//     fail-closed on missing schemaRef; for CONSISTENCY-LEVEL-01, the generated
//     types_gen.go carries a compile-time guard
//     `const _ = uint(cellvocab.<level> - cellvocab.L1)` that overflows uint
//     (fails `go build`) for an L0 command contract — so an L0 codegen:true
//     command contract cannot exist in a buildable tree. The governance rules
//     are the Medium backstops that cover the in-memory / codegen:false vector.
//
//   - Blind spots covered: in-memory ContractMeta with Kind=="command" that
//     never passes through the YAML parser is reported explicitly, preventing
//     programmatic fixtures from silently bypassing the format / level
//     requirements.

import (
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
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

// validateCOMMANDCONTRACTCONSISTENCYLEVEL01 enforces
// COMMAND-CONTRACT-CONSISTENCY-LEVEL-01: a command contract declaring a
// consistencyLevel must declare L1 (LocalTx) or higher; L0 (LocalOnly) is
// rejected.
//
// A command crosses the local boundary (HTTP/async entry → cell) and needs at
// least single-cell transactional atomicity (L1). L0 LocalOnly is for pure
// in-slice computation and is structurally inapplicable to a command. This is a
// lower-bound rule (≥ L1), not an exact-level lock, so the active L4
// devicecommand contracts (and any L1/L2/L3 command) pass unflagged.
//
// Empty / unparseable consistencyLevel is intentionally NOT reported here: it is
// bottomed out by FMT-03 (contract consistencyLevel validity) and the parser's
// non-empty rejection for real YAML. Re-reporting it would double-report the
// same root cause.
//
// AI-robust evaluation: Medium governance rule (gocell validate CI gate +
// in-memory / codegen:false fixture coverage). Hard primary gate = the
// contractgen types.tmpl compile-time guard
// `const _ = uint(cellvocab.<level> - cellvocab.L1)` (same two-layer pattern as
// PROJECTION-CONSISTENCY-01's ≥L3 lower bound).
func (v *Validator) validateCOMMANDCONTRACTCONSISTENCYLEVEL01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != string(cellvocab.ContractCommand) {
			continue
		}
		// Empty / invalid consistencyLevel is bottomed out by FMT-03 and the
		// parser's non-empty rejection — don't double-report here.
		level, err := cellvocab.ParseLevel(c.ConsistencyLevel)
		if err != nil {
			continue
		}
		if level < cellvocab.L1 {
			results = append(results, v.newError(
				codeCOMMANDCONTRACTCONSISTENCYLEVEL01, IssueInvalid,
				contractFile(c),
				"consistencyLevel",
				fmt.Sprintf(
					"command contract %q declares consistencyLevel=%q; command contracts "+
						"require L1 (LocalTx) or higher because a command crosses the local "+
						"boundary and needs at least single-cell transactional atomicity "+
						"(L0 LocalOnly does not apply)",
					c.ID, c.ConsistencyLevel,
				),
				"raise consistencyLevel to L1, L2, L3, or L4 (L0 is invalid for commands)",
			))
		}
	}
	return results
}
