package governance

// invariants:
//   - INVARIANT: SAGA-CONTRACT-BLOCK-PRESENT-01
//   - INVARIANT: SAGA-CONTRACT-STEPS-NONEMPTY-01
//   - INVARIANT: SAGA-CONTRACT-STEP-NAME-VALID-01
//   - INVARIANT: SAGA-CONTRACT-STEP-NAME-UNIQUE-01
//   - INVARIANT: SAGA-CONTRACT-STEP-SCHEMA-REF-01
//   - INVARIANT: SAGA-CONTRACT-COMPENSATION-ORDER-01
//   - INVARIANT: SAGA-CONTRACT-CONSISTENCY-L3-01
//   - INVARIANT: SAGA-CONTRACT-RETRY-TIMEOUT-01
//   - INVARIANT: SAGA-CELL-LEVEL-L3-DECLARE-01
//
// This file implements nine PhaseBase governance rules. The first eight
// validate the format of kind=saga contracts and cover the machine-checkable
// invariants of SagaMeta: presence of the saga block, non-empty steps list,
// per-step name validity and uniqueness, per-step output schema reference,
// compensation-order enum, required consistencyLevel=L3, and numeric validity
// of retry policies and timeout durations. The ninth,
// SAGA-CELL-LEVEL-L3-DECLARE-01, is a cell-level topology rule (not contract
// format): a slice with contractUsage role=orchestrate must belong to a cell
// declared consistencyLevel=L3.
//
// AI-robust evaluation (honest grading — same class as PROJECTION-CONSISTENCY-01):
//
//   - Governance rules (Medium, this file): the real enforcement. `gocell
//     validate` runs in CI and blocks any saga contract that violates the
//     rules below, including in-memory ProjectMeta fixtures that never pass
//     through the YAML parser.
//
//   - Schema enum (documentation + test layer, NOT a parse-time gate): the
//     saga if/then blocks in contract.schema.json restrict the permitted
//     values for compensationOrder and consistencyLevel. The metadata parser
//     does NOT run jsonschema.Validate at load time, so the schema enum is
//     enforced by contract_schema_test.go and serves as IDE/tooling
//     documentation — these governance rules are the real CI gate that also
//     covers in-memory fixtures and non-codegen contracts.
//
//   - Blind spots covered: in-memory ContractMeta with Kind=="saga" and a
//     malformed SagaMeta (zero steps, bad step names, wrong consistency) that
//     never passed through the YAML parser is reported explicitly, preventing
//     programmatic fixtures from silently bypassing format requirements.
//
//   - SAGA-CONTRACT-RETRY-TIMEOUT-01 is the MEDIUM backstop covering
//     in-memory ProjectMeta fixtures and codegen:false contracts. The
//     contractgen builder delegation is the Hard PRIMARY gate — the same
//     codegen-funnel archetype that PROJECTION-CONSISTENCY-01 adopted in
//     gh #960. NB: gh #960 evaluated "parser load-time jsonschema.Validate" as
//     the proposed Hard path and REJECTED it — a parse-time validator is a
//     runtime guard (Medium) that does not cover the in-memory vector. The Hard
//     gate for contract-shape constraints is the contractgen codegen funnel,
//     not the schema. There is no remaining parse-time-jsonschema upgrade to
//     track for saga.

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/saga"
)

// sagaStepNameRe is the legal step-name shape: a Go-identifier-safe camelCase
// token. It is STRICTER than idutil.IsSafeID (which permits ._:/-) because the
// step name flows through contractgen's goPascalCase into generated Go method
// names (Run<Name> / Compensate<Name>) and type names (<Name>Output); a name
// containing '.', ':' or '/' would emit uncompilable Go. Single-source from
// metadata.SagaStepNamePattern (byte-identical to the saga.steps[].name
// pattern in schemas/contract.schema.json).
var sagaStepNameRe = regexp.MustCompile(metadata.SagaStepNamePattern)

// validateSAGACONTRACTSTEPSNONEMPTY01 enforces SAGA-CONTRACT-STEPS-NONEMPTY-01:
// every saga contract must declare at least one step under saga.steps[].
//
// A saga with an empty steps list cannot execute any forward work and is
// structurally incomplete; it would silently succeed with no side-effects
// while appearing to orchestrate something.
func (v *Validator) validateSAGACONTRACTSTEPSNONEMPTY01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != string(cellvocab.ContractSaga) || c.Saga == nil {
			continue
		}
		if len(c.Saga.Steps) == 0 {
			results = append(results, v.newError(
				codeSAGACONTRACTSTEPSNONEMPTY01, IssueRequired,
				contractFile(c),
				"saga.steps",
				fmt.Sprintf("saga contract %q has no steps; saga.steps must be non-empty", c.ID),
				"declare at least one step under saga.steps[]",
			))
		}
	}
	return results
}

// validateSAGACONTRACTSTEPNAMEVALID01 enforces SAGA-CONTRACT-STEP-NAME-VALID-01:
// every step in a saga contract must have a name matching sagaStepNameRe
// (^[a-zA-Z][a-zA-Z0-9]*$) — a Go-identifier-safe camelCase token.
//
// This is the CI gate that keeps step names codegen-safe: contractgen turns
// each name into generated Go identifiers (Run<Name>, Compensate<Name>,
// <Name>Output) via goPascalCase, which does not sanitize '.', ':' or '/'. A
// name like "charge.v2" would generate uncompilable Go, so it is rejected here
// (stricter than idutil.IsSafeID; kept in lockstep with the schema pattern).
func (v *Validator) validateSAGACONTRACTSTEPNAMEVALID01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != string(cellvocab.ContractSaga) || c.Saga == nil {
			continue
		}
		results = append(results, v.checkSagaStepNames(c)...)
	}
	return results
}

// checkSagaStepNames is a helper that checks step name validity for a single
// contract, keeping validateSAGACONTRACTSTEPNAMEVALID01 within complexity <= 15.
func (v *Validator) checkSagaStepNames(c *metadata.ContractMeta) []ValidationResult {
	var results []ValidationResult
	for i, step := range c.Saga.Steps {
		if !sagaStepNameRe.MatchString(step.Name) {
			results = append(results, v.newError(
				codeSAGACONTRACTSTEPNAMEVALID01, IssueInvalid,
				contractFile(c),
				fmt.Sprintf("saga.steps[%d].name", i),
				fmt.Sprintf(
					"saga contract %q step[%d] has invalid name %q; "+
						"step names must match ^[a-zA-Z][a-zA-Z0-9]*$ (codegen turns them into Go identifiers)",
					c.ID, i, step.Name,
				),
				"use a camelCase step name (letters and digits only, starting with a letter), e.g. reserveInventory",
			))
		}
	}
	return results
}

// validateSAGACONTRACTSTEPNAMEUNIQUE01 enforces SAGA-CONTRACT-STEP-NAME-UNIQUE-01:
// step names must be unique within a saga contract.
//
// Duplicate step names make it impossible to unambiguously reference a step
// in compensation logs, metrics, or runtime state machines.
func (v *Validator) validateSAGACONTRACTSTEPNAMEUNIQUE01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != string(cellvocab.ContractSaga) || c.Saga == nil {
			continue
		}
		results = append(results, v.checkSagaStepNamesUnique(c)...)
	}
	return results
}

// checkSagaStepNamesUnique detects duplicate step names within a single saga
// contract, keeping validateSAGACONTRACTSTEPNAMEUNIQUE01 within complexity <= 15.
func (v *Validator) checkSagaStepNamesUnique(c *metadata.ContractMeta) []ValidationResult {
	seen := make(map[string]struct{}, len(c.Saga.Steps))
	var results []ValidationResult
	for i, step := range c.Saga.Steps {
		if step.Name == "" {
			continue // empty name is already reported by STEP-NAME-VALID-01
		}
		if _, dup := seen[step.Name]; dup {
			results = append(results, v.newError(
				codeSAGACONTRACTSTEPNAMEUNIQUE01, IssueDuplicate,
				contractFile(c),
				fmt.Sprintf("saga.steps[%d].name", i),
				fmt.Sprintf(
					"saga contract %q has duplicate step name %q at steps[%d]",
					c.ID, step.Name, i,
				),
				"rename the duplicate step so each step name is unique within the saga",
			))
			continue
		}
		seen[step.Name] = struct{}{}
	}
	return results
}

// validateSAGACONTRACTSTEPSCHEMAREF01 enforces SAGA-CONTRACT-STEP-SCHEMA-REF-01:
// every step in a saga contract must declare a non-empty output schema $ref.
//
// The output schema is the typed contract between steps: it is what the
// runtime passes as prevState to the next step's Run function. Missing it
// makes the step's output contract unverifiable by codegen and tooling.
func (v *Validator) validateSAGACONTRACTSTEPSCHEMAREF01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != string(cellvocab.ContractSaga) || c.Saga == nil {
			continue
		}
		results = append(results, v.checkSagaStepOutputRefs(c)...)
	}
	return results
}

// checkSagaStepOutputRefs checks that each step has a non-empty output ref,
// keeping validateSAGACONTRACTSTEPSCHEMAREF01 within complexity <= 15.
func (v *Validator) checkSagaStepOutputRefs(c *metadata.ContractMeta) []ValidationResult {
	var results []ValidationResult
	for i, step := range c.Saga.Steps {
		if strings.TrimSpace(step.Output) == "" {
			results = append(results, v.newError(
				codeSAGACONTRACTSTEPSCHEMAREF01, IssueRequired,
				contractFile(c),
				fmt.Sprintf("saga.steps[%d].output", i),
				fmt.Sprintf(
					"saga contract %q step[%d] %q has no output schema $ref",
					c.ID, i, step.Name,
				),
				"add an output schema $ref (path relative to this contract.yaml), e.g. reserve-inventory.output.schema.json",
			))
		}
	}
	return results
}

// validateSAGACONTRACTCOMPENSATIONORDER01 enforces SAGA-CONTRACT-COMPENSATION-ORDER-01:
// if compensationOrder is set, it must equal "reverse" (the only supported value).
//
// An empty compensationOrder is valid and treated as "reverse" at runtime.
// Any other non-empty value is a misconfiguration that would cause silent
// misinterpretation if the runtime added a second strategy in the future.
func (v *Validator) validateSAGACONTRACTCOMPENSATIONORDER01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != string(cellvocab.ContractSaga) || c.Saga == nil {
			continue
		}
		if c.Saga.CompensationOrder != "" && c.Saga.CompensationOrder != "reverse" {
			results = append(results, v.newError(
				codeSAGACONTRACTCOMPENSATIONORDER01, IssueInvalid,
				contractFile(c),
				"saga.compensationOrder",
				fmt.Sprintf(
					"saga contract %q has invalid compensationOrder %q; only \"reverse\" is supported",
					c.ID, c.Saga.CompensationOrder,
				),
				"set compensationOrder to reverse (the only supported value)",
			))
		}
	}
	return results
}

// validateSAGACONTRACTCONSISTENCYL301 enforces SAGA-CONTRACT-CONSISTENCY-L3-01:
// saga contracts must declare consistencyLevel=L3 (WorkflowEventual).
//
// Saga orchestration is inherently cross-cell and eventual: the coordinator
// drives a multi-step workflow through event publishing and step-service
// invocations. Declaring L0/L1/L2 implies guarantees the saga pattern
// cannot provide (local-only or single-cell transactional semantics).
// L4 is not permitted either — saga is a WorkflowEventual (L3) pattern, not
// a DeviceLatent (L4) pattern.
func (v *Validator) validateSAGACONTRACTCONSISTENCYL301() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != string(cellvocab.ContractSaga) || c.Saga == nil {
			continue
		}
		if c.ConsistencyLevel != "L3" {
			results = append(results, v.newError(
				codeSAGACONTRACTCONSISTENCYL301, IssueInvalid,
				contractFile(c),
				"consistencyLevel",
				fmt.Sprintf(
					"saga contract %q declares consistencyLevel=%q; "+
						"saga contracts require L3 (WorkflowEventual)",
					c.ID, c.ConsistencyLevel,
				),
				"set consistencyLevel to L3 (saga orchestration is WorkflowEventual)",
			))
		}
	}
	return results
}

// validateSAGACONTRACTBLOCKPRESENT01 enforces SAGA-CONTRACT-BLOCK-PRESENT-01:
// every contract with kind=saga must have a non-nil saga: block.
//
// A kind=saga contract without a saga block is structurally incomplete: the
// coordinator cannot derive any step definitions, retry policies, or timeout
// from it, and contractgen cannot generate any typed saga glue.
func (v *Validator) validateSAGACONTRACTBLOCKPRESENT01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind == string(cellvocab.ContractSaga) && c.Saga == nil {
			results = append(results, v.newError(
				codeSAGACONTRACTBLOCKPRESENT01, IssueRequired,
				contractFile(c),
				"saga",
				fmt.Sprintf(
					"saga contract %q has no saga block; kind=saga requires a saga: section",
					c.ID,
				),
				"add a saga: block with at least one step, or change kind",
			))
		}
	}
	return results
}

// validateSAGACONTRACTRETRYTIMEOUT01 enforces SAGA-CONTRACT-RETRY-TIMEOUT-01:
// validates saga.retries, each step's retries, and saga/step timeout values.
//
// This is the MEDIUM backstop covering in-memory ProjectMeta fixtures and
// codegen:false contracts. The contractgen builder delegation is the Hard
// PRIMARY gate (the codegen-funnel archetype; gh #960 rejected parse-time
// jsonschema.Validate as a Medium runtime guard — see the file godoc above).
//
// Delegation: numeric checks are delegated to kernel/saga.RetryPolicy.Validate
// (MaxAttempts>=0, intervals>=0, MaxInterval>=BaseInterval when both non-zero).
// Empty duration strings are allowed (meaning "inherit"). Negative durations
// are rejected. MaxAttempts==0 means "inherit" and is always valid.
func (v *Validator) validateSAGACONTRACTRETRYTIMEOUT01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != string(cellvocab.ContractSaga) || c.Saga == nil {
			continue
		}
		results = append(results, v.checkSagaRetryTimeout(c)...)
	}
	return results
}

// checkSagaRetryTimeout validates retry policies and timeouts for a single
// saga contract, keeping validateSAGACONTRACTRETRYTIMEOUT01 within complexity <= 15.
func (v *Validator) checkSagaRetryTimeout(c *metadata.ContractMeta) []ValidationResult {
	var results []ValidationResult
	file := contractFile(c)

	// Validate saga-level timeout.
	if _, timeoutErr := parseSagaDuration(c.Saga.Timeout); timeoutErr != nil {
		results = append(results, v.newError(
			codeSAGACONTRACTRETRYTIMEOUT01, IssueInvalid,
			file, "saga.timeout",
			fmt.Sprintf("saga contract %q has invalid saga.timeout %q: %v", c.ID, c.Saga.Timeout, timeoutErr),
			"set saga.timeout to a valid Go duration string (e.g. \"30s\"), or omit to inherit",
		))
	}

	// Validate saga-level retries.
	if c.Saga.Retries != nil {
		results = append(results, v.checkSagaRetryMeta(file, c.ID, "saga.retries", c.Saga.Retries)...)
	}

	// Validate per-step timeouts and retries.
	for i, step := range c.Saga.Steps {
		stepField := fmt.Sprintf("saga.steps[%d]", i)
		if _, stepTimeoutErr := parseSagaDuration(step.Timeout); stepTimeoutErr != nil {
			results = append(results, v.newError(
				codeSAGACONTRACTRETRYTIMEOUT01, IssueInvalid,
				file, stepField+".timeout",
				fmt.Sprintf("saga contract %q step[%d] %q has invalid timeout %q: %v", c.ID, i, step.Name, step.Timeout, stepTimeoutErr),
				"set timeout to a valid Go duration string (e.g. \"10s\"), or omit to inherit",
			))
		}
		if step.Retries != nil {
			results = append(results, v.checkSagaRetryMeta(file, c.ID, fmt.Sprintf("%s.retries", stepField), step.Retries)...)
		}
	}
	return results
}

// checkSagaRetryMeta validates a single SagaRetryMeta by delegating to
// kernel/saga.RetryPolicy.Validate. Keeps checkSagaRetryTimeout within
// complexity <= 15.
func (v *Validator) checkSagaRetryMeta(file, contractID, field string, rm *metadata.SagaRetryMeta) []ValidationResult {
	var results []ValidationResult

	base, baseErr := parseSagaDuration(rm.BaseInterval)
	if baseErr != nil {
		results = append(results, v.newError(
			codeSAGACONTRACTRETRYTIMEOUT01, IssueInvalid,
			file, field+".baseInterval",
			fmt.Sprintf("saga contract %q %s.baseInterval %q is not a valid Go duration: %v", contractID, field, rm.BaseInterval, baseErr),
			"set baseInterval to a valid Go duration string (e.g. \"1s\"), or omit to inherit",
		))
	}

	max, maxErr := parseSagaDuration(rm.MaxInterval)
	if maxErr != nil {
		results = append(results, v.newError(
			codeSAGACONTRACTRETRYTIMEOUT01, IssueInvalid,
			file, field+".maxInterval",
			fmt.Sprintf("saga contract %q %s.maxInterval %q is not a valid Go duration: %v", contractID, field, rm.MaxInterval, maxErr),
			"set maxInterval to a valid Go duration string (e.g. \"60s\"), or omit to inherit",
		))
	}

	// Only call RetryPolicy.Validate when both intervals parsed successfully.
	if baseErr == nil && maxErr == nil {
		policy := saga.RetryPolicy{
			MaxAttempts:  rm.MaxAttempts,
			BaseInterval: base,
			MaxInterval:  max,
		}
		if err := policy.Validate(); err != nil {
			results = append(results, v.newError(
				codeSAGACONTRACTRETRYTIMEOUT01, IssueInvalid,
				file, field,
				fmt.Sprintf("saga contract %q %s has invalid retry policy: %v", contractID, field, err),
				"ensure maxAttempts >= 0, intervals >= 0, and maxInterval >= baseInterval when both are set",
			))
		}
	}
	return results
}

// validateSAGACELLLEVELL3DECLARE01 enforces SAGA-CELL-LEVEL-L3-DECLARE-01:
// a slice with contractUsage role=orchestrate must belong to a cell whose
// consistencyLevel is exactly L3 (WorkflowEventual).
//
// The implication is ONE-WAY: orchestrate ⟹ cell.consistencyLevel==L3.
// The inverse is intentionally NOT enforced — L3 cells predate saga orchestration
// (CQRS projections, compliance-tracking cells are L3 without orchestrating any
// saga), so a bidirectional check would cause spurious failures.
//
// If the slice's belongsToCell does not appear in v.project.Cells, this rule
// silently skips that entry. REF-01 (referential integrity) is the correct owner
// of "unknown cell" findings; double-reporting here would confuse operators.
//
// AI-robust evaluation (Medium): gocell validate runs in CI and covers in-memory
// ProjectMeta fixtures (same class as SAGA-CONTRACT-CONSISTENCY-L3-01 and
// PROJECTION-CONSISTENCY-01). The schema enum is documentation only.
func (v *Validator) validateSAGACELLLEVELL3DECLARE01() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		results = append(results, v.checkSliceOrchestrateLevel(s)...)
	}
	return results
}

// checkSliceOrchestrateLevel checks all contractUsages of a single slice for
// the orchestrate-⟹-L3 invariant. Extracted to keep
// validateSAGACELLLEVELL3DECLARE01 within cognitive complexity ≤ 15.
func (v *Validator) checkSliceOrchestrateLevel(s *metadata.SliceMeta) []ValidationResult {
	cell, ok := v.project.Cells[s.BelongsToCell]
	if !ok {
		return nil // REF covers missing cell declarations; don't double-report
	}
	var results []ValidationResult
	for i, cu := range s.ContractUsages {
		if cellvocab.ContractRole(cu.Role) != cellvocab.RoleOrchestrate {
			continue
		}
		// The L3 requirement attaches only to saga contracts. An orchestrate role
		// on a missing or non-saga contract is owned by REF (missing contract) and
		// TOPO-01 (role/kind legality); skip here so a single broken config is
		// reported once at its root, not double-reported — symmetric with the
		// unknown-cell skip above.
		if c, found := v.project.Contracts[cu.Contract]; !found || c.Kind != string(cellvocab.ContractSaga) {
			continue
		}
		if cell.ConsistencyLevel == "L3" {
			continue
		}
		results = append(results, v.newError(
			codeSAGACELLLEVELL3DECLARE01, IssueInvalid,
			sliceFile(s),
			fmt.Sprintf(fieldContractUsagesRoleFmt, i),
			fmt.Sprintf(
				"slice %q (cell %q) has role=orchestrate but cell.consistencyLevel=%q; "+
					"a saga orchestrator cell must be L3 (WorkflowEventual)",
				s.ID, s.BelongsToCell, cell.ConsistencyLevel,
			),
			fmt.Sprintf("set consistencyLevel to L3 in %s", cell.File),
		))
	}
	return results
}

// parseSagaDuration parses a Go duration string from a saga YAML field.
// An empty string is allowed (means "inherit") and returns (0, nil).
// Returns an error for any non-empty string that is not a valid Go duration
// or that parses to a negative value.
func parseSagaDuration(s string) (time.Duration, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("duration %q must be >= 0", s)
	}
	return d, nil
}
