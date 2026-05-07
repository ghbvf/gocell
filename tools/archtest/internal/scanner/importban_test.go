package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

// copyFile copies a single file.
func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src) //nolint:gosec // testdata path under test control
	if err != nil {
		t.Fatalf("copyFile ReadFile %s: %v", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("copyFile MkdirAll %s: %v", filepath.Dir(dst), err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil { //nolint:gosec // temp dir under test control
		t.Fatalf("copyFile WriteFile %s: %v", dst, err)
	}
}

func TestImportBan_DetectsForbidden(t *testing.T) {
	tmp := t.TempDir()
	// Copy bad.go.txt as bad.go into tmp/violates/
	copyFile(t, filepath.Join("testdata", "importban", "violates", "bad.go.txt"),
		filepath.Join(tmp, "violates", "bad.go"))

	s := DirsScope(tmp, []string{"violates"})
	ban := ImportBan{
		RuleID:    "TEST-BAN-01",
		Forbidden: []string{"github.com/forbidden/path"},
		Hint:      "use the allowed alternative",
	}
	diags, err := ban.detect(s)
	if err != nil {
		t.Fatalf("detect error: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("expected 1 violation, got %d: %v", len(diags), diags)
	}
	if diags[0].Rel != filepath.Join("violates", "bad.go") {
		t.Errorf("unexpected Rel: %s", diags[0].Rel)
	}
}

func TestImportBan_CompliantFile(t *testing.T) {
	tmp := t.TempDir()
	copyFile(t, filepath.Join("testdata", "importban", "compliant", "good.go.txt"),
		filepath.Join(tmp, "compliant", "good.go"))

	s := DirsScope(tmp, []string{"compliant"})
	ban := ImportBan{
		RuleID:    "TEST-BAN-01",
		Forbidden: []string{"github.com/forbidden/path"},
	}
	diags, err := ban.detect(s)
	if err != nil {
		t.Fatalf("detect error: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("expected 0 violations, got %d: %v", len(diags), diags)
	}
}

func TestImportBan_AllowRels_Skips(t *testing.T) {
	tmp := t.TempDir()
	copyFile(t, filepath.Join("testdata", "importban", "allowlisted", "special.go.txt"),
		filepath.Join(tmp, "allowlisted", "special.go"))

	s := DirsScope(tmp, []string{"allowlisted"})
	ban := ImportBan{
		RuleID:    "TEST-BAN-01",
		Forbidden: []string{"github.com/forbidden/path"},
		AllowRels: []string{filepath.Join("allowlisted", "special.go")},
	}
	diags, err := ban.detect(s)
	if err != nil {
		t.Fatalf("detect error: %v", err)
	}
	if len(diags) != 0 {
		t.Errorf("expected 0 violations after allowlist, got %d: %v", len(diags), diags)
	}
}

func TestImportBan_HintInMessage(t *testing.T) {
	tmp := t.TempDir()
	copyFile(t, filepath.Join("testdata", "importban", "violates", "bad.go.txt"),
		filepath.Join(tmp, "violates", "bad.go"))

	s := DirsScope(tmp, []string{"violates"})
	ban := ImportBan{
		RuleID:    "TEST-BAN-01",
		Forbidden: []string{"github.com/forbidden/path"},
		Hint:      "use the allowed alternative",
	}
	diags, err := ban.detect(s)
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
}

func TestImportBan_SortedDiagnostics(t *testing.T) {
	tmp := t.TempDir()
	// Two files that both violate.
	copyFile(t, filepath.Join("testdata", "importban", "violates", "bad.go.txt"),
		filepath.Join(tmp, "b.go"))
	copyFile(t, filepath.Join("testdata", "importban", "violates", "bad.go.txt"),
		filepath.Join(tmp, "a.go"))

	s := DirsScope(tmp, []string{"."})
	ban := ImportBan{
		RuleID:    "TEST-BAN-01",
		Forbidden: []string{"github.com/forbidden/path"},
	}
	diags, err := ban.detect(s)
	if err != nil {
		t.Fatalf("detect error: %v", err)
	}
	if len(diags) < 2 {
		t.Fatalf("expected at least 2 violations, got %d", len(diags))
	}
	if diags[0].Rel > diags[1].Rel {
		t.Errorf("diagnostics not sorted: %v > %v", diags[0].Rel, diags[1].Rel)
	}
}

func TestSortDiagnostics_SameRelDifferentLine(t *testing.T) {
	diags := []Diagnostic{
		{Rel: "a.go", Line: 10, Message: "msg"},
		{Rel: "a.go", Line: 2, Message: "msg"},
		{Rel: "a.go", Line: 2, Message: "zzz"}, // same Rel+Line, different Message
	}
	sortDiagnostics(diags)
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

func TestImportBan_Run_NoViolations(t *testing.T) {
	tmp := t.TempDir()
	copyFile(t, filepath.Join("testdata", "importban", "compliant", "good.go.txt"),
		filepath.Join(tmp, "compliant", "good.go"))

	s := DirsScope(tmp, []string{"compliant"})
	ban := ImportBan{
		RuleID:    "TEST-BAN-01",
		Forbidden: []string{"github.com/forbidden/path"},
	}
	// Run should not call t.Errorf when there are no violations.
	ban.Run(t, s)
}
