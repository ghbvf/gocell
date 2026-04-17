// Package governance implements validation rules for GoCell metadata.
// It checks referential integrity, topological legality, verify closure,
// format compliance, and advisory warnings across the parsed ProjectMeta.
//
// Design ref: kubernetes apimachinery field/errors.go — typed error classification
// and error accumulation pattern; diverges by using simple string field paths
// instead of K8s field.Path linked lists.
package governance

import (
	"os"
	"time"

	"github.com/ghbvf/gocell/kernel/metadata"
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
	Code      string // e.g., "REF-01", "TOPO-03"
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
	root       string                            // project root for file existence checks
	now        func() time.Time                  // clock function (injectable for tests)
	fileExists func(path string) bool            // file existence check (injectable for tests)
	readFile   func(path string) ([]byte, error) // file reader (injectable for tests)
	actorSet   map[string]bool                   // pre-built set of external actor IDs
}

// NewValidator creates a Validator for the given parsed project metadata.
// If project is nil, an empty ProjectMeta is used to avoid nil-pointer panics.
func NewValidator(project *metadata.ProjectMeta, root string) *Validator {
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
	return &Validator{
		locator: locator{project: project},
		root:    root,
		now:     time.Now,
		fileExists: func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		},
		readFile: os.ReadFile,
		actorSet: actorSet,
	}
}

// Validate runs all rules and returns all findings.
func (v *Validator) Validate() []ValidationResult {
	var results []ValidationResult
	for _, rule := range v.rules() {
		results = append(results, rule()...)
	}
	return results
}

// ValidateFailFast runs the same rules as Validate but returns as soon as
// any rule produces a SeverityError result. Warnings do not trigger the
// bailout. This is the true short-circuit path for CI pipelines — unlike
// a Validate() caller that filters downstream, no subsequent rule runs.
//
// When no errors are found, the return value contains every rule's warnings
// in the same order as Validate() would.
func (v *Validator) ValidateFailFast() []ValidationResult {
	var results []ValidationResult
	for _, rule := range v.rules() {
		r := rule()
		results = append(results, r...)
		if HasErrors(r) {
			return results
		}
	}
	return results
}

// rules returns the list of rule methods in the same order Validate runs
// them. Used by both Validate (for full evaluation) and ValidateFailFast
// (for short-circuit). Keeping this list in one place is what makes the
// two entry points provably equivalent on the happy path.
func (v *Validator) rules() []func() []ValidationResult {
	return []func() []ValidationResult{
		v.validateREF01, v.validateREF02, v.validateREF03, v.validateREF04,
		v.validateREF05, v.validateREF06, v.validateREF07, v.validateREF08,
		v.validateREF09, v.validateREF10, v.validateREF11, v.validateREF12,
		v.validateREF13, v.validateREF14, v.validateREF15, v.validateREF16,
		v.validateTOPO01, v.validateTOPO02, v.validateTOPO03, v.validateTOPO04,
		v.validateTOPO05, v.validateTOPO06, v.validateTOPO07, v.validateTOPO08,
		v.validateVERIFY01, v.validateVERIFY02, v.validateVERIFY03,
		v.validateVERIFY04, v.validateVERIFY05,
		v.validateFMT01, v.validateFMT02, v.validateFMT03, v.validateFMT04,
		v.validateFMT05, v.validateFMT06, v.validateFMT07, v.validateFMT08,
		v.validateFMT09, v.validateFMT10, v.validateFMT11, v.validateFMT12,
		v.validateFMT13, v.validateFMT14, v.validateFMT15,
		v.validateADV01, v.validateADV03, v.validateADV04,
		v.validateOUTGUARD01,
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
