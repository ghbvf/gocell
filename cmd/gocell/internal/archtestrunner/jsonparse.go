package archtestrunner

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// testEvent is the shape of a single `go test -json` event line.
// Only the fields we care about are decoded; the rest are ignored.
type testEvent struct {
	Action  string  `json:"Action"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
	Output  string  `json:"Output"`
}

// testAccumulator collects output, elapsed, and final status for a single test.
type testAccumulator struct {
	outputs []string
	elapsed float64
	status  string
}

// maxJSONLineBytes bounds a single `go test -json` event line. The default
// bufio.Scanner cap (bufio.MaxScanTokenSize, 64 KiB) silently STOPS scanning on
// a longer line, which for archtest means a long failure diff or a long test
// list would truncate both the report and the slowgate --test-json-out artifact
// with no signal. 10 MiB comfortably exceeds any single event Output chunk;
// exceeding it surfaces as a scanner error (see scanJSONLines), never a silent
// drop.
const maxJSONLineBytes = 10 << 20

// scanJSONLines invokes fn for each line of output, using a scanner whose token
// cap is raised to maxJSONLineBytes, and returns any scanner error. A line
// exceeding the cap yields bufio.ErrTooLong rather than a silent truncation, so
// callers MUST propagate the error instead of treating the stream as complete.
func scanJSONLines(output []byte, fn func(line []byte)) error {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 0, 64*1024), maxJSONLineBytes)
	for scanner.Scan() {
		fn(scanner.Bytes())
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("archtestrunner: scan go test -json output: %w", err)
	}
	return nil
}

// parseTestJSON parses the combined output of `go test -json` and returns
// per-test results. Non-JSON lines (build noise written to stderr and merged
// by cmdrun's CombinedOutput) are silently skipped. It returns an error only
// when the output stream cannot be fully scanned (e.g. a line exceeding
// maxJSONLineBytes) — a truncated parse would yield a silently incomplete
// report, so the caller must treat that as a failure rather than trusting it.
//
// Aggregation rules:
//   - The final "pass", "fail", or "skip" action for a test name determines Status.
//   - Output lines for the test are concatenated (only kept for failed tests).
//   - Elapsed from the final action is used.
//   - Package-level events (Test=="") are ignored.
func parseTestJSON(output []byte) ([]TestResult, error) {
	state := make(map[string]*testAccumulator)
	if err := parseTestEvents(output, state); err != nil {
		return nil, err
	}
	return buildTestResults(state), nil
}

// parseTestEvents scans the combined `go test -json` output and accumulates
// per-test state. Non-JSON lines are silently skipped; a scan error (truncated
// stream) is returned, never swallowed.
func parseTestEvents(output []byte, state map[string]*testAccumulator) error {
	return scanJSONLines(output, func(line []byte) {
		if !looksLikeJSON(string(line)) {
			return
		}
		var ev testEvent
		if err := json.Unmarshal(line, &ev); err != nil {
			return
		}
		if ev.Test == "" {
			return // package-level event
		}
		acc, ok := state[ev.Test]
		if !ok {
			acc = &testAccumulator{}
			state[ev.Test] = acc
		}
		applyTestEvent(acc, ev)
	})
}

// applyTestEvent updates the accumulator with a single test event.
func applyTestEvent(acc *testAccumulator, ev testEvent) {
	switch ev.Action {
	case "output":
		acc.outputs = append(acc.outputs, ev.Output)
	case "pass", "fail", "skip":
		acc.status = ev.Action
		acc.elapsed = ev.Elapsed
	}
}

// buildTestResults converts the accumulated state to a TestResult slice.
func buildTestResults(state map[string]*testAccumulator) []TestResult {
	results := make([]TestResult, 0, len(state))
	for name, acc := range state {
		if acc.status == "" {
			continue // no final action seen — incomplete (e.g. build failure)
		}
		tr := TestResult{
			Name:    name,
			Status:  acc.status,
			Elapsed: acc.elapsed,
		}
		if acc.status == "fail" {
			tr.Output = strings.Join(acc.outputs, "")
		}
		results = append(results, tr)
	}
	return results
}

// looksLikeJSON reports whether line starts with '{', indicating a JSON object.
// This fast pre-check avoids json.Unmarshal overhead for build-noise lines.
func looksLikeJSON(line string) bool {
	line = strings.TrimSpace(line)
	return len(line) > 0 && line[0] == '{'
}

// collectValidJSONLines returns the subset of lines from output that are valid
// JSON objects with an "Action" field (i.e. genuine test2json events). Used to
// write clean input to TestJSONOut for slowgate consumption. A scan error
// (truncated stream) is returned rather than producing a silently incomplete
// artifact.
func collectValidJSONLines(output []byte) ([][]byte, error) {
	var result [][]byte
	err := scanJSONLines(output, func(line []byte) {
		if !looksLikeJSON(string(line)) {
			return
		}
		var ev struct {
			Action string `json:"Action"`
		}
		if uerr := json.Unmarshal(line, &ev); uerr != nil || ev.Action == "" {
			return
		}
		cp := make([]byte, len(line))
		copy(cp, line)
		result = append(result, cp)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// maxBuildDiagnosticBytes caps the build/package-failure diagnostic surfaced
// inline in the infra error. The full event stream is still written to the
// --test-json-out artifact; this is the operator-facing excerpt that makes the
// CLI's own error message actionable.
const maxBuildDiagnosticBytes = 4096

// buildFailureDiagnostic reconstructs human-readable text from raw `go test
// -json` output for the path where go test failed but produced NO per-test
// result (build error, package panic, or timeout before any test completed).
// It concatenates every event's Output field — build-output and package-level
// events have Test=="" and are otherwise dropped by parseTestJSON — and passes
// through non-JSON lines (build errors go test writes to stderr ahead of the
// JSON stream). The result is trimmed and length-capped. A scan error is
// returned so an unreadable stream never masquerades as an empty diagnostic.
func buildFailureDiagnostic(output []byte) (string, error) {
	var b strings.Builder
	err := scanJSONLines(output, func(line []byte) {
		if !looksLikeJSON(string(line)) {
			b.Write(line)
			b.WriteByte('\n')
			return
		}
		var ev testEvent
		if uerr := json.Unmarshal(line, &ev); uerr != nil {
			return
		}
		b.WriteString(ev.Output)
	})
	if err != nil {
		return "", err
	}
	diag := strings.TrimSpace(b.String())
	if len(diag) > maxBuildDiagnosticBytes {
		diag = diag[:maxBuildDiagnosticBytes] + "\n… (truncated; see --test-json-out artifact for the full stream)"
	}
	return diag, nil
}

// classifyRunError decides whether a `go test` run error is an infrastructure
// failure (returned to the caller, aborting the Report) or an ordinary test
// failure (recorded in the Report with Passed=false). The decision needs the
// PARSED results, because go test exits non-zero for BOTH "some tests failed"
// and "the package failed to build / panicked / timed out before any test
// ran" — the two are indistinguishable from the error type alone.
//
//   - nil                              → no error.
//   - non-*exec.ExitError              → infra (go tool missing, ctx cancel).
//   - *exec.ExitError, len(tests) > 0  → ordinary test failure (nil); failures
//     live in the Report.
//   - *exec.ExitError, len(tests) == 0 → build/package failure: go test -json
//     emitted no per-test event, so an empty Report would render as a
//     misleading "0 failing tests". Surface the raw diagnostic as an infra
//     error so the operator sees the actual compiler/panic output.
func classifyRunError(runErr error, tests []TestResult, output []byte) error {
	if runErr == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if !errors.As(runErr, &exitErr) {
		return fmt.Errorf("archtestrunner: go test execution failed: %w", runErr)
	}
	if len(tests) > 0 {
		return nil // tests ran; failures are reported, not an infra error
	}
	diag, derr := buildFailureDiagnostic(output)
	if derr != nil {
		return fmt.Errorf("archtestrunner: `go test` failed with no test results, "+
			"and its output could not be scanned: %w", derr)
	}
	return fmt.Errorf("archtestrunner: archtest produced no test results but `go test` failed "+
		"(build error, package panic, or timeout before any test completed):\n%s", diag)
}

// writeTestJSONOut writes valid JSON event lines to the specified file.
// Each line is written followed by a newline. Errors are wrapped with context.
// The path comes from a CLI-controlled Request field, not user input.
func writeTestJSONOut(path string, lines [][]byte) error {
	f, err := os.Create(path) //nolint:gosec // path is CLI-controlled, not user input
	if err != nil {
		return fmt.Errorf("archtestrunner: create TestJSONOut %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	for _, line := range lines {
		if _, err := f.Write(append(line, '\n')); err != nil {
			return fmt.Errorf("archtestrunner: write TestJSONOut %s: %w", path, err)
		}
	}
	return nil
}
