package archtest

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// updateGolden, when set via `go test ./tools/archtest/... -update`, makes
// [AssertGolden] (re)write golden files instead of asserting against them.
// Default false: CI (hack/verify-archtest.sh) always asserts, never updates.
//
// archtest is a test-only helper library (every entry point takes
// *testing.T / testing.TB), so this package-scope flag is only ever
// registered into archtest test binaries — there is no production binary
// that imports archtest.
var updateGolden = flag.Bool("update", false,
	"regenerate archtest golden files instead of asserting against them")

// AssertGolden canonicalizes diags and compares the rule's observed diagnostic
// set against the golden file at goldenPath.
//
// Canonicalization reuses [scanner.Canonical] — the single source of
// diagnostic ordering shared with [Report] — so the golden file and a Report
// failure always present the same set in the same order. Each diagnostic is
// rendered as one line "<Rel>:<Line>: <Message>\n"; an empty diagnostic set
// produces an empty golden file (the GREEN-fixture representation).
//
// Why golden (not hardcoded wantLines / typed markers): the expected
// diagnostic set is *derived and regenerated* from the rule's real output,
// never hand-maintained. Adding an import or reformatting a fixture shifts
// line numbers; `-update` rewrites the golden and the reviewer sees a clean
// positional delta with unchanged diagnostic semantics, instead of a
// false-positive test failure on a stale `wantLines []int{534}`. Position
// AND message are bound, so a "miss one violation + over-report an adjacent
// line" regression no longer cancels out under a count-only assertion. See
// docs/architecture/202605181200-adr-archtest-fixture-diagnostic-golden.md.
//
// Golden files are regenerate-only: never hand-edit a *.golden to make CI
// pass — fix the rule or the fixture and re-run with -update. The golden
// diff is a first-class review artifact (same status as a fixture diff under
// .claude/rules/gocell/contract-fanout.md). The regenerate-only discipline
// is currently a review convention (Soft); the backlog entry
// FIXTURE-DIAGNOSTIC-GOLDEN-MIGRATION in docs/backlog/cap-14-tooling.md §14.1
// tracks the optional Medium→Hard meta-archtest upgrade and the scope B/C
// migration of the remaining wantLines-style fixture tests.
func AssertGolden(t testing.TB, goldenPath string, diags []Diagnostic) {
	t.Helper()

	var b strings.Builder
	for _, d := range scanner.Canonical(diags) {
		fmt.Fprintf(&b, "%s:%d: %s\n", d.Rel, d.Line, d.Message)
	}
	got := b.String()

	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o750); err != nil {
			t.Fatalf("archtest.AssertGolden: mkdir %s: %v", filepath.Dir(goldenPath), err)
		}
		if err := os.WriteFile(goldenPath, []byte(got), 0o600); err != nil {
			t.Fatalf("archtest.AssertGolden: write %s: %v", goldenPath, err)
		}
		return
	}

	//nolint:gosec // G304 goldenPath is test-derived (module root + fixed path segments), never user input
	wantBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("archtest.AssertGolden: read golden %s: %v\n"+
			"(hint: run `go test ./tools/archtest/... -run '%s' -update` to generate it, then review the golden diff)",
			goldenPath, err, t.Name())
	}
	if string(wantBytes) != got {
		t.Errorf("archtest.AssertGolden: diagnostic set mismatch for %s\n"+
			"--- want (golden) ---\n%s--- got (rule output) ---\n%s--- end ---\n"+
			"(hint: if intended, run `go test ./tools/archtest/... -run '%s' -update` and review the golden diff as a first-class review artifact)",
			goldenPath, string(wantBytes), got, t.Name())
	}
}
