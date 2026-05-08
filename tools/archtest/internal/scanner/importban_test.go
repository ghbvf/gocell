package scanner_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ghbvf/gocell/pkg/testutil/fileutil"
	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// copyTestFile copies a single file for use in importban tests.
func copyTestFile(t *testing.T, src, dst string) {
	t.Helper()
	data := fileutil.MustReadFile(t, src)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("copyTestFile MkdirAll %s: %v", filepath.Dir(dst), err)
	}
	fileutil.MustWriteFile(t, dst, data)
}

func TestImportBan_DetectsForbidden(t *testing.T) {
	tmp := t.TempDir()
	// Copy bad.go.txt as bad.go into tmp/violates/
	copyTestFile(t, filepath.Join("testdata", "importban", "violates", "bad.go.txt"),
		filepath.Join(tmp, "violates", "bad.go"))

	s := scanner.DirsScope(tmp, []string{"violates"})
	ban := scanner.ImportBan{
		RuleID:    "TEST-BAN-01",
		Forbidden: []string{"github.com/forbidden/path"},
		Hint:      "use the allowed alternative",
	}
	diags, err := ban.DetectForTest(s)
	if err != nil {
		t.Fatalf("detect error: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("expected 1 violation, got %d: %v", len(diags), diags)
	}
	if diags[0].Rel != "violates/bad.go" {
		t.Errorf("unexpected Rel: %s", diags[0].Rel)
	}
}

func TestImportBan_CompliantFile(t *testing.T) {
	tmp := t.TempDir()
	copyTestFile(t, filepath.Join("testdata", "importban", "compliant", "good.go.txt"),
		filepath.Join(tmp, "compliant", "good.go"))

	s := scanner.DirsScope(tmp, []string{"compliant"})
	ban := scanner.ImportBan{
		RuleID:    "TEST-BAN-01",
		Forbidden: []string{"github.com/forbidden/path"},
	}
	diags, err := ban.DetectForTest(s)
	if err != nil {
		t.Fatalf("detect error: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("expected 0 violations, got %d: %v", len(diags), diags)
	}
}

func TestImportBan_AllowRels_Skips(t *testing.T) {
	tmp := t.TempDir()
	copyTestFile(t, filepath.Join("testdata", "importban", "allowlisted", "special.go.txt"),
		filepath.Join(tmp, "allowlisted", "special.go"))

	s := scanner.DirsScope(tmp, []string{"allowlisted"})
	ban := scanner.ImportBan{
		RuleID:    "TEST-BAN-01",
		Forbidden: []string{"github.com/forbidden/path"},
		AllowRels: []string{"allowlisted/special.go"},
	}
	diags, err := ban.DetectForTest(s)
	if err != nil {
		t.Fatalf("detect error: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("expected 0 violations after allowlist, got %d: %v", len(diags), diags)
	}
}

func TestImportBan_HintInMessage(t *testing.T) {
	tmp := t.TempDir()
	copyTestFile(t, filepath.Join("testdata", "importban", "violates", "bad.go.txt"),
		filepath.Join(tmp, "violates", "bad.go"))

	s := scanner.DirsScope(tmp, []string{"violates"})
	ban := scanner.ImportBan{
		RuleID:    "TEST-BAN-01",
		Forbidden: []string{"github.com/forbidden/path"},
		Hint:      "use the allowed alternative",
	}
	diags, err := ban.DetectForTest(s)
	if err != nil {
		t.Fatalf("detect error: %v", err)
	}
	if len(diags) == 0 {
		t.Fatal("expected violation")
	}
	msg := diags[0].Message
	if msg == "" {
		t.Error("message should be non-empty")
	}
	if !containsString(msg, ban.Hint) {
		t.Errorf("message %q should contain hint %q", msg, ban.Hint)
	}
}

// containsString is a local helper to avoid importing strings package just for Contains.
func containsString(s, sub string) bool {
	return len(s) >= len(sub) && (sub == "" || func() bool {
		for i := 0; i <= len(s)-len(sub); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}())
}

func TestImportBan_SortedDiagnostics(t *testing.T) {
	tmp := t.TempDir()
	// Three files that all violate — a.go, b.go, c.go — to cover 3+ element stability.
	for _, name := range []string{"a.go", "b.go", "c.go"} {
		copyTestFile(t, filepath.Join("testdata", "importban", "violates", "bad.go.txt"),
			filepath.Join(tmp, name))
	}

	s := scanner.DirsScope(tmp, []string{"."})
	ban := scanner.ImportBan{
		RuleID:    "TEST-BAN-01",
		Forbidden: []string{"github.com/forbidden/path"},
	}
	diags, err := ban.DetectForTest(s)
	if err != nil {
		t.Fatalf("detect error: %v", err)
	}
	if len(diags) < 3 {
		t.Fatalf("expected at least 3 violations, got %d", len(diags))
	}
	// Verify sorted order for all adjacent pairs.
	for i := 1; i < len(diags); i++ {
		prev, cur := diags[i-1], diags[i]
		if prev.Rel > cur.Rel || (prev.Rel == cur.Rel && prev.Line > cur.Line) {
			t.Errorf("diagnostics not sorted at index %d: %v > %v", i, prev, cur)
		}
	}
}

func TestSortDiagnostics_SameRelDifferentLine(t *testing.T) {
	diags := []scanner.Diagnostic{
		{Rel: "a.go", Line: 10, Message: "msg"},
		{Rel: "a.go", Line: 2, Message: "msg"},
		{Rel: "a.go", Line: 2, Message: "zzz"}, // same Rel+Line, different Message
	}
	scanner.SortDiagnosticsForTest(diags)
	if diags[0].Line != 2 {
		t.Errorf("first should have Line=2, got %d", diags[0].Line)
	}
	if diags[1].Line != 2 {
		t.Errorf("second should have Line=2, got %d", diags[1].Line)
	}
	if diags[2].Line != 10 {
		t.Errorf("third should have Line=10, got %d", diags[2].Line)
	}
	// Within same Rel+Line, msg < zzz
	if diags[0].Message != "msg" {
		t.Errorf("first within same line should be 'msg', got %q", diags[0].Message)
	}
}

func TestImportBan_AllowRels_NormalizationIsConsistent(t *testing.T) {
	// buildAllowSet must apply filepath.Clean + ToSlash so AllowRels keys
	// match fc.Rel (slash-separated, cleaned) regardless of caller-side
	// formatting variations. Equivalent inputs must dedupe to one key.
	set := scanner.BuildAllowSetForTest([]string{
		"tools/archtest/foo.go",
		"./tools/archtest/foo.go",
		"tools//archtest//foo.go",
		"tools/archtest/./foo.go",
	})
	if _, ok := set["tools/archtest/foo.go"]; !ok {
		t.Errorf("Clean+ToSlash should normalize all variants to tools/archtest/foo.go; got %v", set)
	}
	if len(set) != 1 {
		t.Errorf("equivalent paths must dedupe; got %d entries: %v", len(set), set)
	}
}

func TestImportBan_Run_NoViolations(t *testing.T) {
	tmp := t.TempDir()
	copyTestFile(t, filepath.Join("testdata", "importban", "compliant", "good.go.txt"),
		filepath.Join(tmp, "compliant", "good.go"))

	s := scanner.DirsScope(tmp, []string{"compliant"})
	ban := scanner.ImportBan{
		RuleID:    "TEST-BAN-01",
		Forbidden: []string{"github.com/forbidden/path"},
	}
	// Run should not call t.Errorf when there are no violations.
	ban.Run(t, s)
}
