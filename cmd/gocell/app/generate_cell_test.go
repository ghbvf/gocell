package app

import (
	"strings"
	"testing"
)

func TestGenerateCell_NoArgs(t *testing.T) {
	t.Parallel()
	err := generateCell(nil)
	if err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestGenerateCell_DryRunVerifyMutex(t *testing.T) {
	t.Parallel()
	err := generateCell([]string{"--dry-run", "--verify", "demo"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutex error, got %v", err)
	}
}

func TestGenerateCell_AllAndPositionalMutex(t *testing.T) {
	t.Parallel()
	err := generateCell([]string{"--all", "demo"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutex error, got %v", err)
	}
}

func TestGenerateCell_UnknownFlag(t *testing.T) {
	t.Parallel()
	// flag.ContinueOnError makes Parse return an error for unknown flags
	// (it also writes to its Output, which we don't capture here).
	err := generateCell([]string{"--no-such-flag"})
	if err == nil {
		t.Fatal("expected error from flag parser")
	}
}
