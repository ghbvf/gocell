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
type ValidationResult struct {
	Code      string // e.g., "REF-01", "TOPO-03"
	Severity  Severity
	IssueType IssueType
	File      string // YAML file path
	Field     string // field path within YAML, e.g. "contractUsages[0].role"
	Message   string
}

// Validator runs all validation rules against a parsed project.
type Validator struct {
	project    *metadata.ProjectMeta
	root       string                              // project root for file existence checks
	now        func() time.Time                    // clock function (injectable for tests)
	fileExists func(path string) bool              // file existence check (injectable for tests)
	readFile   func(path string) ([]byte, error)   // file reader (injectable for tests)
	actorSet   map[string]bool                     // pre-built set of external actor IDs
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
		project: project,
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

	// Reference integrity rules
	results = append(results, v.validateREF01()...)
	results = append(results, v.validateREF02()...)
	results = append(results, v.validateREF03()...)
	results = append(results, v.validateREF04()...)
	results = append(results, v.validateREF05()...)
	results = append(results, v.validateREF06()...)
	results = append(results, v.validateREF07()...)
	results = append(results, v.validateREF08()...)
	results = append(results, v.validateREF09()...)
	results = append(results, v.validateREF10()...)
	results = append(results, v.validateREF11()...)
	results = append(results, v.validateREF12()...)
	results = append(results, v.validateREF13()...)
	results = append(results, v.validateREF14()...)
	results = append(results, v.validateREF15()...)
	results = append(results, v.validateREF16()...)

	// Topological rules
	results = append(results, v.validateTOPO01()...)
	results = append(results, v.validateTOPO02()...)
	results = append(results, v.validateTOPO03()...)
	results = append(results, v.validateTOPO04()...)
	results = append(results, v.validateTOPO05()...)
	results = append(results, v.validateTOPO06()...)
	results = append(results, v.validateTOPO07()...)
	results = append(results, v.validateTOPO08()...)

	// Verify closure rules
	results = append(results, v.validateVERIFY01()...)
	results = append(results, v.validateVERIFY02()...)
	results = append(results, v.validateVERIFY03()...)
	results = append(results, v.validateVERIFY04()...)
	results = append(results, v.validateVERIFY05()...)

	// Format compliance rules
	results = append(results, v.validateFMT01()...)
	results = append(results, v.validateFMT02()...)
	results = append(results, v.validateFMT03()...)
	results = append(results, v.validateFMT04()...)
	results = append(results, v.validateFMT05()...)
	results = append(results, v.validateFMT06()...)
	results = append(results, v.validateFMT07()...)
	results = append(results, v.validateFMT08()...)
	results = append(results, v.validateFMT09()...)
	results = append(results, v.validateFMT10()...)
	results = append(results, v.validateFMT11()...)
	results = append(results, v.validateFMT12()...)
	results = append(results, v.validateFMT13()...)
	results = append(results, v.validateFMT14()...)
	results = append(results, v.validateFMT15()...)

	// Advisory rules
	results = append(results, v.validateADV01()...)
	results = append(results, v.validateADV03()...)
	results = append(results, v.validateADV04()...)

	return results
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
