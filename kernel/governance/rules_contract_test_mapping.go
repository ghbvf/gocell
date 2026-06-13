package governance

// rules_contract_test_mapping.go: CONTRACT-ENDPOINT-TEST-MAPPING-01.
//
// CONTRACT-ENDPOINT-TEST-MAPPING-01 (Error):
//
//	与 ADV-06 同形态双向校验：方向 A (contract → slice) + 方向 B (slice → contract).
//
//	Direction A (contract → slice): every active serve-style platform contract
//	must be referenced by at least one slice in its server cell
//	(endpoints.server) via a verify.contract entry with the ".serve" role
//	suffix: "contract.<contractID>.serve". Serve-style kinds are those whose
//	role set includes RoleServe (http and grpc — both provider/server contracts
//	using endpoints.server), derived from cellvocab.ValidRolesForKind via
//	isServeRoleKind so adding a future serve-style kind extends coverage
//	automatically without editing this rule.
//
//	Direction B (slice → contract): when a slice declares "contract.<id>.serve" in
//	verify.contract, the referenced contract must satisfy ALL of the following.
//	Each predicate failure produces its own diagnostic so the developer sees
//	the precise root cause instead of a silent skip:
//	  1. The contract must exist in v.project.Contracts. A dangling .serve
//	     entry (typo, removed contract, or unmerged change) was previously
//	     silent — review F4 closed it.
//	  2. The contract's kind must be serve-style (http or grpc). Event contracts
//	     are handled by ADV-06 (endpoints.subscribers) and must not appear in a
//	     .serve entry; this catches a slice declaring a subscribe contract under
//	     the wrong role.
//	  3. The contract's lifecycle must be "active". A slice declaring serve
//	     coverage for a deprecated / experimental contract signals stale or
//	     out-of-order migration; the entry should be removed or the lifecycle
//	     updated.
//	  4. If the contract lives under examples/, the slice MUST also live under
//	     examples/. Examples self-serving (examples slice → examples contract
//	     within the same example project) is allowed because examples are
//	     permitted to depend on all layers (CLAUDE.md "依赖规则"); platform
//	     slice → examples contract is forbidden because that direction would
//	     invert the allowed dependency arrow.
//	  5. The contract's endpoints.server must equal the slice's belongsToCell.
//	     Mismatch means the slice claims coverage for a contract it does not own.
//	     gRPC mirrors http here: both carry the provider in endpoints.server.
//
//	This is the serve-side direction complement of ADV-06 (which checks the
//	event-subscribe direction). Both rules close the same gap from opposite
//	role families: a contract that is declared active but is not covered by
//	any test declaration in the implementing slice is undetectable as
//	drift-prone at the metadata governance layer.
//
// Exemptions (same policy as JOURNEY-CONTRACT-EXISTENCE-01):
//   - contracts under examples/  — example projects manage their own closure
//   - lifecycle != "active"      — experimental/deprecated do not require coverage
//   - non-serve-style kind       — event contracts handled by ADV-06;
//     projection/command by future targeted rules
//
// AI-robust grade: Medium.
//
// ref: rules_journey.go:validateJOURNEYCONTRACTEXISTENCE01 (same exemption
// pattern, inverse direction); rules_misc_advisory.go:adv06ContractToSlice
// (ADV-06 subscribe-role counterpart).

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
)

const examplesPathPrefix = "examples/"

// validateCONTRACTENDPOINTTESTMAPPING01 runs the bidirectional check:
//   - Direction A (contract → slice): every active serve-style (http/grpc)
//     contract has at least one slice in its server cell declaring
//     "contract.<id>.serve".
//   - Direction B (slice → contract): every slice verify.contract entry of the
//     form "contract.<id>.serve" refers to an active serve-style (http/grpc)
//     contract whose endpoints.server equals the slice's belongsToCell.
//
// Algorithm mirrors ADV-06 (see rules_misc_advisory.go lines 177-182).
func (v *Validator) validateCONTRACTENDPOINTTESTMAPPING01() []ValidationResult {
	cellServes := buildCellServeIndex(v.project.Slices)
	results := v.ctmContractToSlice(cellServes)
	results = append(results, v.ctmSliceToContract()...)
	return results
}

// ctmContractToSlice implements direction A: for each active serve-style
// platform contract, verify at least one slice in its server cell declares the
// serve entry. Reports error with candidate slice paths (up to 3) to aid the
// developer.
func (v *Validator) ctmContractToSlice(cellServes map[string]map[string]bool) []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if !isActiveServeStylePlatformContract(c) {
			continue
		}
		if cellServes[c.Endpoints.Server][c.ID] {
			continue
		}
		candidateHint := v.buildCandidateSliceHint(c.Endpoints.Server)
		results = append(results, v.newError(
			codeCONTRACTENDPOINTTESTMAPPING01, IssueRequired,
			contractFile(c),
			"id",
			fmt.Sprintf(
				"active %s contract %q (server cell: %s) is not referenced by any slice "+
					"verify.contract entry with .serve role%s",
				c.Kind, c.ID, c.Endpoints.Server, candidateHint,
			),
			fmt.Sprintf(
				"add \"contract.%s.serve\" to a slice in cell %q under verify.contract, "+
					"or change lifecycle to experimental/deprecated",
				c.ID, c.Endpoints.Server,
			),
		))
	}
	return results
}

// ctmSliceToContract implements direction B: for each verify.contract entry
// of the form "contract.<id>.serve", every predicate in the 5-step contract
// check (existence, kind=http, lifecycle=active, examples arrow, server match)
// must hold; each failure emits a distinct diagnostic via a dedicated helper.
// The previous monolithic form used !isActiveServeStylePlatformContract(c) as a
// "skip" filter for direction B, silently passing dangling references,
// role/lifecycle drift, and platform-slice-serving-examples-contract cases
// (review F4); the per-check helpers below were extracted to keep cognitive
// complexity ≤ 15 per CLAUDE.md while preserving each check's distinct fix
// guidance (the newError fix argument).
func (v *Validator) ctmSliceToContract() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		for i, entry := range s.Verify.Contract {
			if r := v.ctmEvaluateServeEntry(s, i, entry); r != nil {
				results = append(results, *r)
			}
		}
	}
	return results
}

// ctmEvaluateServeEntry runs the 5-step contract check sequentially and
// returns the first failure as a non-nil *ValidationResult. Returns nil when
// the entry is not a .serve role or all 5 checks pass. The pointer sentinel
// (rather than (ValidationResult, bool)) avoids constructing empty
// ValidationResult{} literals, which trip
// TestGovernanceRuleCodeConstSingleSource — every ValidationResult literal in
// kernel/governance must include a Code: field, and the empty-zero sentinel
// would either need a carve-out or violate the rule.
func (v *Validator) ctmEvaluateServeEntry(s *metadata.SliceMeta, i int, entry string) *ValidationResult {
	contractID := extractServeContractID(entry)
	if contractID == "" {
		return nil
	}
	fieldPath := fmt.Sprintf("verify.contract[%d]", i)
	c := v.project.Contracts[contractID]
	if r := v.ctmCheckContractExists(s, c, fieldPath, entry, contractID); r != nil {
		return r
	}
	if r := v.ctmCheckKindServeStyle(s, c, fieldPath, entry, contractID); r != nil {
		return r
	}
	if r := v.ctmCheckLifecycleActive(s, c, fieldPath, entry, contractID); r != nil {
		return r
	}
	if r := v.ctmCheckExamplesArrow(s, c, fieldPath, entry, contractID); r != nil {
		return r
	}
	return v.ctmCheckServerMatch(s, c, fieldPath, entry, contractID)
}

// ctmCheckContractExists is direction B step 1: the referenced contract must
// exist in the project.
func (v *Validator) ctmCheckContractExists(
	s *metadata.SliceMeta, c *metadata.ContractMeta,
	fieldPath, entry, contractID string,
) *ValidationResult {
	if c != nil {
		return nil
	}
	r := v.newError(
		codeCONTRACTENDPOINTTESTMAPPING01, IssueRefNotFound,
		sliceFile(s), fieldPath,
		fmt.Sprintf(
			"slice %q declares verify.contract %q (.serve role) but contract %q does not exist",
			s.ID, entry, contractID,
		),
		"remove this entry, fix the contract ID typo, or add the missing contract.yaml",
	)
	return &r
}

// ctmCheckKindServeStyle is direction B step 2: the .serve role applies to
// serve-style (provider/server) contracts only — http and grpc, both carrying
// the provider in endpoints.server. Event contracts use ADV-06
// (endpoints.subscribers); projection/command use their own role mappings.
func (v *Validator) ctmCheckKindServeStyle(
	s *metadata.SliceMeta, c *metadata.ContractMeta,
	fieldPath, entry, contractID string,
) *ValidationResult {
	if isServeRoleKind(c.Kind) {
		return nil
	}
	r := v.newError(
		codeCONTRACTENDPOINTTESTMAPPING01, IssueMismatch,
		sliceFile(s), fieldPath,
		fmt.Sprintf(
			"slice %q declares verify.contract %q (.serve role) but contract %q kind is %q (must be \"http\" or \"grpc\")",
			s.ID, entry, contractID, c.Kind,
		),
		"remove this entry; .serve is for http/grpc provider contracts — event contracts use ADV-06 (endpoints.subscribers)",
	)
	return &r
}

// ctmCheckLifecycleActive is direction B step 3: serve coverage is required
// only for lifecycle: active contracts.
func (v *Validator) ctmCheckLifecycleActive(
	s *metadata.SliceMeta, c *metadata.ContractMeta,
	fieldPath, entry, contractID string,
) *ValidationResult {
	if c.Lifecycle == lifecycleActive {
		return nil
	}
	r := v.newError(
		codeCONTRACTENDPOINTTESTMAPPING01, IssueMismatch,
		sliceFile(s), fieldPath,
		fmt.Sprintf(
			"slice %q declares verify.contract %q (.serve role) but contract %q lifecycle is %q (must be \"active\")",
			s.ID, entry, contractID, c.Lifecycle,
		),
		"remove this entry, or promote the contract to lifecycle: active",
	)
	return &r
}

// ctmCheckExamplesArrow is direction B step 4: examples self-serving (examples
// slice → examples contract within the same example project) is allowed
// because examples are permitted to depend on all layers (CLAUDE.md "依赖
// 规则"); platform-slice → examples-contract is forbidden because that would
// invert the allowed dependency arrow.
func (v *Validator) ctmCheckExamplesArrow(
	s *metadata.SliceMeta, c *metadata.ContractMeta,
	fieldPath, entry, contractID string,
) *ValidationResult {
	if !strings.HasPrefix(c.File, examplesPathPrefix) || strings.HasPrefix(sliceFile(s), examplesPathPrefix) {
		return nil
	}
	r := v.newError(
		codeCONTRACTENDPOINTTESTMAPPING01, IssueForbidden,
		sliceFile(s), fieldPath,
		fmt.Sprintf(
			"slice %q declares verify.contract %q (.serve role) but contract %q lives under examples/ (%s)",
			s.ID, entry, contractID, c.File,
		),
		"remove this entry — platform slices must not serve example contracts (examples depend on platform, not the reverse)",
	)
	return &r
}

// ctmCheckServerMatch is direction B step 5: contract.endpoints.server must
// equal slice.belongsToCell. Mismatch means the slice claims coverage for a
// contract it does not own.
func (v *Validator) ctmCheckServerMatch(
	s *metadata.SliceMeta, c *metadata.ContractMeta,
	fieldPath, entry, contractID string,
) *ValidationResult {
	if c.Endpoints.Server == s.BelongsToCell {
		return nil
	}
	r := v.newError(
		codeCONTRACTENDPOINTTESTMAPPING01, IssueMismatch,
		sliceFile(s), fieldPath,
		fmt.Sprintf(
			"slice %q declares verify.contract %q (.serve role) but contract's endpoints.server (%s) ≠ slice's belongsToCell (%s)",
			s.ID, entry, c.Endpoints.Server, s.BelongsToCell,
		),
		fmt.Sprintf(
			"remove this entry or change the slice's belongsToCell to match, "+
				"or update contract %q endpoints.server to %q",
			contractID, s.BelongsToCell,
		),
	)
	return &r
}

// buildCandidateSliceHint returns a "; candidate slices: ..." suffix listing
// up to 3 slice file paths that belong to the given owner cell, in alphabetical
// order. Used in direction A error messages to help developers locate the slice
// that should carry the verify.contract entry.
//
// If no slices belong to the owner cell, returns a hint to create one or change
// endpoints.server. If there are 4+ candidates, the tail is truncated with "+N more".
func (v *Validator) buildCandidateSliceHint(ownerCell string) string {
	var paths []string
	for _, s := range v.project.Slices {
		if s.BelongsToCell == ownerCell {
			paths = append(paths, s.File)
		}
	}
	if len(paths) == 0 {
		return fmt.Sprintf("; no slice belongs to owner cell %s, create one or change endpoints.server", ownerCell)
	}
	sort.Strings(paths)
	const maxShow = 3
	if len(paths) <= maxShow {
		return "; candidate slices: " + strings.Join(paths, ", ")
	}
	shown := paths[:maxShow]
	extra := len(paths) - maxShow
	return fmt.Sprintf("; candidate slices: %s, +%d more", strings.Join(shown, ", "), extra)
}

// isServeRoleKind reports whether contracts of the given kind use the
// serve/call role pair — i.e. request/response provider contracts whose
// provider lives in endpoints.server (http and grpc today). Derived from
// cellvocab.ValidRolesForKind so that introducing a future kind with RoleServe
// automatically extends .serve coverage without editing this rule, keeping the
// CONTRACT-ENDPOINT-TEST-MAPPING-01 role-family definition single-sourced with
// the contract vocabulary.
func isServeRoleKind(kind string) bool {
	return slices.Contains(cellvocab.ValidRolesForKind(cellvocab.ContractKind(kind)), cellvocab.RoleServe)
}

// isActiveServeStylePlatformContract reports whether a contract is subject to
// CONTRACT-ENDPOINT-TEST-MAPPING-01 coverage requirements. All three
// conditions must hold:
//   - serve-style kind (http or grpc; see isServeRoleKind)
//   - lifecycle == "active"
//   - File does not start with "examples/" (platform-scope only)
func isActiveServeStylePlatformContract(c *metadata.ContractMeta) bool {
	return c != nil &&
		isServeRoleKind(c.Kind) &&
		c.Lifecycle == lifecycleActive &&
		!strings.HasPrefix(c.File, examplesPathPrefix)
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
