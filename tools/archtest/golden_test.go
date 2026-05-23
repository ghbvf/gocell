package archtest

// invariants:
//   - INVARIANT: GOLDEN-HELPER-UNIT-01
//
// golden_test.go — unit coverage for the AssertGolden harness and the
// scanner.Canonical single-source ordering it shares with Report. The blind
// spot of these tests (declared per .claude/rules/gocell/ai-robust.md
// §"工具选定后强制盲区自检"): they exercise writeOrAssertGolden directly with
// an explicit update bool, NOT via a real *testing.T with flag parsing, so they
// do not prove `-update` interacts correctly with `go test` flag parsing — that
// path is a declared blind spot; local manual verification:
// `go test ./tools/archtest/... -run TestPanicRegisteredScannerFixtures -update`.

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

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
