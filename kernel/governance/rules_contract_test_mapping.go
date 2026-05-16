package governance

// rules_contract_test_mapping.go: CONTRACT-ENDPOINT-TEST-MAPPING-01.
//
// CONTRACT-ENDPOINT-TEST-MAPPING-01 (Error):
//
//	Every active HTTP platform contract must be referenced by at least one
//	slice in its server cell (endpoints.server) via a verify.contract entry
//	with the ".serve" role suffix:  "contract.<contractID>.serve".
//
//	This is the HTTP-serve direction complement of ADV-06 (which checks the
//	event-subscribe direction). Both rules close the same gap from opposite
//	contract kinds: a contract that is declared active but is not covered by
//	any test declaration in the implementing slice is undetectable as
//	drift-prone at the metadata governance layer.
//
// Exemptions (same policy as JOURNEY-CONTRACT-EXISTENCE-01):
//   - contracts under examples/  — example projects manage their own closure
//   - lifecycle != "active"      — experimental/deprecated do not require coverage
//   - kind != "http"             — event contracts handled by ADV-06;
//     projection/command/query by future targeted rules
//
// AI-rebust grade: Medium. Hard upgrade backlog:
//   - cap-14 CONTRACT-ENDPOINT-TEST-MAPPING-HARD-CODEGEN-01 (slice.yaml
//     verify.contract → codegen derived from contract.yaml + cell ownership
//     single source). Hard gap: current rule is YAML-governance-layer only;
//     a codegen funnel would make the omission unrepresentable at the authoring
//     level by generating the verify.contract stub automatically.
//
// ref: rules_journey.go:validateJOURNEYCONTRACTEXISTENCE01 (same exemption
// pattern, inverse direction); rules_misc_advisory.go:adv06ContractToSlice
// (ADV-06 subscribe-role counterpart).

import (
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// validateCONTRACTENDPOINTTESTMAPPING01 checks that every active HTTP
// platform contract has at least one slice in its server cell declaring
// "contract.<id>.serve" in verify.contract.
//
// Algorithm:
//  1. Build cellServes index: for each slice, scan verify.contract for entries
//     matching "contract.<id>.serve" and map cellID → set of covered contractIDs.
//  2. For each active HTTP non-examples contract, look up cellServes[server][id].
//     Missing entry → SeverityError with IssueRequired.
func (v *Validator) validateCONTRACTENDPOINTTESTMAPPING01() []ValidationResult {
	cellServes := buildCellServeIndex(v.project.Slices)
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if !isActiveHTTPPlatformContract(c) {
			continue
		}
		if cellServes[c.Endpoints.Server][c.ID] {
			continue
		}
		results = append(results, v.newResult(
			codeCONTRACTENDPOINTTESTMAPPING01, SeverityError, IssueRequired,
			contractFile(c),
			"id",
			fmt.Sprintf(
				"active HTTP contract %q (server cell: %s) is not referenced by any slice "+
					"verify.contract entry with .serve role; "+
					"fix: add \"contract.%s.serve\" to a slice in cell %q under verify.contract, "+
					"or change lifecycle to experimental/deprecated",
				c.ID, c.Endpoints.Server, c.ID, c.Endpoints.Server,
			),
		))
	}
	return results
}

// isActiveHTTPPlatformContract reports whether a contract is subject to
// CONTRACT-ENDPOINT-TEST-MAPPING-01 coverage requirements. All three
// conditions must hold:
//   - kind == "http"
//   - lifecycle == "active"
//   - File does not start with "examples/" (platform-scope only)
func isActiveHTTPPlatformContract(c *metadata.ContractMeta) bool {
	return c != nil &&
		c.Kind == "http" &&
		c.Lifecycle == "active" &&
		!strings.HasPrefix(c.File, "examples/")
}

// buildCellServeIndex maps each cell ID to the set of contract IDs that any of
// its slices declare with role suffix ".serve" in verify.contract. Keeps the
// main rule body O(slices + contracts) instead of O(contracts × slices).
//
// Only entries matching the canonical prefix "contract." and suffix ".serve"
// are indexed; other verify.contract patterns (e.g. ".publish", ".subscribe")
// are ignored.
func buildCellServeIndex(slices map[string]*metadata.SliceMeta) map[string]map[string]bool {
	idx := make(map[string]map[string]bool, len(slices))
	for _, s := range slices {
		for _, entry := range s.Verify.Contract {
			contractID := extractServeContractID(entry)
			if contractID == "" {
				continue
			}
			set, ok := idx[s.BelongsToCell]
			if !ok {
				set = make(map[string]bool)
				idx[s.BelongsToCell] = set
			}
			set[contractID] = true
		}
	}
	return idx
}

// extractServeContractID returns the contract ID from a verify.contract entry
// of the form "contract.<id>.serve", or "" if the entry does not match.
func extractServeContractID(entry string) string {
	const prefix = "contract."
	const suffix = ".serve"
	if !strings.HasPrefix(entry, prefix) || !strings.HasSuffix(entry, suffix) {
		return ""
	}
	inner := entry[len(prefix) : len(entry)-len(suffix)]
	if inner == "" {
		return ""
	}
	return inner
}
