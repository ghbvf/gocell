package archtestrunner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/ghbvf/gocell/framework/pkg/cmdrun"
)

// execFn is the function signature for running a subprocess (go test, go list).
// Injected via engine to enable unit-testing without spawning real subprocesses.
type execFn func(ctx context.Context, dir string, extraEnv, args []string) ([]byte, error)

// changedFilesFn is the function signature for retrieving the repo-relative
// changed files (of any kind) vs origin/develop. Injected via engine to enable
// unit-testing without a live git repo.
type changedFilesFn func(ctx context.Context, workspaceRoot string) ([]string, error)

// engine holds the injected subprocess functions.
// Production callers use newEngine(); tests construct engine{exec: fakeExec, changed: fakeChanged}.
type engine struct {
	exec    execFn
	changed changedFilesFn
}

// newEngine returns an engine wired to the real go tool and git implementations.
func newEngine() engine {
	return engine{
		exec:    defaultExec,
		changed: changedRepoFiles,
	}
}

// defaultExec resolves the go tool via cmdrun.NewTool and calls cmdrun.RunWith.
// This is the production subprocess entry point; semantics are unchanged from
// the former package-level runGoCommand variable.
func defaultExec(ctx context.Context, dir string, _ []string, args []string) ([]byte, error) {
	goTool, err := cmdrun.NewTool(goToolName())
	if err != nil {
		return nil, fmt.Errorf("archtestrunner: resolve go tool: %w", err)
	}
	return cmdrun.RunWith(ctx, goTool, cmdrun.RunOptions{
		Dir:      filepath.Clean(dir),
		ExtraEnv: goTestExtraEnv(goTool.Dir()),
	}, args...)
}

// Run discovers → selects (shard ∩ rule|changed) → executes
// `go test -tags=archtest -json -run ...` → parses → Report.
//
// Returns a non-nil error ONLY for infrastructure failures (GOWORK=off, go
// tool missing, build error that prevents any test from running, json parse
// impossibility). Test FAILURES are NOT errors — they live in Report
// (Passed=false).
func Run(ctx context.Context, req Request) (Report, error) {
	return newEngine().run(ctx, req)
}

// ListTests returns the selected test names (discovery + shard/rule/changed
// selection) WITHOUT executing them. Backs the CLI `--list-tests` flag.
//
// The result is deterministic: sorted discovery + stable partition.
func ListTests(ctx context.Context, req Request) ([]string, error) {
	return newEngine().listTests(ctx, req)
}

// run is the engine-scoped implementation of Run.
func (e engine) run(ctx context.Context, req Request) (Report, error) {
	if err := checkGOWORK(); err != nil {
		return Report{}, err
	}
	if err := validateShard(req.Shard); err != nil {
		return Report{}, err
	}

	selected, err := e.resolveSelected(ctx, req)
	if err != nil {
		return Report{}, err
	}

	if len(selected) == 0 {
		// Empty selection: still create the (empty) TestJSONOut so CI's slowgate
		// pipe does not fail on a missing file.
		if werr := writeJSONArtifactIfRequested(req, nil); werr != nil {
			return Report{}, werr
		}
		return buildEmptyReport(), nil
	}

	output, runErr := e.execGoTest(ctx, req, selected)

	// Write the JSON artifact BEFORE run-error classification so it exists even
	// when the run is a build/package failure.
	if werr := writeJSONArtifactIfRequested(req, output); werr != nil {
		return Report{}, werr
	}

	tests, perr := parseTestJSON(output)
	if perr != nil {
		return Report{}, perr
	}

	// classifyRunError needs the parsed results: a non-zero exit with zero
	// test-level results is a build/package failure (surfaced as an infra error
	// with the raw diagnostic), not an empty "0 failing tests" report.
	if infraErr := classifyRunError(runErr, tests, output); infraErr != nil {
		return Report{}, infraErr
	}

	enrichWithMeta(ctx, req.WorkspaceRoot, tests)

	passed := runErr == nil && !hasFailures(tests)
	return Report{
		Selected: selected,
		Tests:    tests,
		Passed:   passed,
	}, nil
}

// writeJSONArtifactIfRequested writes the valid JSON event lines from output to
// req.TestJSONOut when set (no-op when empty). CI's slowgate pipe (`< file`)
// must not fail on a missing file, so the file is always created when the flag
// is set — even for empty output, which is a valid empty JSON stream.
func writeJSONArtifactIfRequested(req Request, output []byte) error {
	if req.TestJSONOut == "" {
		return nil
	}
	validLines, err := collectValidJSONLines(output)
	if err != nil {
		return err
	}
	return writeTestJSONOut(req.TestJSONOut, validLines)
}

// listTests is the engine-scoped implementation of ListTests.
func (e engine) listTests(ctx context.Context, req Request) ([]string, error) {
	if err := checkGOWORK(); err != nil {
		return nil, err
	}
	if err := validateShard(req.Shard); err != nil {
		return nil, err
	}

	return e.resolveSelected(ctx, req)
}

// resolveSelected runs discovery and applies all selection filters.
func (e engine) resolveSelected(ctx context.Context, req Request) ([]string, error) {
	discovered, err := e.discoverTests(ctx, req.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	return e.applyFilters(ctx, req, discovered)
}

// applyFilters applies shard / rule / changed filters to a pre-discovered test list.
// Extracted as a pure-ish helper to allow testing without a real `go test -list` call.
func (e engine) applyFilters(ctx context.Context, req Request, discovered []string) ([]string, error) {
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

	// Apply changed-source filter: keep only rules a changed file could affect.
	if req.Changed {
		changedFiles, err := e.changed(ctx, req.WorkspaceRoot)
		if err != nil {
			return nil, err
		}
		selected, err = selectByChangedSource(req.WorkspaceRoot, selected, changedFiles)
		if err != nil {
			return nil, err
		}
	}

	return selected, nil
}

// selectByChangedSource returns the subset of selected rules (test funcs) that a
// changed file could affect (gh #1877). A rule is kept when either:
//   - its own *_test.go file changed (mechanical subsumption, via
//     changedFilesToTests, which self-filters to archtest files), or
//   - a changed source file falls within the rule's static scan domain.
//
// Rules whose scan domain cannot be statically determined (the zero-value
// fileDomain returned for an absent index key) always match — no false
// negatives. Selection preserves the discovery order of `selected`.
func selectByChangedSource(workspaceRoot string, selected, changedFiles []string) ([]string, error) {
	domainIdx, err := buildFileDomainIndex(workspaceRoot)
	if err != nil {
		return nil, err
	}
	selfTests, err := changedFilesToTests(workspaceRoot, changedFiles)
	if err != nil {
		return nil, err
	}
	selfSet := make(map[string]bool, len(selfTests))
	for _, name := range selfTests {
		selfSet[name] = true
	}

	normChanged := make([]string, len(changedFiles))
	for i, c := range changedFiles {
		normChanged[i] = filepath.ToSlash(c)
	}

	var out []string
	for _, name := range selected {
		if selfSet[name] || anyChangeSelects(domainIdx[name], normChanged) {
			out = append(out, name)
		}
	}
	return out, nil
}

// anyChangeSelects reports whether any changed file selects the given domain.
func anyChangeSelects(d fileDomain, changed []string) bool {
	for _, c := range changed {
		if domainSelectsChange(d, c) {
			return true
		}
	}
	return false
}

// discoverTests runs `go test -tags=archtest -list '^Test' ./tools/archtest`
// and returns the sorted list of Test* function names.
func (e engine) discoverTests(ctx context.Context, workspaceRoot string) ([]string, error) {
	args := buildDiscoverArgs()
	output, runErr := e.exec(ctx, workspaceRoot, nil, args)
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
func (e engine) execGoTest(ctx context.Context, req Request, selected []string) ([]byte, error) {
	args := buildTestArgs(req.Timeout, selected)
	return e.exec(ctx, req.WorkspaceRoot, nil, args)
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

// checkGOWORK returns an error if GOWORK=off. workspace modules are required
// for packages.Load; without the workspace the archtest suite cannot load the
// multi-module repo graph.
func checkGOWORK() error {
	if os.Getenv("GOWORK") == "off" {
		return fmt.Errorf("archtestrunner: GOWORK=off — workspace modules are required for packages.Load;" +
			" unset GOWORK or run without GOWORK=off (e.g. 'unset GOWORK && gocell verify archtest')")
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
