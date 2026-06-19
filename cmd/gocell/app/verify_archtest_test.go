package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cmd/gocell/internal/archtestrunner"
	"github.com/ghbvf/gocell/framework/kernel/governance"
)

// TestEmitChangedSelectionSummary verifies the --changed selection summary is
// written (with the count + pre-filter caveat) when Changed is set, and is a
// no-op otherwise — so a 0-rule --changed run is diagnosable, not silent.
func TestEmitChangedSelectionSummary(t *testing.T) {
	t.Run("changed prints count + caveat", func(t *testing.T) {
		var buf bytes.Buffer
		emitChangedSelectionSummary(&buf, archtestrunner.Request{Changed: true}, 0)
		out := buf.String()
		assert.Contains(t, out, "0 rule(s) selected")
		assert.Contains(t, out, "pre-filter")
	})
	t.Run("changed nonzero count", func(t *testing.T) {
		var buf bytes.Buffer
		emitChangedSelectionSummary(&buf, archtestrunner.Request{Changed: true}, 7)
		assert.Contains(t, buf.String(), "7 rule(s) selected")
	})
	t.Run("not changed is silent", func(t *testing.T) {
		var buf bytes.Buffer
		emitChangedSelectionSummary(&buf, archtestrunner.Request{Changed: false}, 5)
		assert.Empty(t, buf.String())
	})
}

// ---------------------------------------------------------------------------
// Flag / dispatch tests (use captureDispatch from dispatch_test.go)
// ---------------------------------------------------------------------------

func TestVerifyArchtest_HelpFlag(t *testing.T) {
	ctx := context.Background()
	exit, stdout, stderr := captureDispatch(t, ctx, []string{"verify", "archtest", "-h"})
	assert.Equal(t, ExitOK, exit, "help request must exit 0")
	combined := stdout + stderr
	assert.Contains(t, combined, "archtest", "help output should mention 'archtest'")
}

func TestVerifyArchtestAlias_HelpFlag(t *testing.T) {
	ctx := context.Background()
	exit, stdout, stderr := captureDispatch(t, ctx, []string{"archtest", "-h"})
	assert.Equal(t, ExitOK, exit, "alias help request must exit 0")
	combined := stdout + stderr
	assert.Contains(t, combined, "archtest", "alias help output should mention 'archtest'")
}

func TestVerifyArchtest_BadFormat(t *testing.T) {
	ctx := context.Background()
	exit, _, stderr := captureDispatch(t, ctx, []string{"verify", "archtest", "--format=bogus"})
	// Unknown format error → ExitRuntime (printers.New returns error before any run)
	assert.Equal(t, ExitRuntime, exit, "bad format must exit 1")
	assert.Contains(t, stderr, "bogus", "stderr must mention the bad format value")
}

func TestVerifyArchtest_RuleAndChangedMutuallyExclusive(t *testing.T) {
	ctx := context.Background()
	exit, _, stderr := captureDispatch(t, ctx, []string{"verify", "archtest", "--rule=X", "--changed"})
	assert.Equal(t, ExitRuntime, exit, "mutual-exclusion combo must exit 1")
	assert.True(t,
		strings.Contains(stderr, "--rule") || strings.Contains(stderr, "--changed"),
		"stderr must mention one of the conflicting flags, got: %q", stderr)
}

func TestVerifyArchtest_BadShard(t *testing.T) {
	tests := []struct {
		name  string
		shard string
	}{
		{name: "non-numeric", shard: "bad"},
		{name: "missing slash", shard: "1"},
		{name: "N equals K", shard: "2/2"},
		{name: "N greater than K", shard: "5/2"},
		{name: "negative K", shard: "0/-1"},
		{name: "both non-numeric", shard: "a/b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			exit, _, _ := captureDispatch(t, ctx, []string{"verify", "archtest", "--shard=" + tc.shard})
			assert.Equal(t, ExitRuntime, exit, "bad shard %q must exit 1", tc.shard)
		})
	}
}

// ---------------------------------------------------------------------------
// --timeout validation tests
// ---------------------------------------------------------------------------

func TestVerifyArchtest_TimeoutValidation(t *testing.T) {
	tests := []struct {
		name      string
		timeout   string
		wantError bool
		errSubstr string
	}{
		{name: "valid 5m", timeout: "5m", wantError: false},
		{name: "valid 10m", timeout: "10m", wantError: false},
		{name: "valid 30s", timeout: "30s", wantError: false},
		{name: "invalid abc", timeout: "abc", wantError: true, errSubstr: "--timeout"},
		{name: "zero disables timeout", timeout: "0", wantError: true, errSubstr: "--timeout"},
		{name: "negative duration", timeout: "-1s", wantError: true, errSubstr: "--timeout"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, err := parseArchtestFlags([]string{"--timeout=" + tc.timeout})
			if tc.wantError {
				require.Error(t, err, "expected error for --timeout=%q", tc.timeout)
				assert.Contains(t, err.Error(), tc.errSubstr)
			} else if err != nil {
				// parseArchtestFlags may error later (e.g. findRoot), but the
				// timeout validation itself must not be the cause.
				assert.NotContains(t, err.Error(), "--timeout",
					"unexpected timeout-related error for valid --timeout=%q: %v", tc.timeout, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// parseShard unit tests
// ---------------------------------------------------------------------------

func TestParseShard_Valid(t *testing.T) {
	tests := []struct {
		input string
		want  archtestrunner.Shard
	}{
		{"0/3", archtestrunner.Shard{Index: 0, Total: 3}},
		{"1/3", archtestrunner.Shard{Index: 1, Total: 3}},
		{"2/3", archtestrunner.Shard{Index: 2, Total: 3}},
		{"0/1", archtestrunner.Shard{Index: 0, Total: 1}},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := parseShard(tc.input)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestParseShard_Empty(t *testing.T) {
	got, err := parseShard("")
	require.NoError(t, err)
	assert.Equal(t, archtestrunner.Shard{}, got, "empty string should return zero Shard (no sharding)")
}

func TestParseShard_Invalid(t *testing.T) {
	cases := []string{
		"bad",
		"1",
		"a/b",
		"1/0", // K must be >= 1
		"2/2", // N must be < K
		"3/2", // N > K
		"-1/3",
		"1/-3",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			_, err := parseShard(c)
			assert.Error(t, err, "expected error for input %q", c)
		})
	}
}

// ---------------------------------------------------------------------------
// mapReportToResults unit tests
// ---------------------------------------------------------------------------

func TestMapReportToResults_IssueTypeIsInvalid(t *testing.T) {
	report := archtestrunner.Report{
		Tests: []archtestrunner.TestResult{
			{Name: "TestFail", Status: "fail", Rules: []string{"LAYER-01"}},
		},
	}
	results := mapReportToResults(report)
	require.Len(t, results, 1)
	assert.Equal(t, governance.IssueInvalid, results[0].IssueType,
		"archtest failures are invariant violations (IssueInvalid), not forbidden constructs (IssueForbidden)")
}

func TestMapReportToResults_OnlyFailuresBecomeResults(t *testing.T) {
	report := archtestrunner.Report{
		Selected: []string{"TestA", "TestB", "TestC"},
		Tests: []archtestrunner.TestResult{
			{Name: "TestA", Status: "fail", File: "tools/archtest/layer_test.go", Rules: []string{"LAYER-01"}, Output: "expected X got Y"},
			{Name: "TestB", Status: "pass", File: "tools/archtest/layer_test.go", Rules: []string{"LAYER-01"}},
			{Name: "TestC", Status: "skip"},
		},
		Passed: false,
	}

	results := mapReportToResults(report)

	require.Len(t, results, 1, "only the failing test should produce a ValidationResult")
	r := results[0]
	assert.Equal(t, governance.SeverityError, r.Severity)
	assert.Equal(t, "tools/archtest/layer_test.go", r.File)
	assert.Equal(t, "LAYER-01", string(r.Code), "code should be the first rule ID")
	assert.NotEmpty(t, r.Fix, "Fix must be non-empty (GOVERNANCE-RULE-ERROR-FIX-FIELD-01)")
	assert.Contains(t, r.Message, "TestA", "message should contain test name")
}

func TestMapReportToResults_NoRulesFallsBackToTestName(t *testing.T) {
	report := archtestrunner.Report{
		Tests: []archtestrunner.TestResult{
			{Name: "TestFoo", Status: "fail", File: "", Rules: nil, Output: "oops"},
		},
	}

	results := mapReportToResults(report)

	require.Len(t, results, 1)
	r := results[0]
	assert.Equal(t, governance.RuleCode("TestFoo"), r.Code, "no rules: code should be test name")
	assert.NotEmpty(t, r.Fix)
}

func TestMapReportToResults_EmptyReport(t *testing.T) {
	report := archtestrunner.Report{Passed: true}
	results := mapReportToResults(report)
	assert.Empty(t, results)
}

func TestMapReportToResults_AllPass(t *testing.T) {
	report := archtestrunner.Report{
		Tests: []archtestrunner.TestResult{
			{Name: "TestA", Status: "pass"},
			{Name: "TestB", Status: "skip"},
		},
		Passed: true,
	}
	results := mapReportToResults(report)
	assert.Empty(t, results)
}

func TestMapReportToResults_OutputCapped(t *testing.T) {
	longOutput := strings.Repeat("x", 600)
	report := archtestrunner.Report{
		Tests: []archtestrunner.TestResult{
			{Name: "TestLong", Status: "fail", Output: longOutput},
		},
	}
	results := mapReportToResults(report)
	require.Len(t, results, 1)
	assert.LessOrEqual(t, len(results[0].Message), 512+50,
		"message should be capped to a reasonable length")
}

func TestMapReportToResults_FixIsConstant(t *testing.T) {
	report := archtestrunner.Report{
		Tests: []archtestrunner.TestResult{
			{Name: "TestX", Status: "fail"},
			{Name: "TestY", Status: "fail"},
		},
	}
	results := mapReportToResults(report)
	require.Len(t, results, 2)
	// Fix must be the same constant across all results (stable remediation)
	assert.Equal(t, results[0].Fix, results[1].Fix)
	assert.Contains(t, results[0].Fix, "gocell verify archtest",
		"Fix should include how to re-run")
}
