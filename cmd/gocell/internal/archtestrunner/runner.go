package archtestrunner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/ghbvf/gocell/pkg/cmdrun"
)

// Run discovers → selects (shard ∩ rule|changed) → executes
// `go test -tags=archtest -json -run ...` → parses → Report.
//
// Returns a non-nil error ONLY for infrastructure failures (GOWORK=off, go
// tool missing, build error that prevents any test from running, json parse
// impossibility). Test FAILURES are NOT errors — they live in Report
// (Passed=false).
func Run(ctx context.Context, req Request) (Report, error) {
	if err := checkGOWORK(); err != nil {
		return Report{}, err
	}
	if err := validateShard(req.Shard); err != nil {
		return Report{}, err
	}

	selected, err := resolveSelected(ctx, req)
	if err != nil {
		return Report{}, err
	}

	if len(selected) == 0 {
		return buildEmptyReport(), nil
	}

	output, runErr := execGoTest(ctx, req, selected)

	// Write JSON out before processing results, if requested.
	if req.TestJSONOut != "" && len(output) > 0 {
		validLines := collectValidJSONLines(output)
		if werr := writeTestJSONOut(req.TestJSONOut, validLines); werr != nil {
			return Report{}, werr
		}
	}

	infraErr := determineInfraErr(runErr)
	if infraErr != nil {
		return Report{}, fmt.Errorf("archtestrunner: go test execution failed: %w", infraErr)
	}

	tests := parseTestJSON(output)
	enrichWithMeta(ctx, req.WorkspaceRoot, tests)

	passed := runErr == nil && !hasFailures(tests)
	return Report{
		Selected: selected,
		Tests:    tests,
		Passed:   passed,
	}, nil
}

// ListTests returns the selected test names (discovery + shard/rule/changed
// selection) WITHOUT executing them. Backs the CLI `--list-tests` flag.
//
// The result is deterministic: sorted discovery + stable partition.
func ListTests(ctx context.Context, req Request) ([]string, error) {
	if err := checkGOWORK(); err != nil {
		return nil, err
	}
	if err := validateShard(req.Shard); err != nil {
		return nil, err
	}

	return resolveSelected(ctx, req)
}

// resolveSelected runs discovery and applies all selection filters.
func resolveSelected(ctx context.Context, req Request) ([]string, error) {
	discovered, err := discoverTests(ctx, req.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	return applyFilters(ctx, req, discovered)
}

// applyFilters applies shard / rule / changed filters to a pre-discovered test list.
// Extracted as a pure-ish helper to allow testing without a real `go test -list` call.
func applyFilters(ctx context.Context, req Request, discovered []string) ([]string, error) {
	// Apply shard partitioning first.
	selected := applyShardSelection(discovered, req.Shard)

	// Apply rule filter (intersect).
	if req.Rule != "" {
		idx, err := buildRuleIndex(req.WorkspaceRoot)
		if err != nil {
			return nil, err
		}
		selected, err = selectByRule(idx, req.Rule, selected)
		if err != nil {
			return nil, err
		}
	}

	// Apply changed-files filter (intersect).
	if req.Changed {
		changedFiles, err := changedArchtestFiles(ctx, req.WorkspaceRoot)
		if err != nil {
			return nil, err
		}
		changedTests, err := changedFilesToTests(req.WorkspaceRoot, changedFiles)
		if err != nil {
			return nil, err
		}
		selected = intersect(selected, changedTests)
	}

	return selected, nil
}

// discoverTests runs `go test -tags=archtest -list '^Test' ./tools/archtest`
// and returns the sorted list of Test* function names.
func discoverTests(ctx context.Context, workspaceRoot string) ([]string, error) {
	goTool, err := cmdrun.NewTool(goToolName())
	if err != nil {
		return nil, fmt.Errorf("archtestrunner: resolve go tool: %w", err)
	}

	args := buildDiscoverArgs()
	output, runErr := cmdrun.RunWith(ctx, goTool, cmdrun.RunOptions{
		Dir:      filepath.Clean(workspaceRoot),
		ExtraEnv: goTestExtraEnv(goTool.Dir()),
	}, args...)

	if runErr != nil {
		return nil, fmt.Errorf("archtestrunner: discover archtest tests: %w", runErr)
	}

	tests := parseDiscoveryOutput(string(output))
	if len(tests) == 0 {
		return nil, fmt.Errorf("archtestrunner: no archtest Test* functions discovered in ./tools/archtest")
	}
	return tests, nil
}

// execGoTest runs `go test -tags=archtest -json -run '^(...)$' ./tools/archtest`
// and returns the combined output and any run error.
func execGoTest(ctx context.Context, req Request, selected []string) ([]byte, error) {
	goTool, err := cmdrun.NewTool(goToolName())
	if err != nil {
		return nil, fmt.Errorf("archtestrunner: resolve go tool: %w", err)
	}

	args := buildTestArgs(req.Timeout, selected)
	output, runErr := cmdrun.RunWith(ctx, goTool, cmdrun.RunOptions{
		Dir:      filepath.Clean(req.WorkspaceRoot),
		ExtraEnv: goTestExtraEnv(goTool.Dir()),
	}, args...)
	return output, runErr
}

// enrichWithMeta populates TestResult.File and TestResult.Rules using the
// file-meta map built from the INVARIANT anchors. Errors during meta-map
// building are non-fatal (fields are left empty).
func enrichWithMeta(_ context.Context, workspaceRoot string, tests []TestResult) {
	meta, err := buildFileMetaMap(workspaceRoot)
	if err != nil {
		return // best-effort; leave File/Rules empty
	}
	for i := range tests {
		if m, ok := meta[tests[i].Name]; ok {
			tests[i].File = m.file
			tests[i].Rules = m.rules
		}
	}
}

// hasFailures reports whether any test result has status "fail".
func hasFailures(tests []TestResult) bool {
	for _, t := range tests {
		if t.Status == "fail" {
			return true
		}
	}
	return false
}

// intersect returns the elements of a that are also in b, preserving a's order.
func intersect(a, b []string) []string {
	bSet := make(map[string]bool, len(b))
	for _, s := range b {
		bSet[s] = true
	}
	out := make([]string, 0, len(a))
	for _, s := range a {
		if bSet[s] {
			out = append(out, s)
		}
	}
	return out
}

// checkGOWORK returns an error if GOWORK=off. workspace modules are required
// for packages.Load; without the workspace the archtest suite cannot load the
// multi-module repo graph.
func checkGOWORK() error {
	if os.Getenv("GOWORK") == "off" {
		return fmt.Errorf("archtestrunner: GOWORK=off — workspace modules are required for packages.Load; do not run with GOWORK=off")
	}
	return nil
}

// goToolName returns "go" on Unix and "go.exe" on Windows.
func goToolName() string {
	if runtime.GOOS == "windows" {
		return "go.exe"
	}
	return "go"
}

// goTestExtraEnv returns an additive PATH override that puts goToolDir first,
// so go-toolchain-internal helpers resolve to the toolchain that owns the go
// binary. Mirrors kernel/verify/gotest.go's goTestExtraEnv.
func goTestExtraEnv(goToolDir string) []string {
	if goToolDir == "" {
		return nil
	}
	pathKey, pathValue := currentPathEnv()
	return []string{pathKey + "=" + prependToPath(goToolDir, pathValue)}
}

func currentPathEnv() (string, string) {
	for _, kv := range os.Environ() {
		k, v, ok := splitEnvKV(kv)
		if ok && isPathKey(k) {
			return k, v
		}
	}
	return "PATH", ""
}

func splitEnvKV(kv string) (string, string, bool) {
	for i, c := range kv {
		if c == '=' {
			return kv[:i], kv[i+1:], true
		}
	}
	return "", "", false
}

func isPathKey(key string) bool {
	if runtime.GOOS == "windows" {
		return len(key) == 4 && (key == "PATH" || key == "path" || key == "Path")
	}
	return key == "PATH"
}

func prependToPath(dir, existing string) string {
	if dir == "" {
		return existing
	}
	if existing == "" {
		return dir
	}
	return dir + string(os.PathListSeparator) + existing
}

// sortedKeys returns sorted keys of a string set (used for deterministic output).
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
