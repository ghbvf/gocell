package app

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen/cellgen"
)

// stubSpec is a minimal codegenSpec for testing parseCodegenFlags in isolation.
var stubSpec = codegenSpec[cellgen.Result]{
	Kind:          "widget",
	GenerateUsage: "gocell generate widget <widgetID> | --all [--dry-run | --verify]",
	AllFlagDesc:   "generate for every widget",
	PluralNoun:    "widgets",
	Generate: func(_ string, _ *metadata.ProjectMeta, _, _ bool, _ string) (cellgen.Result, error) {
		return cellgen.Result{}, nil
	},
}

func TestParseCodegenFlags_NoArgs(t *testing.T) {
	t.Parallel()
	_, _, _, err := parseCodegenFlags(stubSpec, nil)
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestParseCodegenFlags_DryRunVerifyMutex(t *testing.T) {
	t.Parallel()
	_, _, _, err := parseCodegenFlags(stubSpec, []string{"--dry-run", "--verify", "w1"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutex error, got %v", err)
	}
}

func TestParseCodegenFlags_AllAndPositionalMutex(t *testing.T) {
	t.Parallel()
	_, _, _, err := parseCodegenFlags(stubSpec, []string{"--all", "w1"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutex error, got %v", err)
	}
}

func TestParseCodegenFlags_DryRunOnlyFlag(t *testing.T) {
	t.Parallel()
	_, _, _, err := parseCodegenFlags(stubSpec, []string{"--dry-run"})
	if err == nil || !strings.Contains(err.Error(), "--all") {
		t.Fatalf("expected diagnostic error, got %v", err)
	}
}

func TestParseCodegenFlags_VerifyOnlyFlag(t *testing.T) {
	t.Parallel()
	_, _, _, err := parseCodegenFlags(stubSpec, []string{"--verify"})
	if err == nil || !strings.Contains(err.Error(), "--all") {
		t.Fatalf("expected diagnostic error, got %v", err)
	}
}

func TestParseCodegenFlags_AllFlag(t *testing.T) {
	t.Parallel()
	dryRun, verify, only, err := parseCodegenFlags(stubSpec, []string{"--all"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dryRun || verify || only != "" {
		t.Fatalf("unexpected values: dryRun=%v verify=%v only=%q", dryRun, verify, only)
	}
}

func TestParseCodegenFlags_AllWithDryRun(t *testing.T) {
	t.Parallel()
	dryRun, verify, only, err := parseCodegenFlags(stubSpec, []string{"--all", "--dry-run"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dryRun || verify || only != "" {
		t.Fatalf("unexpected values: dryRun=%v verify=%v only=%q", dryRun, verify, only)
	}
}

func TestParseCodegenFlags_AllWithVerify(t *testing.T) {
	t.Parallel()
	dryRun, verify, only, err := parseCodegenFlags(stubSpec, []string{"--all", "--verify"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dryRun || !verify || only != "" {
		t.Fatalf("unexpected values: dryRun=%v verify=%v only=%q", dryRun, verify, only)
	}
}

func TestParseCodegenFlags_PositionalID(t *testing.T) {
	t.Parallel()
	dryRun, verify, only, err := parseCodegenFlags(stubSpec, []string{"my-widget"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dryRun || verify || only != "my-widget" {
		t.Fatalf("unexpected values: dryRun=%v verify=%v only=%q", dryRun, verify, only)
	}
}

func TestParseCodegenFlags_UnknownFlag(t *testing.T) {
	t.Parallel()
	_, _, _, err := parseCodegenFlags(stubSpec, []string{"--no-such-flag"})
	if err == nil {
		t.Fatal("expected flag-parse error for unknown flag")
	}
}
