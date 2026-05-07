package scanner

import (
	"fmt"
	"sort"
	"testing"
)

// Diagnostic represents a single rule violation found during a scan.
type Diagnostic struct {
	// Rel is the file path relative to the module root.
	Rel string
	// Line is the 1-based line number of the violation.
	Line int
	// Message describes the violation.
	Message string
}

// diagKey is used for deduplication.
type diagKey struct {
	Rel     string
	Line    int
	Message string
}

// formatReport deduplicates and sorts diags, then returns one formatted message
// string per unique diagnostic in the form "<ruleID>: <rel>:<line>: <message>".
func formatReport(ruleID string, diags []Diagnostic) []string {
	if len(diags) == 0 {
		return nil
	}
	// Deduplicate via a set.
	seen := make(map[diagKey]struct{}, len(diags))
	unique := make([]Diagnostic, 0, len(diags))
	for _, d := range diags {
		k := diagKey(d)
		if _, ok := seen[k]; !ok {
			seen[k] = struct{}{}
			unique = append(unique, d)
		}
	}
	// Sort by (Rel, Line, Message) for stable output.
	sort.Slice(unique, func(i, j int) bool {
		if unique[i].Rel != unique[j].Rel {
			return unique[i].Rel < unique[j].Rel
		}
		if unique[i].Line != unique[j].Line {
			return unique[i].Line < unique[j].Line
		}
		return unique[i].Message < unique[j].Message
	})
	msgs := make([]string, len(unique))
	for i, d := range unique {
		msgs[i] = fmt.Sprintf("%s: %s:%d: %s", ruleID, d.Rel, d.Line, d.Message)
	}
	return msgs
}

// Report formats and emits each diagnostic as a t.Errorf call, sorted and
// deduplicated. An empty diags slice is a no-op.
func Report(t *testing.T, ruleID string, diags []Diagnostic) {
	t.Helper()
	for _, msg := range formatReport(ruleID, diags) {
		t.Errorf("%s", msg)
	}
}
