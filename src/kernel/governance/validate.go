// Package governance implements validation rules for GoCell metadata.
// It checks referential integrity, topological legality, verify closure,
// format compliance, and advisory warnings across the parsed ProjectMeta.
//
// Design ref: kubernetes apimachinery field/errors.go — typed error classification
// and error accumulation pattern; diverges by using simple string field paths
// instead of K8s field.Path linked lists.
package governance

import "github.com/ghbvf/gocell/kernel/metadata"

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
	Code      string            // e.g., "REF-01", "TOPO-03"
	Severity  Severity          //nolint:unused
	IssueType IssueType         //nolint:unused
	File      string            // YAML file path
	Field     string            // field path within YAML, e.g. "contractUsages[0].role"
	Message   string            //nolint:unused
	Details   map[string]string //nolint:unused
}

// Validator runs all validation rules against a parsed project.
type Validator struct {
	project *metadata.ProjectMeta
	root    string // project root for file existence checks
}

// NewValidator creates a Validator for the given parsed project metadata.
func NewValidator(project *metadata.ProjectMeta, root string) *Validator {
	return &Validator{project: project, root: root}
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

	// Topological rules
	results = append(results, v.validateTOPO01()...)
	results = append(results, v.validateTOPO02()...)
	results = append(results, v.validateTOPO03()...)
	results = append(results, v.validateTOPO04()...)
	results = append(results, v.validateTOPO05()...)
	results = append(results, v.validateTOPO06()...)

	// Verify closure rules
	results = append(results, v.validateVERIFY01()...)
	results = append(results, v.validateVERIFY02()...)
	results = append(results, v.validateVERIFY03()...)

	// Format compliance rules
	results = append(results, v.validateFMT01()...)
	results = append(results, v.validateFMT02()...)
	results = append(results, v.validateFMT03()...)
	results = append(results, v.validateFMT04()...)
	results = append(results, v.validateFMT05()...)

	// Advisory rules
	results = append(results, v.validateADV01()...)
	results = append(results, v.validateADV02()...)

	return results
}

// HasErrors returns true if any result has SeverityError.
func (v *Validator) HasErrors(results []ValidationResult) bool {
	for i := range results {
		if results[i].Severity == SeverityError {
			return true
		}
	}
	return false
}

// Errors returns only error-severity results.
func (v *Validator) Errors(results []ValidationResult) []ValidationResult {
	var out []ValidationResult
	for i := range results {
		if results[i].Severity == SeverityError {
			out = append(out, results[i])
		}
	}
	return out
}

// Warnings returns only warning-severity results.
func (v *Validator) Warnings(results []ValidationResult) []ValidationResult {
	var out []ValidationResult
	for i := range results {
		if results[i].Severity == SeverityWarning {
			out = append(out, results[i])
		}
	}
	return out
}
