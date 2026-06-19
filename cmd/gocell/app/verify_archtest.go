package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ghbvf/gocell/cmd/gocell/app/printers"
	"github.com/ghbvf/gocell/cmd/gocell/internal/archtestrunner"
	"github.com/ghbvf/gocell/framework/kernel/governance"
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
		"run only rules a changed file (vs origin/develop) could affect: rules whose own"+
			" *_test.go changed, plus rules whose static scan domain contains a changed source"+
			" file; rules with an undeterminable scan domain always run (fast pre-filter, not a"+
			" merge gate)")
	shardStr := fs.String("shard", "", "shard selection N/K (e.g. 0/3)")
	scopeFlag := fs.String("scope", string(archtestrunner.ScopeWorkspace),
		"execution scope: workspace (full suite, default) | framework (portable StandardCellRules subset)")
	formatFlag := fs.String("format", "text", "output format: "+strings.Join(printers.SupportedFormats(), " | "))
	testJSONOut := fs.String("test-json-out", "", "optional file: write raw go test -json events")
	listTestsFlag := fs.Bool("list-tests", false, "print selected test names to stdout and exit")
	timeout := fs.String("timeout", "5m", "go test -timeout value (e.g. 5m, 10m)")

	if parseErr := fs.Parse(args); parseErr != nil {
		return req, false, "", parseErr
	}
	if *rule != "" && *changed {
		return req, false, "", fmt.Errorf(
			"--rule and --changed are mutually exclusive: --changed already narrows by scan domain;" +
				" to run one rule unfiltered, drop --changed and use --rule=<ID>",
		)
	}

	shard, shardErr := parseShard(*shardStr)
	if shardErr != nil {
		return req, false, "", fmt.Errorf("--shard: %w", shardErr)
	}

	// Validate scope eagerly (fail fast on a misconfigured CI call, like --shard /
	// --timeout); ResolveScope is the same validator Run / ListTests apply.
	scope, scopeErr := archtestrunner.ResolveScope(archtestrunner.Scope(*scopeFlag))
	if scopeErr != nil {
		return req, false, "", fmt.Errorf("--scope: %w", scopeErr)
	}

	dur, durErr := time.ParseDuration(*timeout)
	if durErr != nil {
		return req, false, "", fmt.Errorf("--timeout: invalid duration %q: %w", *timeout, durErr)
	}
	if dur <= 0 {
		return req, false, "", fmt.Errorf(
			"--timeout: must be a positive duration (e.g. 5m); got %q"+
				" (0 disables go test timeout, hanging CI shards until GHA's 10m backstop)",
			*timeout,
		)
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
		// --scope=framework runs only the portable StandardCellRules subset; the
		// default (workspace) runs the full suite. See #2331 / #1878.
		Scope: scope,
		Rule:  *rule,
		// Source-aware --changed: a changed file selects the rules whose own *_test.go
		// changed plus the rules whose static scan domain contains the change (gh #1877).
		Changed:     *changed,
		Shard:       shard,
		TestJSONOut: *testJSONOut,
		Timeout:     *timeout,
	}
	return req, *listTestsFlag, *formatFlag, nil
}

// emitChangedSelectionSummary writes, under --changed, a one-line summary of how
// many rules were selected — to w (os.Stderr in production) so stdout stays
// clean (test names for --list-tests, the printer payload for report mode). It
// makes a 0-rule result diagnosable instead of an empty/clean output that reads
// like "all passed". No-op when --changed is not set.
func emitChangedSelectionSummary(w io.Writer, req archtestrunner.Request, selectedCount int) {
	if !req.Changed {
		return
	}
	// Note the active scope so a 0-result under --scope=framework --changed reads as
	// "the framework set ∩ changed was empty", not "the full suite passed".
	scopeNote := ""
	if req.Scope == archtestrunner.ScopeFramework {
		scopeNote = " --scope=framework"
	}
	// Best-effort diagnostic line; a stderr write failure must not fail the run.
	_, _ = fmt.Fprintf(w,
		"archtest --changed%s: %d test function(s) selected to run (scan-domain matches + undeterminable-scope rules);"+
			" this is a pre-filter, not the authoritative full run\n", scopeNote, selectedCount)
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
	emitChangedSelectionSummary(os.Stderr, req, len(names))
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

	// Under --changed, make the selection explicit on stderr (shared with
	// --list-tests) so a clean exit with 0 rules selected is not misread as
	// "the full suite passed" — the full sharded run remains authoritative.
	emitChangedSelectionSummary(os.Stderr, req, len(report.Selected))

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
