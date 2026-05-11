package archtest

// INVARIANT: ARCHTEST-VERIFY-COVERAGE-01
//
//   - discovery: script's discovered Test* set == top-level *_test.go AST scan
//   - partition: shard_assignment is an exactly-once cover of the discovered
//     set (no test runs in two shards, no test runs in zero shards)
//
// archtest_verify_coverage_test.go — guard hack/verify-archtest.sh from
// two failure modes:
//
//  1. Discovery drift — `go test -list '^Test' | grep '^Test' | sort` could
//     silently shrink if a maintainer adds `| grep -v TestFoo` (debug
//     breadcrumb not removed) or narrows the regex. Discovery-vs-AST cross-
//     check catches this in CI before the loss reaches develop.
//
//  2. Dispatch drift — the modulo partition that routes tests to shards
//     could be broken (off-by-one in `NR % n == s + 1`, or replaced with
//     something that double-counts) without changing discovery. The
//     partition exactly-once check loops every shard via the script's own
//     LIST_SHARD_TESTS mode, asserts the union equals discovery and that
//     no name appears in two shards — independent of K (test uses K=4 to
//     run fast; correctness of the algorithm is K-independent).
//
// AI-rebust: Medium (runtime cross-check against AST + algorithm
// conformance test going through the same shard_assignment shell function
// that real execution uses — single algorithm source).
//
// Cannot funnel: the script is a shell entry point; codegen-ing the
// discovery list into the script body would replace runtime ground truth
// with a build-time snapshot — more drift risk, not less.

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestArchtestVerifyCoverage01 runs both arms of the invariant:
//
//   - Discovery cross-check (script DRY_RUN set == AST scan set)
//   - Dispatch partition exactly-once (union of shard assignments == discovery
//     set; no name appears in two shards)
//
// Either failure mode would be silent under the legacy script (single bash
// pipeline, no Go-side verification) and would erode CI coverage without
// any developer-visible signal.
func TestArchtestVerifyCoverage01(t *testing.T) {
	t.Parallel()
	repoRoot := findModuleRoot(t)

	scriptSet, err := runVerifyArchtestDryRun(repoRoot)
	if err != nil {
		t.Fatalf("verify-archtest.sh DRY_RUN failed: %v", err)
	}
	astSet := scanArchtestTopLevelTestNames(t, repoRoot)

	missingFromScript := setDifference(astSet, scriptSet)
	missingFromAST := setDifference(scriptSet, astSet)
	if len(missingFromScript) > 0 || len(missingFromAST) > 0 {
		var msg []string
		if len(missingFromScript) > 0 {
			msg = append(msg,
				"verify-archtest.sh discovery is MISSING tests that exist in tools/archtest/*_test.go:",
				"  "+strings.Join(missingFromScript, "\n  "),
			)
		}
		if len(missingFromAST) > 0 {
			msg = append(msg,
				"verify-archtest.sh discovery reports tests that AST scan cannot find under tools/archtest/ (excluding internal/):",
				"  "+strings.Join(missingFromAST, "\n  "),
			)
		}
		t.Fatalf("ARCHTEST-VERIFY-COVERAGE-01 (discovery): script discovery diverges from AST scan.\n%s",
			strings.Join(msg, "\n"))
	}

	// Partition exactly-once. K=4 is arbitrary; the modulo algorithm's
	// correctness is independent of K, so a small K runs faster.
	assertShardPartitionExactlyOnce(t, repoRoot, scriptSet, 4)
}

// assertShardPartitionExactlyOnce loops SHARD_TARGET=0..k-1, captures each
// shard's assignment via `LIST_SHARD_TESTS=1`, and verifies:
//   - every name in scriptSet appears in at least one shard (union == cover)
//   - no name appears in two or more shards (disjoint)
//
// Both go through the script's shard_assignment() bash function — the same
// function run_shard() uses — so this is single-source algorithm verification.
func assertShardPartitionExactlyOnce(t *testing.T, repoRoot string, scriptSet map[string]struct{}, k int) {
	t.Helper()
	assignmentOf := map[string]int{} // test name -> shard index (-1 = duplicate)
	for s := 0; s < k; s++ {
		shardSet, err := runVerifyArchtestListShard(repoRoot, k, s)
		if err != nil {
			t.Fatalf("verify-archtest.sh LIST_SHARD_TESTS SHARD_TARGET=%d SHARD_COUNT=%d failed: %v", s, k, err)
		}
		for name := range shardSet {
			if prev, dup := assignmentOf[name]; dup {
				t.Errorf("ARCHTEST-VERIFY-COVERAGE-01 (partition): %q assigned to both shard %d and shard %d (K=%d) — modulo algorithm broken",
					name, prev, s, k)
				continue
			}
			assignmentOf[name] = s
		}
	}
	// Cover check: every discovered name in some shard.
	var missing []string
	for name := range scriptSet {
		if _, ok := assignmentOf[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("ARCHTEST-VERIFY-COVERAGE-01 (partition): "+
			"%d discovered tests unassigned across K=%d shards "+
			"— partition not covering full set:\n  %s",
			len(missing), k, strings.Join(missing, "\n  "))
	}
	// Reverse check: every assigned name is in scriptSet (catches script
	// inventing names — would be a regression of the discovery filter).
	var extra []string
	for name := range assignmentOf {
		if _, ok := scriptSet[name]; !ok {
			extra = append(extra, name)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		t.Fatalf("ARCHTEST-VERIFY-COVERAGE-01 (partition): "+
			"K=%d shards reported names not in discovery set:\n  %s",
			k, strings.Join(extra, "\n  "))
	}
}

// runVerifyArchtestDryRun invokes the script with DRY_RUN=1 and returns
// the set of Test* function names it would dispatch to shards.
//
// gosec G204 is suppressed: repoRoot is the go.mod-bearing ancestor of the
// archtest test binary's working directory (findModuleRoot), not user
// input. The path is fully controlled by the test runner.
//
// A 2-minute deadline guards against `go test -list` hanging on a broken
// toolchain or build error — the surrounding archtest's own 5m -timeout
// also catches it, but this gives a more targeted error message.
func runVerifyArchtestDryRun(repoRoot string) (map[string]struct{}, error) {
	return runVerifyArchtestForNames(repoRoot, []string{"DRY_RUN=1"})
}

// runVerifyArchtestListShard invokes the script in LIST_SHARD_TESTS mode
// for one (shardCount, shardTarget) tuple and returns the assigned set.
func runVerifyArchtestListShard(repoRoot string, shardCount, shardTarget int) (map[string]struct{}, error) {
	return runVerifyArchtestForNames(repoRoot, []string{
		"LIST_SHARD_TESTS=1",
		fmt.Sprintf("SHARD_COUNT=%d", shardCount),
		fmt.Sprintf("SHARD_TARGET=%d", shardTarget),
	})
}

func runVerifyArchtestForNames(repoRoot string, extraEnv []string) (map[string]struct{}, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	//nolint:gosec // G204: repoRoot is the discovered go.mod ancestor (test-time, no user input)
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(repoRoot, "hack", "verify-archtest.sh"))
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Dir = repoRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, &dryRunError{err: err, stderr: stderr.String()}
	}
	out := map[string]struct{}{}
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Test") {
			continue
		}
		out[line] = struct{}{}
	}
	return out, nil
}

type dryRunError struct {
	err    error
	stderr string
}

func (e *dryRunError) Error() string {
	if e.stderr != "" {
		return e.err.Error() + ": stderr=" + strings.TrimSpace(e.stderr)
	}
	return e.err.Error()
}

// scanArchtestTopLevelTestNames AST-scans tools/archtest/*_test.go (excluding
// internal/ subpackages — the script targets only the top-level archtest
// Go package via `go test ./tools/archtest` without `...`) for top-level
// `func TestX(t *testing.T)` declarations and returns the set of names.
func scanArchtestTopLevelTestNames(t *testing.T, repoRoot string) map[string]struct{} {
	t.Helper()
	scope := scanner.DirsScope(repoRoot, []string{"tools/archtest"}, scanner.IncludeTests())

	names := map[string]struct{}{}
	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		// Only the archtest top-level Go package (matches the script's
		// `./tools/archtest` non-recursive package selector). Subpackages
		// like internal/scanner and internal/typeseval have their own
		// `go test ./tools/archtest/internal/...` entry.
		if filepath.ToSlash(filepath.Dir(fc.Rel)) != "tools/archtest" {
			return
		}
		if !strings.HasSuffix(fc.Rel, "_test.go") {
			return
		}
		scanner.EachNode[ast.FuncDecl](fc.File, func(fd *ast.FuncDecl) {
			if fd.Recv != nil { // method — not a Test* function
				return
			}
			if fd.Name == nil || !strings.HasPrefix(fd.Name.Name, "Test") {
				return
			}
			if !isStandardTestSignature(fd.Type) {
				return
			}
			names[fd.Name.Name] = struct{}{}
		})
	})
	return names
}

// isStandardTestSignature reports whether ft matches `func(t *testing.T)`,
// which is the only signature `go test -list` admits as a Test* function.
// Helper functions named TestXxx but with other signatures are correctly
// excluded — `go test -list` skips them too.
func isStandardTestSignature(ft *ast.FuncType) bool {
	if ft == nil || ft.Params == nil || len(ft.Params.List) != 1 {
		return false
	}
	field := ft.Params.List[0]
	if len(field.Names) != 1 {
		return false
	}
	ptr, ok := field.Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := ptr.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "testing" && sel.Sel.Name == "T"
}

func setDifference(a, b map[string]struct{}) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
