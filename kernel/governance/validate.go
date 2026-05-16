// Package governance implements validation rules for GoCell metadata.
// It checks referential integrity, topological legality, verify closure,
// format compliance, and advisory warnings across the parsed ProjectMeta.
//
// Design ref: kubernetes apimachinery field/errors.go — typed error classification
// and error accumulation pattern; diverges by using simple string field paths
// instead of K8s field.Path linked lists.
package governance

import (
	"context"
	"os"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/verify"
)

// Severity of a validation result.
type Severity string

const (
	SeverityError   Severity = "error"   // blocking
	SeverityWarning Severity = "warning" // advisory
)

// IssueType classifies the kind of validation issue.
// ref: kubernetes apimachinery field/errors.go — typed error classification
type IssueType string

const (
	IssueRequired    IssueType = "required"
	IssueInvalid     IssueType = "invalid"
	IssueRefNotFound IssueType = "referenceNotFound"
	IssueMismatch    IssueType = "mismatch"
	IssueForbidden   IssueType = "forbidden"
	IssueDuplicate   IssueType = "duplicate"
)

// ValidationResult represents a single validation finding.
//
// File and Scope are mutually exclusive:
//   - File identifies a real YAML file; the finding points at a concrete
//     location (File plus Line/Column) that an IDE can open.
//   - Scope names a virtual domain ("project", "cross-file", ...) used by
//     checks that inspect relationships across multiple files and therefore
//     cannot pin the issue to a single file position. CLI renderers must
//     avoid showing Scope with a "file:line:col" prefix because doing so
//     would invite users to try (and fail) to jump to it.
type ValidationResult struct {
	Code      RuleCode // e.g., codeREF01, codeTOPO03
	Severity  Severity
	IssueType IssueType
	File      string // YAML file path; empty when Scope is set
	Scope     string // virtual scope name; empty when File is set
	Field     string // field path within YAML, e.g. "contractUsages[0].role"
	Message   string
	// Line and Column locate the offending value inside File. They are 1-based
	// (matching yaml.v3) and zero when the position is unknown — e.g. the
	// ProjectMeta was constructed without FileNodes, or the field path cannot
	// be resolved (array index out of range, typo in rule code, etc.). They
	// are always zero when Scope is set.
	Line   int
	Column int
}

// Validator runs all validation rules against a parsed project. It embeds
// locator to share locate/newResult with DependencyChecker and to promote
// the project field so existing rule code keeps using v.project.* directly.
type Validator struct {
	locator
	root             string                            // project root for file existence checks
	clk              clock.Clock                       // clock (injectable for tests; production uses clock.Real())
	fileExists       func(path string) bool            // file existence check (injectable for tests)
	readFile         func(path string) ([]byte, error) // file reader (injectable for tests)
	actorSet         map[string]bool                   // pre-built set of external actor IDs from actors.yaml (membership = external)
	verifyJourneyRef func(
		ctx context.Context,
		j *metadata.JourneyMeta,
		ref string,
	) (verify.TestResult, []error)
}

// NewValidator creates a Validator for the given parsed project metadata.
// If project is nil, an empty ProjectMeta is used to avoid nil-pointer panics.
func NewValidator(project *metadata.ProjectMeta, root string, clk clock.Clock) *Validator {
	clock.MustHaveClock(clk, "governance.NewValidator")
	if project == nil {
		project = &metadata.ProjectMeta{
			Cells:      map[string]*metadata.CellMeta{},
			Slices:     map[string]*metadata.SliceMeta{},
			Contracts:  map[string]*metadata.ContractMeta{},
			Journeys:   map[string]*metadata.JourneyMeta{},
			Assemblies: map[string]*metadata.AssemblyMeta{},
		}
	}
	actorSet := make(map[string]bool, len(project.Actors))
	for _, a := range project.Actors {
		actorSet[a.ID] = true
	}
	validator := &Validator{
		locator: locator{project: project},
		root:    root,
		clk:     clk,
		fileExists: func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		},
		readFile: os.ReadFile,
		actorSet: actorSet,
	}
	if root != "" {
		runner := verify.NewRunner(project, root)
		validator.verifyJourneyRef = runner.RunJourneyCheckRef
	}
	return validator
}

// ValidateStrict is the single entry point for governance validation. strict
// and failFast are orthogonal flags forming a 2x2 matrix:
//
//   - strict=false, failFast=false → run all base rules, collect every result
//   - strict=false, failFast=true  → run base rules, stop at the first error
//   - strict=true,  failFast=false → run base + strict-only rules, collect all
//   - strict=true,  failFast=true  → run base + strict rules, stop on error
//
// rules() and strictRules(ctx) stay separate functions because their closure
// shapes differ (strictRules captures ctx for VERIFY-06); ValidateStrict
// concats them at the dispatch site so there is exactly one ctx-cancel /
// fail-fast loop body covering both halves of the pipeline.
//
// The error return is non-nil only when ctx.Err() != nil at the time the
// loop is interrupted; it carries the partial findings collected so far so
// callers can distinguish "clean run" from "interrupted run".
//
// Validator is not safe for concurrent ValidateStrict calls. Build one
// Validator per concurrent caller — same expectation as the underlying
// locator and the verifyJourneyRef closure.
//
// archtest GOVERNANCE-RULES-REGISTRATION-GUARD-01 (tools/archtest/
// governance_rules_invariants_test.go) reflects over *Validator at build
// time to confirm every validate* method with the rule signature is
// reachable from rules() or strictRules(); forgetting to register a new
// rule fails CI.
func (v *Validator) ValidateStrict(ctx context.Context, strict, failFast bool) ([]ValidationResult, error) {
	pipeline := v.rules()
	if strict {
		pipeline = append(pipeline, v.strictRules(ctx)...)
	}
	var results []ValidationResult
	for _, rule := range pipeline {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		r := rule()
		results = append(results, r...)
		if failFast && HasErrors(r) {
			return results, nil
		}
	}
	return results, nil
}

// rules returns the base rule pipeline in the order ValidateStrict runs them.
// Every entry is a zero-arg closure; ctx-bound work (VERIFY-06's
// verifyJourneyRef subprocess, runGit shell-outs) lives in strictRules,
// which captures ctx, so this list keeps the pure-memory invariant.
//
// archtest GOVERNANCE-RULES-REGISTRATION-GUARD-01 reflects over *Validator's
// method set and diffs against the names referenced here plus in
// strictRules(); a validate* method that returns []ValidationResult but is
// not referenced from either function fails CI immediately. The check is
// the single source preventing "method exists but never runs" drift.
//
// ref: kubernetes apimachinery validation/field/errors.go (pure-memory rules
// with no ctx); opentofu internal/command/validate.go (top-level aggregator
// threads ctx).
func (v *Validator) rules() []func() []ValidationResult {
	return []func() []ValidationResult{
		v.validateREF01, v.validateREF02, v.validateREF03, v.validateREF04,
		v.validateREF05, v.validateREF06, v.validateREF07, v.validateREF08,
		v.validateREF09, v.validateREF10, v.validateREF11, v.validateREF12,
		v.validateREF13, v.validateREF14, v.validateREF15, v.validateREF16,
		v.validateREF17,
		v.validateTOPO01, v.validateTOPO02, v.validateTOPO03, v.validateTOPO04,
		v.validateTOPO05, v.validateTOPO06, v.validateTOPO07, v.validateTOPO08,
		v.validateTOPO09,
		v.validateVERIFY01, v.validateVERIFY02, v.validateVERIFY03,
		v.validateVERIFY04, v.validateVERIFY05,
		v.validateFMT01, v.validateFMT02, v.validateFMT03, v.validateFMT04,
		v.validateFMT05, v.validateFMT06, v.validateFMT07, v.validateFMT08,
		v.validateFMT09, v.validateFMT10, v.validateFMT11, v.validateFMT12,
		v.validateFMT13, v.validateFMT14, v.validateFMT15, v.validateFMT24, v.validateFMT26,
		v.validateFMT27, v.validateFMT28, v.validateFMT29, v.validateFMT30, v.validateFMT31,
		v.validateFMT32,
		v.validateFMTA1,
		v.validateFMTC1,
		v.validateADV01, v.validateADV03, v.validateADV04, v.validateADV05,
		v.validateADV06,
		v.validateOUTGUARD01,
		v.validateSliceConsistency,
		v.validateFMTRequestStrict01,
		v.validateFMTContractDirIDMatch01,
		v.validateStatusBoardStateEnum01,
		v.validateContractDeprecatedCleanup01,
		v.validateFMTInputConstraint01,
		v.validateCONTRACTCONSISTENCYEMIT01,
	}
}

// HasErrors returns true if any result has SeverityError.
func HasErrors(results []ValidationResult) bool {
	for i := range results {
		if results[i].Severity == SeverityError {
			return true
		}
	}
	return false
}

// FilterErrors returns only error-severity results.
func FilterErrors(results []ValidationResult) []ValidationResult {
	var out []ValidationResult
	for i := range results {
		if results[i].Severity == SeverityError {
			out = append(out, results[i])
		}
	}
	return out
}

// FilterWarnings returns only warning-severity results.
func FilterWarnings(results []ValidationResult) []ValidationResult {
	var out []ValidationResult
	for i := range results {
		if results[i].Severity == SeverityWarning {
			out = append(out, results[i])
		}
	}
	return out
}
