//go:build archtest

package archtest

// invariants:
//   - INVARIANT: GOLDEN-HELPER-UNIT-01
//
// golden_test.go — the [AssertGolden] regenerate-or-assert harness for GoCell's
// OWN golden-fixture archtests, plus unit coverage for it.
//
// The harness lives in a _test.go file (not a plain .go file) on purpose: the
// `-update` flag below is a package-scope flag.Bool side-effect, and Go never
// compiles a dependency's _test.go files into a dependent's build — test binary
// OR not. So an external Cell repo that imports tools/archtest can never have
// this `-update` flag registered into its own test binary, eliminating the
// flag-redefinition panic a package-level flag would cause if two importers both
// registered "-update". External consumers run rules via external.go's
// RunStandardCellRules and never touch AssertGolden (they have no GoCell goldens).
//
// Blind spot of the unit tests below (declared per .claude/rules/gocell/ai-robust.md
// §"工具选定后强制盲区自检"): they exercise writeOrAssertGolden directly with an
// explicit update bool, NOT via a real *testing.T with flag parsing, so they do
// not prove `-update` interacts correctly with `go test` flag parsing — a
// declared blind spot; local manual verification:
// `go test -tags=archtest ./tools/archtest -run TestPanicRegisteredScannerFixtures -update`.

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// updateGolden, when set via `go test -tags=archtest ./tools/archtest -update`, makes
// [AssertGolden] (re)write golden files instead of asserting against them.
// Default false: CI (hack/verify-archtest.sh) always asserts, never updates;
// golden mismatch surfaces as a t.Errorf test failure, not as git diff --exit-code.
// Registered only in archtest's own test binary (this is a _test.go file).
var updateGolden = flag.Bool("update", false,
	"regenerate archtest golden files instead of asserting against them")

// AssertGolden canonicalizes diags and compares the rule's observed diagnostic
// set against the golden file at goldenPath.
//
// goldenPath must be an absolute path derived from the module root or
// t.TempDir() (e.g. testdata/<fixture>/diag.golden under the repo root).
// Relative paths and paths outside the repository are not supported.
//
// Canonicalization reuses [scanner.Canonical] — the single source of
// diagnostic ordering shared with [Report] — so the golden file and a Report
// failure always present the same set in the same order. Each diagnostic is
// rendered as one line "<Rel>:<Line>: <Message>\n"; an empty diagnostic set
// produces an empty golden file (the GREEN-fixture representation).
//
// Golden files are regenerate-only: never hand-edit a *.golden to make CI
// pass — fix the rule or the fixture and re-run with -update. The golden
// diff is a first-class review artifact (same status as a fixture diff under
// .claude/rules/gocell/contract-fanout.md). The regenerate-only discipline
// is currently a review convention (Soft). NOTE: this does NOT govern
// ARCHTEST-MODULE-PATH-FUNNEL-01's frozen baseline, which is a shrink-only
// ceiling never touched by -update — see module_path_funnel_test.go.
func AssertGolden(t testing.TB, goldenPath string, diags []Diagnostic) {
	t.Helper()
	writeOrAssertGolden(t, goldenPath, diags, *updateGolden)
}

// writeOrAssertGolden is the testable core of [AssertGolden]. Callers pass
// update explicitly so tests can exercise the update path without touching the
// package-global updateGolden flag (which would cause data races with
// t.Parallel tests that concurrently read *updateGolden).
func writeOrAssertGolden(t testing.TB, goldenPath string, diags []Diagnostic, update bool) {
	t.Helper()

	var b strings.Builder
	for _, d := range scanner.Canonical(diags) {
		fmt.Fprintf(&b, "%s:%d: %s\n", d.Rel, d.Line, d.Message)
	}
	got := b.String()

	if update {
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
			"(hint: run `go test -tags=archtest ./tools/archtest -run '%s' -update` to generate it, then review the golden diff)",
			goldenPath, err, t.Name())
	}
	if string(wantBytes) != got {
		t.Errorf("archtest.AssertGolden: diagnostic set mismatch for %s\n"+
			"--- want (golden) ---\n%s--- got (rule output) ---\n%s--- end ---\n"+
			"(hint: if intended, run `go test -tags=archtest ./tools/archtest -run '%s' -update` "+
			"and review the golden diff as a first-class review artifact)",
			goldenPath, string(wantBytes), got, t.Name())
	}
}

// recorderTB captures Fatalf/Errorf instead of failing the outer test. It
// embeds testing.TB to satisfy the sealed interface (testing.TB has an
// unexported method; external types can only satisfy it via embedding).
//
// AssertGolden invokes t.Fatalf strictly followed by an explicit `return`
// at every call site, so this recorder deliberately does NOT emulate
// FailNow/runtime.Goexit semantics — there is no code path in AssertGolden
// that continues executing after Fatalf. This is asserted by
// TestAssertGolden_MissingGolden (the recorder observes exactly one Fatalf
// and AssertGolden returns normally).
type recorderTB struct {
	testing.TB
	fatals []string
	errors []string
}

func (r *recorderTB) Helper()                   {}
func (r *recorderTB) Name() string              { return "recorderTB" }
func (r *recorderTB) Fatalf(f string, a ...any) { r.fatals = append(r.fatals, fmt.Sprintf(f, a...)) }

func (r *recorderTB) Errorf(f string, a ...any) { r.errors = append(r.errors, fmt.Sprintf(f, a...)) }

func TestCanonicalDedupAndSort(t *testing.T) {
	t.Parallel()

	in := []scanner.Diagnostic{
		{Rel: "b.go", Line: 2, Message: "m2"},
		{Rel: "a.go", Line: 9, Message: "z"},
		{Rel: "a.go", Line: 9, Message: "z"}, // dup
		{Rel: "a.go", Line: 1, Message: "a"},
	}
	got := scanner.Canonical(in)

	want := []scanner.Diagnostic{
		{Rel: "a.go", Line: 1, Message: "a"},
		{Rel: "a.go", Line: 9, Message: "z"},
		{Rel: "b.go", Line: 2, Message: "m2"},
	}
	assert.Equal(t, want, got, "dedup + (Rel,Line,Message) sort")
	assert.Nil(t, scanner.Canonical(nil), "nil input -> nil")
	assert.Nil(t, scanner.Canonical([]scanner.Diagnostic{}), "empty input -> nil")
}

func TestAssertGolden_Match(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gp := filepath.Join(dir, "diag.golden")
	require.NoError(t, os.WriteFile(gp,
		[]byte("a.go:3: boom\n"), 0o644))

	rec := &recorderTB{}
	AssertGolden(rec, gp, []Diagnostic{{Rel: "a.go", Line: 3, Message: "boom"}})

	assert.Empty(t, rec.fatals, "matching golden -> no fatal")
	assert.Empty(t, rec.errors, "matching golden -> no error")
}

func TestAssertGolden_GreenEmpty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gp := filepath.Join(dir, "diag.golden")
	require.NoError(t, os.WriteFile(gp, []byte(""), 0o644))

	rec := &recorderTB{}
	AssertGolden(rec, gp, nil) // GREEN fixture: zero diagnostics

	assert.Empty(t, rec.fatals)
	assert.Empty(t, rec.errors, "empty golden + zero diags -> GREEN")
}

func TestAssertGolden_Mismatch(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gp := filepath.Join(dir, "diag.golden")
	require.NoError(t, os.WriteFile(gp, []byte("a.go:3: boom\n"), 0o644))

	rec := &recorderTB{}
	// Same count, different line — count-only would have passed; golden must
	// flag the positional drift (the coverage PR #557 cardinality-only lost).
	AssertGolden(rec, gp, []Diagnostic{{Rel: "a.go", Line: 4, Message: "boom"}})

	assert.Empty(t, rec.fatals)
	require.Len(t, rec.errors, 1, "mismatch -> exactly one Errorf")
	assert.Contains(t, rec.errors[0], "a.go:3: boom", "shows want")
	assert.Contains(t, rec.errors[0], "a.go:4: boom", "shows got")
}

func TestAssertGolden_MissingGolden(t *testing.T) {
	t.Parallel()

	rec := &recorderTB{}
	AssertGolden(rec, filepath.Join(t.TempDir(), "nope.golden"),
		[]Diagnostic{{Rel: "a.go", Line: 1, Message: "x"}})

	require.Len(t, rec.fatals, 1, "missing golden -> exactly one Fatalf, then return")
	assert.Contains(t, rec.fatals[0], "-update", "hint mentions -update regeneration")
}

func TestAssertGolden_UpdateWritesFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	gp := filepath.Join(dir, "sub", "diag.golden")

	rec := &recorderTB{}
	// Call writeOrAssertGolden directly with update=true to avoid touching the
	// package-global *updateGolden flag, which would race with parallel tests.
	writeOrAssertGolden(rec, gp, []Diagnostic{{Rel: "z.go", Line: 7, Message: "w"}}, true)

	require.Empty(t, rec.fatals)
	require.Empty(t, rec.errors)
	b, err := os.ReadFile(gp) //nolint:gosec // G304 gp is t.TempDir()-derived test path, not user input
	require.NoError(t, err, "update creates nested dir + file")
	assert.Equal(t, "z.go:7: w\n", string(b))
}
