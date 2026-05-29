package governance

// INVARIANT: SAGA-CONTRACT-STEPS-NONEMPTY-01
//   - INVARIANT: SAGA-CONTRACT-STEP-NAME-VALID-01
//   - INVARIANT: SAGA-CONTRACT-STEP-NAME-UNIQUE-01
//   - INVARIANT: SAGA-CONTRACT-STEP-SCHEMA-REF-01
//   - INVARIANT: SAGA-CONTRACT-COMPENSATION-ORDER-01
//   - INVARIANT: SAGA-CONTRACT-CONSISTENCY-L3-01
//
// This file implements six PhaseBase governance rules validating the format
// of kind=saga contracts. Together they cover the machine-checkable invariants
// of SagaMeta: non-empty steps list, per-step name validity and uniqueness,
// per-step output schema reference, compensation-order enum, and required
// consistencyLevel=L3.
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
//   - Hard 化路径：在 metadata parser Load 时运行 jsonschema.Validate 可将
//     schema enum gate 升级为 parse-time 强制（基础设施已有，参照
//     PROJECTION-CONSISTENCY-01 的相同升级路径）。

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// sagaStepNameRe is the legal step-name shape: a Go-identifier-safe camelCase
// token. It is STRICTER than idutil.IsSafeID (which permits ._:/-) because the
// step name flows through contractgen's goPascalCase into generated Go method
// names (Run<Name> / Compensate<Name>) and type names (<Name>Output); a name
// containing '.', ':' or '/' would emit uncompilable Go. Kept byte-identical to
// the saga.steps[].name pattern in schemas/contract.schema.json.
var sagaStepNameRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]*$`)

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
