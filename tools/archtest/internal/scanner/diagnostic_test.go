package scanner_test

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

func TestFormatReport_SortsAndDeduplicates(t *testing.T) {
	diags := []scanner.Diagnostic{
		{Rel: "b/b.go", Line: 10, Message: "msg B"},
		{Rel: "a/a.go", Line: 5, Message: "msg A"},
		{Rel: "a/a.go", Line: 5, Message: "msg A"}, // duplicate
		{Rel: "a/a.go", Line: 3, Message: "msg A2"},
	}
	msgs := scanner.FormatReportForTest("RULE-01", diags)
	// Expect 3 unique messages (duplicate removed), sorted by Rel then Line.
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d: %v", len(msgs), msgs)
	}
	// First: a/a.go:3
	if !strings.Contains(msgs[0], "a/a.go:3") {
		t.Errorf("msgs[0] should contain a/a.go:3, got: %s", msgs[0])
	}
	// Second: a/a.go:5
	if !strings.Contains(msgs[1], "a/a.go:5") {
		t.Errorf("msgs[1] should contain a/a.go:5, got: %s", msgs[1])
	}
	// Third: b/b.go:10
	if !strings.Contains(msgs[2], "b/b.go:10") {
		t.Errorf("msgs[2] should contain b/b.go:10, got: %s", msgs[2])
	}
}

func TestFormatReport_EmptySlice(t *testing.T) {
	msgs := scanner.FormatReportForTest("RULE-01", nil)
	if len(msgs) != 0 {
		t.Errorf("expected empty, got %v", msgs)
	}
}

func TestFormatReport_RuleIDPrefix(t *testing.T) {
	diags := []scanner.Diagnostic{
		{Rel: "x.go", Line: 1, Message: "bad import"},
	}
	msgs := scanner.FormatReportForTest("MY-RULE-42", diags)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if !strings.HasPrefix(msgs[0], "MY-RULE-42: ") {
		t.Errorf("expected ruleID prefix, got: %s", msgs[0])
	}
}

func TestFormatReport_Format(t *testing.T) {
	diags := []scanner.Diagnostic{
		{Rel: "pkg/foo.go", Line: 42, Message: "some violation"},
	}
	msgs := scanner.FormatReportForTest("RULE-01", diags)
	want := "RULE-01: pkg/foo.go:42: some violation"
	if msgs[0] != want {
		t.Errorf("got %q, want %q", msgs[0], want)
	}
}

func TestReport_NoPanic(t *testing.T) {
	// Report wraps formatReport and calls t.Errorf. With empty diags it should be no-op.
	scanner.Report(t, "RULE-01", nil)
}

func TestFormatReport_SameLine_DifferentMessage(t *testing.T) {
	// Tests the third comparison branch in less(): same Rel + same Line → sort by Message.
	diags := []scanner.Diagnostic{
		{Rel: "a.go", Line: 1, Message: "z violation"},
		{Rel: "a.go", Line: 1, Message: "a violation"},
	}
	msgs := scanner.FormatReportForTest("RULE-01", diags)
	if len(msgs) != 2 {
		t.Fatalf("expected 2, got %d", len(msgs))
	}
	if !strings.Contains(msgs[0], "a violation") {
		t.Errorf("first message should be 'a violation', got: %s", msgs[0])
	}
	if !strings.Contains(msgs[1], "z violation") {
		t.Errorf("second message should be 'z violation', got: %s", msgs[1])
	}
}
