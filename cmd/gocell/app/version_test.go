package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"strings"
	"testing"
)

// TestRunVersion_TextDefault pins the default (text) output surface: all three
// reported fields are present. Labels are snake_case per the #1088 spec
// (cli_version / framework_version / compatible_framework_range).
func TestRunVersion_TextDefault(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runVersion(context.Background(), nil); err != nil {
			t.Fatalf("runVersion(nil) error: %v", err)
		}
	})
	for _, want := range []string{"cli_version:", "framework_version:", "compatible_framework_range:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("version text output missing %q in:\n%s", want, out)
		}
	}
}

func TestRunVersion_TextExplicit(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runVersion(context.Background(), []string{"--format=text"}); err != nil {
			t.Fatalf("runVersion(--format=text) error: %v", err)
		}
	})
	if !strings.Contains(out, "cli_version:") {
		t.Fatalf("version --format=text missing cli_version: in:\n%s", out)
	}
}

// TestRunVersion_JSONFormat pins the JSON wire shape: a single object with the
// three camelCase keys, every value non-empty. The key set is the machine
// contract external tooling reads, so it is asserted explicitly (Medium guard).
func TestRunVersion_JSONFormat(t *testing.T) {
	out := captureStdout(t, func() {
		if err := runVersion(context.Background(), []string{"--format=json"}); err != nil {
			t.Fatalf("runVersion(--format=json) error: %v", err)
		}
	})
	var got struct {
		CLIVersion               string `json:"cliVersion"`
		FrameworkVersion         string `json:"frameworkVersion"`
		CompatibleFrameworkRange string `json:"compatibleFrameworkRange"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("version --format=json not valid JSON: %v\noutput:\n%s", err, out)
	}
	if got.CLIVersion == "" || got.FrameworkVersion == "" || got.CompatibleFrameworkRange == "" {
		t.Fatalf("version json fields must all be non-empty: %+v", got)
	}
}

func TestRunVersion_UnknownFormat(t *testing.T) {
	var err error
	_ = captureStderr(t, func() {
		err = runVersion(context.Background(), []string{"--format=sarif"})
	})
	if err == nil {
		t.Fatal("runVersion(--format=sarif) should return an error")
	}
	if !strings.Contains(err.Error(), "unknown format") {
		t.Fatalf("want unknown-format error, got: %v", err)
	}
}

func TestRunVersion_UnknownFlag(t *testing.T) {
	var err error
	_ = captureStderr(t, func() {
		err = runVersion(context.Background(), []string{"--nope"})
	})
	if err == nil {
		t.Fatal("runVersion(--nope) should return a flag parse error")
	}
}

// TestRunVersion_HelpFlag confirms -h flows through flag.ContinueOnError as
// flag.ErrHelp (which Dispatch maps to ExitOK), matching the other subcommands.
func TestRunVersion_HelpFlag(t *testing.T) {
	var err error
	_ = captureStderr(t, func() {
		err = runVersion(context.Background(), []string{"-h"})
	})
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("runVersion(-h) should return flag.ErrHelp, got: %v", err)
	}
}

func TestDispatch_Version_ExitOK(t *testing.T) {
	out := captureStdout(t, func() {
		if code := Dispatch(context.Background(), []string{"version"}); code != ExitOK {
			t.Fatalf("Dispatch(version) exit code = %d, want %d", code, ExitOK)
		}
	})
	if !strings.Contains(out, "cli_version:") {
		t.Fatalf("Dispatch(version) stdout missing cli_version: in:\n%s", out)
	}
}

// TestCompatibleFrameworkRange pins the Hard single-source derivation: the
// compatible framework range is a pure function of the injected version, NOT a
// separately-maintained constant. v0.x lines are minor-bounded (SemVer §4 lets
// minors break pre-1.0); an unparseable / dev version passes through unchanged.
func TestCompatibleFrameworkRange(t *testing.T) {
	cases := []struct{ in, want string }{
		{"v0.1.0", ">=v0.1.0 <v0.2.0"},
		{"v1.2.3", ">=v1.2.0 <v1.3.0"},
		{"v0.1.0-develop.20260613T060848Z", ">=v0.1.0 <v0.2.0"},
		{"v0.9.0", ">=v0.9.0 <v0.10.0"},
		{"dev", "dev"},
	}
	for _, tc := range cases {
		if got := compatibleFrameworkRange(tc.in); got != tc.want {
			t.Errorf("compatibleFrameworkRange(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
