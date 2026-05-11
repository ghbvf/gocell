package archtest

// INVARIANT: ARCHTEST-VERIFY-COVERAGE-01
//
// archtest_verify_coverage_test.go — guard hack/verify-archtest.sh discovery
// from drifting away from the actual top-level Test* functions in
// tools/archtest/*_test.go.
//
// AI-rebust: Medium. Discovery in the script is `go test -list '^Test'`
// piped through `grep -E '^Test' | sort`. A maintainer adding a `grep -v
// TestFoo` filter (forgotten debug breadcrumb) or narrowing the regex
// pattern would silently skip tests in CI while the local developer-side
// `go test ./tools/archtest/...` keeps catching them. Cross-checking the
// script's DRY_RUN output against an AST scan of the archtest *_test.go
// files closes that gap.
//
// This rule is the only meta-rule the K=16 sharded verify-archtest pipeline
// needs (a sibling pkg-coverage rule was eliminated by computing the
// _build-lint.yml tools-shard pkgs via `go list ./tools/...` at runtime —
// no human-edited yml list to drift).
//
// Cannot funnel: the script is a shell entry point; the funnel candidate
// would be codegen-ing the discovery list into the script body, which adds
// more drift risk (codegen vs runtime go-list output) than it prevents.

import (
	"bytes"
	"go/ast"
	"go/parser"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestArchtestVerifyCoverage01 cross-checks that
// `DRY_RUN=1 bash hack/verify-archtest.sh` discovers the same set of
// top-level Test* functions that the archtest AST scan finds under
// tools/archtest/ (excluding internal/ subpackages, which run via their
// own `go test ./tools/archtest/internal/...` paths).
//
// Symmetric diff is fatal: either the script under-discovers (silent
// unenforce — the high-risk direction the rule was authored for) or the
// AST over-counts (e.g. a TestXxx helper added that go test legitimately
// can't run — must be renamed or excluded explicitly).
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
	if len(missingFromScript) == 0 && len(missingFromAST) == 0 {
		return
	}

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
	t.Fatalf("ARCHTEST-VERIFY-COVERAGE-01: script discovery diverges from AST scan.\n%s",
		strings.Join(msg, "\n"))
}

// runVerifyArchtestDryRun invokes the script with DRY_RUN=1 and returns
// the set of Test* function names it would dispatch to shards.
//
// gosec G204 is suppressed: repoRoot is the go.mod-bearing ancestor of the
// archtest test binary's working directory (findModuleRoot), not user
// input. The path is fully controlled by the test runner.
func runVerifyArchtestDryRun(repoRoot string) (map[string]struct{}, error) {
	//nolint:gosec // G204: repoRoot is the discovered go.mod ancestor (test-time, no user input)
	cmd := exec.Command("bash", filepath.Join(repoRoot, "hack", "verify-archtest.sh"))
	cmd.Env = append(os.Environ(), "DRY_RUN=1")
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
