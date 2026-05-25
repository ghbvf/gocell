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
	"github.com/ghbvf/gocell/kernel/registry"
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
	// Fix is the remediation guidance for a finding ("how to make this rule
	// pass"). It is REQUIRED and non-empty for all findings regardless of
	// severity: both error and warning results carry structured remediation
	// guidance in this typed field. Splitting Fix out of Message replaces the
	// old "; fix:" Message-substring convention (INV-3 Soft anchor) with a
	// typed field: all findings are constructed exclusively through newError /
	// newWarning / newErrorAt / newScopedError, each of which takes fix as a
	// mandatory positional argument (GOVERNANCE-RULE-ERROR-FIX-FIELD-01).
	// Renderers surface it as a distinct "fix:" line / JSON field, never
	// re-concatenated into Message.
	Fix string
	// Line and Column locate the offending value inside File. They are 1-based
	// (matching yaml.v3) and zero when the position is unknown — e.g. the
	// ProjectMeta was constructed without FileNodes, or the field path cannot
	// be resolved (array index out of range, typo in rule code, etc.). They
	// are always zero when Scope is set.
	Line   int
	Column int
	// Next is the structured remediation disposition (ADR §M3 P-C2): what the
	// M5-HARVEST layer should DO with this finding (block / advisory / autofix /
	// suggest / escalate). Stamped by the engine from the owning Rule.
	Next NextAction
	// Metric is the evaluated distance for the rules that carry one (ADR §M3
	// P-C3): deprecation days remaining, coverage gap, dead-event count, etc. It
	// is nil for the majority of rules — whose findings have no continuous
	// distance — and for rules whose Metric was not applicable to the current
	// project state.
	Metric *float64
}

// Validator runs all validation rules against a parsed project. It embeds
// locator to share locate + the typed constructors (newError/newWarning/newScopedError)
// and to promote the project field so rule code reads v.project.* directly.
//
// Validator is not safe for concurrent ValidateStrict calls. Build one
// Validator per concurrent caller.
//
// runCtx holds the context for the current run() invocation; read by
// ctx-bound detect funcs (VERIFY-06). Validator is not concurrent-safe,
// documented above.
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
	// cells and contracts are the typed registries used by PhaseDep rules.
	cells     *registry.CellRegistry
	contracts *registry.ContractRegistry
	// runCtx holds the context for the current run() invocation; read by
	// ctx-bound detect funcs (VERIFY-06). Set by run() at the start of each
	// invocation; zero value is context.Background().
	runCtx context.Context
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
		readFile:  os.ReadFile,
		actorSet:  actorSet,
		cells:     registry.NewCellRegistry(project),
		contracts: registry.NewContractRegistry(project),
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
// The rule pipeline is driven by allRules (rules_registry.go), filtered by
// phase: base runs PhaseBase+PhaseDep; strict additionally includes
// PhaseStrict. The engine loop (run) stamps each finding's Next/Metric from
// the owning Rule, handles ctx cancellation, and implements the fail-fast
// bailout on the first SeverityError. See engine.go for the loop body.
//
// The error return is non-nil only when ctx.Err() != nil at the time the
// loop is interrupted; it carries the partial findings collected so far so
// callers can distinguish "clean run" from "interrupted run".
//
// Validator is not safe for concurrent ValidateStrict calls. Build one
// Validator per concurrent caller — same expectation as the underlying
// locator and the verifyJourneyRef closure.
func (v *Validator) ValidateStrict(ctx context.Context, strict, failFast bool) ([]ValidationResult, error) {
	phases := []Phase{PhaseBase, PhaseDep}
	if strict {
		phases = append(phases, PhaseStrict)
	}
	return v.run(ctx, rulesForPhases(phases...), failFast)
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
