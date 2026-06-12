package app

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ghbvf/gocell/cmd/gocell/app/printers"
	"github.com/ghbvf/gocell/cmd/gocell/internal/archtestrunner"
	"github.com/ghbvf/gocell/kernel/governance"
)

// archtestFixConst is the stable remediation guidance for failing archtest
// findings. Required non-empty by GOVERNANCE-RULE-ERROR-FIX-FIELD-01.
const archtestFixConst = "inspect the failing archtest output; re-run with: gocell verify archtest --rule=<ID>"

// maxArchtestOutputLen caps the Output field length when mapping to a
// ValidationResult.Message (avoids multi-MB messages in CI artifacts).
const maxArchtestOutputLen = 512

// verifyArchtest implements: gocell verify archtest [flags]
// It is also the handler for the top-level alias "gocell archtest".
func verifyArchtest(ctx context.Context, args []string) error {
	req, listTests, format, err := parseArchtestFlags(args)
	if err != nil {
		return err
	}

	if listTests {
		return runArchtestListTests(ctx, req)
	}
	return runArchtestReport(ctx, req, format)
}

// parseArchtestFlags parses all flags for verifyArchtest, validates combos,
// and returns the runner Request, list-tests mode flag, format string, and
// any error.
func parseArchtestFlags(args []string) (req archtestrunner.Request, listTests bool, format string, err error) {
	fs := flag.NewFlagSet("verify archtest", flag.ContinueOnError)

	root := fs.String("root", "", "workspace root directory (default: auto-detect)")
	rule := fs.String("rule", "", "optional INVARIANT rule ID, e.g. LAYER-05")
	changed := fs.Bool("changed", false,
		"run only tests whose tools/archtest/*_test.go file changed vs origin/develop"+
			" (mechanical; NOT source→affected-rule mapping, see #1877)")
	shardStr := fs.String("shard", "", "shard selection N/K (e.g. 0/3)")
	formatFlag := fs.String("format", "text", "output format: "+strings.Join(printers.SupportedFormats(), " | "))
	testJSONOut := fs.String("test-json-out", "", "optional file: write raw go test -json events")
	listTestsFlag := fs.Bool("list-tests", false, "print selected test names to stdout and exit")
	timeout := fs.String("timeout", "5m", "go test -timeout value (e.g. 5m, 10m)")

	if parseErr := fs.Parse(args); parseErr != nil {
		return req, false, "", parseErr
	}
	if *rule != "" && *changed {
		return req, false, "", fmt.Errorf("--rule and --changed are mutually exclusive: use one or the other")
	}

	shard, shardErr := parseShard(*shardStr)
	if shardErr != nil {
		return req, false, "", fmt.Errorf("--shard: %w", shardErr)
	}

	dur, durErr := time.ParseDuration(*timeout)
	if durErr != nil {
		return req, false, "", fmt.Errorf("--timeout: invalid duration %q: %w", *timeout, durErr)
	}
	if dur <= 0 {
		return req, false, "", fmt.Errorf(
			"--timeout: must be a positive duration (e.g. 5m); got %q"+
				" (0 disables go test timeout, hanging CI shards until GHA's 10m backstop)",
			*timeout)
	}

	workspaceRoot := *root
	if workspaceRoot == "" {
		workspaceRoot, err = findRoot()
		if err != nil {
			return req, false, "", fmt.Errorf("cannot find workspace root: %w", err)
		}
	}

	req = archtestrunner.Request{
		WorkspaceRoot: workspaceRoot,
		// --scope flag intentionally not exposed: framework==workspace execution today
		// (all archtest is one package requiring GOWORK). Internal Scope/WorkspaceRoot
		// seam reserved for external-repo archtest (epic gh #1878).
		Scope: archtestrunner.ScopeWorkspace,
		Rule:  *rule,
		// Mechanical --changed: changed archtest test files only; source→affected-rule
		// mapping deferred (gh #1877).
		Changed:     *changed,
		Shard:       shard,
		TestJSONOut: *testJSONOut,
		Timeout:     *timeout,
	}
	return req, *listTestsFlag, *formatFlag, nil
}

// runArchtestListTests handles --list-tests mode: prints one name per line to
// stdout and exits. The output format is exactly as ListTests returns it
// (test function names, nothing else on stdout).
func runArchtestListTests(ctx context.Context, req archtestrunner.Request) error {
	names, err := archtestrunner.ListTests(ctx, req)
	if err != nil {
		return err
	}
	for _, n := range names {
		if _, werr := fmt.Fprintln(os.Stdout, n); werr != nil {
			return fmt.Errorf("write list-tests output: %w", werr)
		}
	}
	return ctxInterrupted(ctx, "archtest")
}

// runArchtestReport handles the normal execution+report mode.
func runArchtestReport(ctx context.Context, req archtestrunner.Request, format string) error {
	// Format validation before execution (fail fast on misconfigured CI call).
	printer, err := printers.New(format, os.Stdout, toolVersion())
	if err != nil {
		return err
	}

	report, err := archtestrunner.Run(ctx, req)
	if err != nil {
		return err
	}
	if ie := ctxInterrupted(ctx, "archtest"); ie != nil {
		return ie
	}

	results := mapReportToResults(report)
	if err := printer.Print(results); err != nil {
		return fmt.Errorf("emit results: %w", err)
	}

	if !report.Passed {
		return fmt.Errorf("archtest: %d failing test(s)", len(results))
	}
	return nil
}

// runArchtestAlias is the top-level alias handler for `gocell archtest`.
// It delegates directly to verifyArchtest so the two entry points are
// identical in behavior.
func runArchtestAlias(ctx context.Context, args []string) error {
	return verifyArchtest(ctx, args)
}

// parseShard parses a "N/K" shard string into an archtestrunner.Shard.
// Empty string returns Shard{} (no sharding). Returns an error for any
// invalid format or out-of-range values.
func parseShard(s string) (archtestrunner.Shard, error) {
	if s == "" {
		return archtestrunner.Shard{}, nil
	}
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return archtestrunner.Shard{}, fmt.Errorf("invalid shard format %q: expected N/K (e.g. 0/3)", s)
	}
	n, nErr := strconv.Atoi(parts[0])
	if nErr != nil {
		return archtestrunner.Shard{}, fmt.Errorf("invalid shard format %q: N must be an integer: %w", s, nErr)
	}
	k, kErr := strconv.Atoi(parts[1])
	if kErr != nil {
		return archtestrunner.Shard{}, fmt.Errorf("invalid shard format %q: K must be an integer: %w", s, kErr)
	}
	if k < 1 {
		return archtestrunner.Shard{}, fmt.Errorf("invalid shard %q: K must be >= 1, got %d", s, k)
	}
	if n < 0 || n >= k {
		return archtestrunner.Shard{}, fmt.Errorf("invalid shard %q: N must be in [0, %d), got %d", s, k, n)
	}
	return archtestrunner.Shard{Index: n, Total: k}, nil
}

// mapReportToResults converts a runner Report into []governance.ValidationResult.
// Only failed tests produce results; pass/skip tests are ignored.
func mapReportToResults(report archtestrunner.Report) []governance.ValidationResult {
	var results []governance.ValidationResult
	for _, tr := range report.Tests {
		if tr.Status != "fail" {
			continue
		}
		results = append(results, governance.ValidationResult{
			Code:      ruleCodeForTest(tr),
			Severity:  governance.SeverityError,
			IssueType: governance.IssueInvalid,
			File:      tr.File,
			Field:     tr.Name,
			Message:   trimOutput(tr.Output, tr.Name),
			Fix:       archtestFixConst,
		})
	}
	return results
}

// ruleCodeForTest returns a governance.RuleCode for a TestResult.
// Uses the first rule ID from tr.Rules when available; falls back to
// the test function name. RuleCode is an open string type so any value
// is valid outside the kernel/governance package.
func ruleCodeForTest(tr archtestrunner.TestResult) governance.RuleCode {
	if len(tr.Rules) > 0 && tr.Rules[0] != "" {
		return governance.RuleCode(tr.Rules[0])
	}
	return governance.RuleCode(tr.Name)
}

// trimOutput returns a trimmed, length-capped message derived from the test
// output. When output is empty the test name alone is used.
func trimOutput(output, testName string) string {
	out := strings.TrimSpace(output)
	if out == "" {
		return "archtest failure: " + testName
	}
	if len(out) > maxArchtestOutputLen {
		out = out[:maxArchtestOutputLen] + "…"
	}
	return "archtest failure: " + testName + ": " + out
}
