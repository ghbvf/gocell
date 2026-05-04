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

// --- parseCodegenFlags table-driven tests ------------------------------------
//
// K#05 W2 DX defaults: --all=true (run all when no args), positional id wins.

func TestParseCodegenFlags(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		args      []string
		wantDry   bool
		wantVer   bool
		wantOnly  string
		wantErrIn string // substring expected in error; empty = no error
	}{
		// Default: no args → all=true default, target ""
		{name: "NoArgs_DefaultsToAll", args: nil, wantOnly: ""},
		// Explicit --all still works
		{name: "ExplicitAll", args: []string{"--all"}, wantOnly: ""},
		// Positional id overrides default --all
		{name: "PositionalID", args: []string{"my-widget"}, wantOnly: "my-widget"},
		// Positional id alongside explicit --all: positional wins (no error)
		{name: "AllAndPositional_PositionalWins", args: []string{"--all", "w1"}, wantOnly: "w1"},
		// --all=false without positional id: error
		{name: "AllFalseNoID_Error", args: []string{"--all=false"}, wantErrIn: "usage"},
		// --dry-run alone (all=true default): succeeds
		{name: "DryRunAlone", args: []string{"--dry-run"}, wantDry: true, wantOnly: ""},
		// --verify alone (all=true default): succeeds
		{name: "VerifyAlone", args: []string{"--verify"}, wantVer: true, wantOnly: ""},
		// --dry-run + --all: succeeds
		{name: "AllWithDryRun", args: []string{"--all", "--dry-run"}, wantDry: true, wantOnly: ""},
		// --all + --verify: succeeds
		{name: "AllWithVerify", args: []string{"--all", "--verify"}, wantVer: true, wantOnly: ""},
		// --dry-run + --verify: mutually exclusive
		{name: "DryRunVerifyMutex", args: []string{"--dry-run", "--verify", "w1"}, wantErrIn: "mutually exclusive"},
		// --all=false + --dry-run without positional: diagnostic error
		{name: "AllFalseWithDryRunNoID", args: []string{"--all=false", "--dry-run"}, wantErrIn: "--all"},
		// --all=false + --verify without positional: diagnostic error
		{name: "AllFalseWithVerifyNoID", args: []string{"--all=false", "--verify"}, wantErrIn: "--all"},
		// Unknown flag: parse error
		{name: "UnknownFlag", args: []string{"--no-such-flag"}, wantErrIn: "flag provided but not defined"},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dryRun, verify, only, err := parseCodegenFlags(stubSpec, tc.args)
			if tc.wantErrIn != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErrIn) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErrIn, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if dryRun != tc.wantDry || verify != tc.wantVer || only != tc.wantOnly {
				t.Fatalf("got dryRun=%v verify=%v only=%q; want dryRun=%v verify=%v only=%q",
					dryRun, verify, only, tc.wantDry, tc.wantVer, tc.wantOnly)
			}
		})
	}
}
