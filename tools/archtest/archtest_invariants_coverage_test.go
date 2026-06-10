//go:build archtest

package archtest

// INVARIANT: ARCHTEST-INVARIANTS-COVERAGE-01
//
// archtest_invariants_coverage_test.go — guards hack/verify-archtest-invariants.sh
// against rename drift: every Test function named in the script's -run regex
// must exist as a top-level func TestXxx(*testing.T) in tools/archtest/*_test.go.
//
// Failure mode guarded: a maintainer renames (e.g. TestProdDurationConst →
// TestProdDurationConst01) without updating the -run regex in the shell script.
// The script would silently run zero tests for that invariant group and pass.
//
// AI-robust: Medium (archtest-bound AST + shell regex parse; form-uniqueness
// on (script regex set ⊆ AST function-name set)). Cannot be Hard because the
// script is a shell entry point — codegen-ing the list into the script body
// would replace runtime ground truth with a build-time snapshot.
//
// Sibling: ARCHTEST-VERIFY-COVERAGE-01 (archtest_verify_coverage_test.go)
// guards hack/verify-archtest.sh (full suite). This test guards the separate
// PR-time invariants script.
//
// Blind spot: the AST scan does not check that the functions actually implement
// the invariant they claim to — that is the responsibility of the archtest
// authors and their own self-test fixtures.

import (
	"bufio"
	"go/ast"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestArchtestInvariantsCoverage checks that every Test function named in
// hack/verify-archtest-invariants.sh's -run regex exists in the archtest
// top-level package.
func TestArchtestInvariantsCoverage(t *testing.T) {
	t.Parallel()
	repoRoot := findModuleRoot(t)

	scriptFuncs := parseInvariantsScriptFuncs(t, repoRoot)
	astFuncs := scanArchtestTopLevelTestNames(t, repoRoot)

	var missing []string
	for name := range scriptFuncs {
		if _, ok := astFuncs[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("ARCHTEST-INVARIANTS-COVERAGE-01: hack/verify-archtest-invariants.sh "+
			"-run regex references Test functions that do not exist in tools/archtest/*_test.go.\n"+
			"Either rename the test function back or update the -run regex in the script.\n"+
			"Missing:\n  %s", strings.Join(missing, "\n  "))
	}
}

// parseInvariantsScriptFuncs reads hack/verify-archtest-invariants.sh and
// extracts the pipe-separated function names from the -run '^(...)$' argument.
//
// It looks for a line of the form:
//
//	-run '^(Foo|Bar|Baz)$' \
//
// and returns {"Foo", "Bar", "Baz"}.
func parseInvariantsScriptFuncs(t *testing.T, repoRoot string) map[string]struct{} {
	t.Helper()
	scriptPath := filepath.Join(repoRoot, "hack", "verify-archtest-invariants.sh")
	f, err := os.Open(scriptPath) //nolint:gosec // path is findModuleRoot-derived, not user input
	if err != nil {
		t.Fatalf("ARCHTEST-INVARIANTS-COVERAGE-01: cannot open %s: %v", scriptPath, err)
	}
	defer func() {
		if err := f.Close(); err != nil {
			t.Errorf("ARCHTEST-INVARIANTS-COVERAGE-01: closing %s: %v", scriptPath, err)
		}
	}()

	// Match: -run '^(TestFoo|TestBar|...)$'
	// The names may span across a line continuation — we read the full file
	// and match on the concatenated content after stripping line continuations.
	var sb strings.Builder
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), " \\")
		sb.WriteString(line)
		sb.WriteString(" ")
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("ARCHTEST-INVARIANTS-COVERAGE-01: reading %s: %v", scriptPath, err)
	}

	content := sb.String()
	// Extract the alternation group inside -run '^(...)$'
	runRe := regexp.MustCompile(`-run\s+'\^\(([^)]+)\)\$'`)
	m := runRe.FindStringSubmatch(content)
	if m == nil {
		t.Fatalf("ARCHTEST-INVARIANTS-COVERAGE-01: could not find -run '^(...)$' pattern in %s", scriptPath)
	}

	names := map[string]struct{}{}
	for _, name := range strings.Split(m[1], "|") {
		name = strings.TrimSpace(name)
		if name != "" {
			names[name] = struct{}{}
		}
	}
	if len(names) == 0 {
		t.Fatalf("ARCHTEST-INVARIANTS-COVERAGE-01: -run regex in %s parsed to empty set", scriptPath)
	}
	return names
}

// TestArchtestInvariantsCoverage_ReverseBlindSpot verifies that the script
// parser does not silently accept a missing -run line (e.g., if the script is
// refactored to use a different flag name). If the script had no -run flag,
// parseInvariantsScriptFuncs would return an empty set and the coverage check
// would trivially pass — this self-test ensures the parser rejects that case.
func TestArchtestInvariantsCoverage_ReverseBlindSpot(t *testing.T) {
	t.Parallel()

	// A script fragment with no -run flag should produce zero names.
	noRunContent := "go test ./tools/archtest -count=1 -timeout 5m"
	runRe := regexp.MustCompile(`-run\s+'\^\(([^)]+)\)\$'`)
	if runRe.FindStringSubmatch(noRunContent) != nil {
		t.Fatal("ARCHTEST-INVARIANTS-COVERAGE-01 self-test: parser matched unexpected content")
	}

	// A well-formed fragment must match.
	okContent := "-run '^(TestFoo|TestBar)$'"
	m := runRe.FindStringSubmatch(okContent)
	if m == nil {
		t.Fatal("ARCHTEST-INVARIANTS-COVERAGE-01 self-test: parser failed to match well-formed content")
	}
	names := strings.Split(m[1], "|")
	if len(names) != 2 || names[0] != "TestFoo" || names[1] != "TestBar" {
		t.Fatalf("ARCHTEST-INVARIANTS-COVERAGE-01 self-test: unexpected parsed names: %v", names)
	}

	// Sanity: the AST scanner must not return non-Test* names. Build a tiny
	// fake FuncDecl and verify isStandardTestSignature rejects non-*testing.T
	// signatures.
	noParams := &ast.FuncType{Params: &ast.FieldList{List: nil}}
	if isStandardTestSignature(noParams) {
		t.Fatal("ARCHTEST-INVARIANTS-COVERAGE-01 self-test: isStandardTestSignature accepted nil-params func")
	}
}
