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

// parseTestJSON parses the combined output of `go test -json` and returns
// per-test results. Non-JSON lines (build noise written to stderr and merged
// by cmdrun's CombinedOutput) are silently skipped.
//
// Aggregation rules:
//   - The final "pass", "fail", or "skip" action for a test name determines Status.
//   - Output lines for the test are concatenated (only kept for failed tests).
//   - Elapsed from the final action is used.
//   - Package-level events (Test=="") are ignored.
func parseTestJSON(output []byte) []TestResult {
	state := make(map[string]*testAccumulator)
	parseTestEvents(output, state)
	return buildTestResults(state)
}

// parseTestEvents scans the combined `go test -json` output and accumulates
// per-test state. Non-JSON lines are silently skipped.
func parseTestEvents(output []byte, state map[string]*testAccumulator) {
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := scanner.Text()
		if !looksLikeJSON(line) {
			continue
		}
		var ev testEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Test == "" {
			continue // package-level event
		}
		acc, ok := state[ev.Test]
		if !ok {
			acc = &testAccumulator{}
			state[ev.Test] = acc
		}
		applyTestEvent(acc, ev)
	}
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
// write clean input to TestJSONOut for slowgate consumption.
func collectValidJSONLines(output []byte) [][]byte {
	var result [][]byte
	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		line := scanner.Bytes()
		if !looksLikeJSON(string(line)) {
			continue
		}
		var ev struct {
			Action string `json:"Action"`
		}
		if err := json.Unmarshal(line, &ev); err != nil || ev.Action == "" {
			continue
		}
		cp := make([]byte, len(line))
		copy(cp, line)
		result = append(result, cp)
	}
	return result
}

// determineInfraErr distinguishes between test failures (exit error) and
// infrastructure errors (tool not found, build error, etc.).
//
//   - nil error → no error at all (tests passed)
//   - *exec.ExitError → tests ran and some failed; this is NOT an infra error
//   - any other error → infra error (caller should return it, not set Passed=false)
func determineInfraErr(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil // test failure, not infra failure
	}
	return err
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
