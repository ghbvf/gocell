package scanner

import (
	"fmt"
	"go/parser"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ImportBan describes a rule that forbids importing specific packages.
//
// Use ImportBan when the entire check is "file imports forbidden package X".
// For custom AST patterns (e.g., struct literals, argument values), use
// [EachFile] with [Report] instead.
type ImportBan struct {
	// RuleID is the invariant identifier, e.g. "KERNEL-NO-RUNTIME-01".
	RuleID string
	// Forbidden is the list of import paths that are disallowed.
	Forbidden []string
	// AllowRels lists relative file paths (from module root) that are exempt
	// from the ban. Useful for adapter bridges that must import the forbidden
	// package by design.
	AllowRels []string
	// Hint is an optional message appended to each violation describing the
	// preferred alternative.
	Hint string
}

// detect scans scope for files that import any of b.Forbidden, returning a
// Diagnostic per violation. Files whose relative path appears in b.AllowRels
// are skipped. The returned slice is sorted by (Rel, Line).
func (b ImportBan) detect(s Scope) ([]Diagnostic, error) {
	allowSet := buildAllowSet(b.AllowRels)

	var diags []Diagnostic
	err := eachFile(s, parser.ImportsOnly, func(fc FileContext) error {
		if isAllowed(fc.Rel, allowSet) {
			return nil
		}
		diags = append(diags, b.checkImports(fc)...)
		return nil
	})
	if err != nil {
		return nil, err
	}

	sortDiagnostics(diags)
	return diags, nil
}

// buildAllowSet converts a slice of allowed rels into a set for O(1) lookup.
// Each entry is normalised with filepath.ToSlash(filepath.Clean(r)) to match
// the slash-separated fc.Rel values produced by eachFile on all platforms,
// including Windows where filepath.Join returns backslash-separated paths.
func buildAllowSet(allowRels []string) map[string]struct{} {
	set := make(map[string]struct{}, len(allowRels))
	for _, r := range allowRels {
		set[filepath.ToSlash(filepath.Clean(r))] = struct{}{}
	}
	return set
}

// isAllowed reports whether rel (slash-separated) appears in the allow set.
func isAllowed(rel string, allowSet map[string]struct{}) bool {
	if _, ok := allowSet[rel]; ok {
		return true
	}
	return false
}

// checkImports returns diagnostics for any forbidden imports found in fc.
func (b ImportBan) checkImports(fc FileContext) []Diagnostic {
	var diags []Diagnostic
	for _, imp := range fc.File.Imports {
		importPath := strings.Trim(imp.Path.Value, `"`)
		for _, forbidden := range b.Forbidden {
			if importPath != forbidden {
				continue
			}
			line := fc.Fset.Position(imp.Path.Pos()).Line
			diags = append(diags, Diagnostic{
				Rel:     fc.Rel,
				Line:    line,
				Message: b.buildMessage(importPath),
			})
		}
	}
	return diags
}

// buildMessage formats the violation message, appending b.Hint when non-empty.
func (b ImportBan) buildMessage(importPath string) string {
	msg := fmt.Sprintf("import %q is forbidden", importPath)
	if b.Hint != "" {
		msg += "; " + b.Hint
	}
	return msg
}

// Run executes the import ban check against scope and reports violations via
// t.Errorf. A missing or unreachable scope is a fatal error. If detect returns
// diagnostics, they are emitted via [Report].
func (b ImportBan) Run(t *testing.T, s Scope) {
	t.Helper()
	diags, err := b.detect(s)
	if err != nil {
		t.Fatalf("scanner.ImportBan.Run(%s): %v", b.RuleID, err)
	}
	Report(t, b.RuleID, diags)
}

// sortDiagnostics sorts diags in-place by (Rel, Line, Message).
func sortDiagnostics(diags []Diagnostic) {
	sort.Slice(diags, func(i, j int) bool {
		return less(diags[i], diags[j])
	})
}

func less(a, b Diagnostic) bool {
	if a.Rel != b.Rel {
		return a.Rel < b.Rel
	}
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Message < b.Message
}
