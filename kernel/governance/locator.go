package governance

import (
	"path"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
)

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
// precondition is missing (no file nodes, no matching file, unresolvable path).
// Rules should prefer newResult, which wraps this call.
func (l *locator) locate(file, field string) (line, col int) {
	if file == "" || field == "" {
		return 0, 0
	}
	if l.project == nil {
		return 0, 0
	}
	file = l.resolveFile(file)
	pos := l.project.Locate(file, field)
	return pos.Line, pos.Column
}

// newResult constructs a ValidationResult with Line/Column auto-populated
// from the yaml.Node cache. Rule implementations should prefer this builder
// over struct literals so locations stay consistent across all findings.
func (l *locator) newResult(code string, sev Severity, typ IssueType, file, field, msg string) ValidationResult {
	file = l.resolveFile(file)
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

func (l *locator) resolveFile(file string) string {
	if l == nil || l.project == nil || file == "" {
		return file
	}
	clean := path.Clean(strings.ReplaceAll(file, "\\", "/"))
	for _, resolver := range canonicalFileResolvers {
		id, ok := resolver.match(clean)
		if !ok {
			continue
		}
		if actual := resolver.file(l.project, id); actual != "" {
			return actual
		}
		return file
	}
	return file
}

type canonicalFileResolver struct {
	match func(string) (string, bool)
	file  func(*metadata.ProjectMeta, string) string
}

var canonicalFileResolvers = []canonicalFileResolver{
	{match: canonicalCellID, file: cellMetaFile},
	{match: canonicalSliceKey, file: sliceMetaFile},
	{match: canonicalContractID, file: contractMetaFile},
	{match: canonicalJourneyID, file: journeyMetaFile},
	{match: canonicalAssemblyID, file: assemblyMetaFile},
}

func cellMetaFile(project *metadata.ProjectMeta, id string) string {
	if c := project.Cells[id]; c != nil {
		return c.File
	}
	return ""
}

func sliceMetaFile(project *metadata.ProjectMeta, key string) string {
	if s := project.Slices[key]; s != nil {
		return s.File
	}
	return ""
}

func contractMetaFile(project *metadata.ProjectMeta, id string) string {
	if c := project.Contracts[id]; c != nil {
		return c.File
	}
	return ""
}

func journeyMetaFile(project *metadata.ProjectMeta, id string) string {
	if j := project.Journeys[id]; j != nil {
		return j.File
	}
	return ""
}

func assemblyMetaFile(project *metadata.ProjectMeta, id string) string {
	if a := project.Assemblies[id]; a != nil {
		return a.File
	}
	return ""
}

func canonicalCellID(file string) (string, bool) {
	parts := strings.Split(file, "/")
	if len(parts) == 3 && parts[0] == "cells" && parts[2] == "cell.yaml" {
		return parts[1], true
	}
	return "", false
}

func canonicalSliceKey(file string) (string, bool) {
	parts := strings.Split(file, "/")
	if len(parts) == 5 && parts[0] == "cells" && parts[2] == "slices" && parts[4] == "slice.yaml" {
		return parts[1] + "/" + parts[3], true
	}
	return "", false
}

func canonicalContractID(file string) (string, bool) {
	parts := strings.Split(file, "/")
	if len(parts) < 5 || parts[0] != "contracts" || parts[len(parts)-1] != "contract.yaml" {
		return "", false
	}
	return strings.Join(parts[1:len(parts)-1], "."), true
}

func canonicalJourneyID(file string) (string, bool) {
	parts := strings.Split(file, "/")
	if len(parts) != 2 || parts[0] != "journeys" {
		return "", false
	}
	name := parts[1]
	if !strings.HasPrefix(name, "J-") || !strings.HasSuffix(name, ".yaml") {
		return "", false
	}
	return strings.TrimSuffix(name, ".yaml"), true
}

func canonicalAssemblyID(file string) (string, bool) {
	parts := strings.Split(file, "/")
	if len(parts) == 3 && parts[0] == "assemblies" && parts[2] == "assembly.yaml" {
		return parts[1], true
	}
	return "", false
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
