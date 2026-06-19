// Package archtestrunner is the single Go executor for the GoCell archtest
// suite. It replaces the legacy hack/verify-archtest.sh subprocess driver
// and provides a stable Go API for gocell verify archtest.
//
// # Shard partition algorithm
//
// partition is the single shard-modulo assignment implementation in the tree
// today. It mirrors the shell script's awk 'NR % n == s' 1-based line-number
// logic (a test at 0-based index i is assigned to shard (i+1) % Total), and the
// legacy hack/verify-archtest.sh awk implementation was DELETED, so only one
// exists by construction. This single-implementation property is documentation,
// NOT a machine-checked invariant: there is intentionally no static scanner
// against a future second implementation, so keep shard math in this function.
//
// What IS machine-enforced is the partition's exactly-once BEHAVIOR (every
// discovered test runs in exactly one shard): ARCHTEST-VERIFY-COVERAGE-01
// (tools/archtest) drives it through the CLI's --shard=N/K --list-tests path,
// and discover_test.go asserts the exact NR-mirror split to catch silent drift.
package archtestrunner

import (
	"fmt"
	"sort"
	"strings"
)

// Scope selects which test suite to run. Today only ScopeWorkspace is supported.
type Scope string

// ScopeWorkspace runs the full workspace archtest suite under ./tools/archtest.
const ScopeWorkspace Scope = "workspace"

// Shard selects one modulo partition of the test list.
// Total==0 means "no sharding" (run all tests).
type Shard struct {
	Index int
	Total int
}

// Request is the input to Run and ListTests.
type Request struct {
	// WorkspaceRoot is the repo root used as cwd for `go test` (from cmd/gocell findRoot()).
	// It is the subprocess-runner counterpart of tools/archtest.RuntimeScopeConfig.WorkspaceRoot()
	// — when the child `go test` process resolves its own in-process RuntimeScopeConfig, it
	// derives the workspace root from go.work discovery (WorkspaceRoot() is the result).
	// A future --scope flag (#2331) will allow callers to supply an explicit root here instead
	// of relying on process-cwd discovery inside the child.
	WorkspaceRoot string
	// Scope selects the test suite to run. Only [ScopeWorkspace] is supported today.
	// The in-process counterpart resolved by the child subprocess is
	// tools/archtest.RuntimeScopeConfig, which captures the full scope context
	// (workspace root, target module path, framework/platform paths, scan dirs).
	Scope       Scope  // default ScopeWorkspace
	Rule        string // optional INVARIANT rule ID, e.g. "LAYER-05"
	Changed     bool   // optional: select only rules a changed file could affect (#1877)
	Shard       Shard  // optional shard selection
	TestJSONOut string // optional file: write the raw `go test -json` event lines
	Timeout     string // `go test -timeout` value; default "5m" when empty
}

// TestResult holds the outcome of a single test function.
type TestResult struct {
	Name    string   // Test function name
	File    string   // best-effort owning *_test.go (repo-relative); "" if unknown
	Rules   []string // INVARIANT anchor IDs declared in File's header (may be empty)
	Status  string   // "pass" | "fail" | "skip"
	Elapsed float64  // seconds
	Output  string   // captured output (failures)
}

// Report is the result of a Run call.
type Report struct {
	Selected []string     // test names selected to run (post shard/rule/changed)
	Tests    []TestResult // per-test outcomes
	Passed   bool         // true iff go test exit==0 and no failures
}

// validateShard returns an error if shard parameters are out of range.
func validateShard(s Shard) error {
	if s.Total < 0 {
		return fmt.Errorf("archtestrunner: Shard.Total must be >= 0, got %d", s.Total)
	}
	if s.Total == 0 {
		return nil // no sharding
	}
	if s.Index < 0 || s.Index >= s.Total {
		return fmt.Errorf("archtestrunner: Shard.Index must be in [0, %d), got %d", s.Total, s.Index)
	}
	return nil
}

// partition returns the subset of sorted tests assigned to the given shard.
//
// Algorithm: mirrors the shell awk 'NR % n == s' where NR is 1-based.
// For 0-based index i in the sorted slice: test i is assigned to shard
// (i+1) % Total. When Total==0, all tests are returned unchanged.
//
// This is the single shard-assignment implementation (see the package doc); its
// exactly-once property is guarded behaviorally by ARCHTEST-VERIFY-COVERAGE-01.
//
// This is a pure function with no side effects. Callers must pass a
// pre-sorted slice to guarantee stable, deterministic sharding across
// invocations and replicas.
func partition(sorted []string, shard Shard) []string {
	if shard.Total == 0 {
		// No sharding: return all tests (copy to avoid aliasing).
		if len(sorted) == 0 {
			return []string{}
		}
		out := make([]string, len(sorted))
		copy(out, sorted)
		return out
	}
	out := make([]string, 0, len(sorted)/shard.Total+1)
	for i, name := range sorted {
		// 1-based NR: (i+1) % Total == Index
		if (i+1)%shard.Total == shard.Index {
			out = append(out, name)
		}
	}
	return out
}

// applyShardSelection applies shard partitioning to the discovered test list.
// Returns all tests when shard.Total==0.
func applyShardSelection(discovered []string, shard Shard) []string {
	return partition(discovered, shard)
}

// parseDiscoveryOutput parses the stdout of `go test -list '^Test'` and
// returns the sorted list of Test* function names. Non-test lines (build
// output, "ok  ./tools/archtest" summary) are filtered out.
func parseDiscoveryOutput(output string) []string {
	var tests []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "Test") {
			tests = append(tests, line)
		}
	}
	sort.Strings(tests)
	return tests
}

// buildDiscoverArgs returns the `go test` args for the discovery (-list) run.
func buildDiscoverArgs() []string {
	return []string{
		"test",
		"-tags=archtest",
		"-list",
		"^Test",
		"./tools/archtest",
	}
}

// buildTestArgs returns the `go test` args for the execution run.
// The returned slice does NOT include the "test" subcommand prefix
// (the caller prepends it).
func buildTestArgs(timeout string, selected []string) []string {
	if timeout == "" {
		timeout = "5m"
	}
	pattern := buildRunPattern(selected)
	return []string{
		"test",
		"-tags=archtest",
		"-count=1",
		"-timeout=" + timeout,
		"-json",
		"-run=" + pattern,
		"./tools/archtest",
	}
}

// buildRunPattern constructs the `-run` regex pattern from a list of test names.
// Pattern form: ^(Name1|Name2|...)$.
func buildRunPattern(names []string) string {
	return "^(" + strings.Join(names, "|") + ")$"
}

// buildEmptyReport returns a trivially passed Report for the case when no
// tests are selected (e.g. --changed with no archtest files modified).
func buildEmptyReport() Report {
	return Report{
		Selected: []string{},
		Tests:    []TestResult{},
		Passed:   true,
	}
}
