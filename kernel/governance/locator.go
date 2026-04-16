package governance

import "github.com/ghbvf/gocell/kernel/metadata"

// locator provides position-enriched ValidationResult construction. It is
// embedded into Validator and DependencyChecker so both share a single
// implementation of locate/newResult — one copy, not two.
//
// The embedded form also promotes the `project` field, which is why existing
// rule code continues to read `v.project.Cells` / `dc.project.Slices` without
// changes: the outer struct no longer declares `project` directly; it lifts
// the value off the embedded locator.
type locator struct {
	project *metadata.ProjectMeta
}

// locate returns the 1-based (line, column) of `field` inside `file` using
// the yaml.Node cache captured by the parser. Returns (0, 0) when any
// precondition is missing (no FileNodes, no matching file, unresolvable path).
// Rules should prefer newResult, which wraps this call.
func (l *locator) locate(file, field string) (line, col int) {
	if file == "" || field == "" {
		return 0, 0
	}
	if l.project == nil || l.project.FileNodes == nil {
		return 0, 0
	}
	n, ok := l.project.FileNodes[file]
	if !ok || n == nil {
		return 0, 0
	}
	pos := metadata.Locate(n, field)
	return pos.Line, pos.Column
}

// newResult constructs a ValidationResult with Line/Column auto-populated
// from the yaml.Node cache. Rule implementations should prefer this builder
// over struct literals so locations stay consistent across all findings.
func (l *locator) newResult(code string, sev Severity, typ IssueType, file, field, msg string) ValidationResult {
	line, col := l.locate(file, field)
	return ValidationResult{
		Code:      code,
		Severity:  sev,
		IssueType: typ,
		File:      file,
		Field:     field,
		Message:   msg,
		Line:      line,
		Column:    col,
	}
}

// newScopedResult constructs a ValidationResult for checks that span multiple
// files (or none at all). Pass a virtual scope name (e.g. "project") instead
// of a file path; Line/Column are always zero because there is no single
// location to point at. Renderers distinguish Scope from File so users do
// not mistake the scope label for a jumpable path.
func (l *locator) newScopedResult(code string, sev Severity, typ IssueType, scope, field, msg string) ValidationResult {
	return ValidationResult{
		Code:      code,
		Severity:  sev,
		IssueType: typ,
		Scope:     scope,
		Field:     field,
		Message:   msg,
	}
}
