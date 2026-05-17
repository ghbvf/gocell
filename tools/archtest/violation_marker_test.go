// INVARIANT: VIOLATION-MARKER-HELPER-UNIT-01
//
// violation_marker_test.go — unit tests for archtest.CountViolationMarkers
// and archtest.AssertDiagnosticCount. These fix the contract that fixture
// tests rely on via the FIXTURESPEC-COUNT-MATCH-ENFORCED-01 funnel rule.
//
// Wave 1 (RED): stubs in violation_marker.go return -1 / Fatalf; tests below
// fail. Wave 2 (GREEN): real impl, tests pass.
package archtest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestCountViolationMarkers_ZeroMarkers loads the meta-fixture
// testdata/violation_marker_meta_fixtures/zero_markers/ which has no
// spec.Violation() calls. CountViolationMarkers must return 0; the same
// fixture is also asserted via AssertDiagnosticCount with an empty got
// slice — naturally satisfying the FIXTURESPEC-COUNT-MATCH-ENFORCED-01
// upstream funnel rule.
func TestCountViolationMarkers_ZeroMarkers(t *testing.T) {
	t.Parallel()

	pattern := "./tools/archtest/testdata/violation_marker_meta_fixtures/zero_markers"
	_ = RunTyped(t, TypedOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		got := CountViolationMarkers(p)
		assert.Equal(t, 0, got,
			"zero_markers fixture must yield 0 spec.Violation callees, got %d", got)
		AssertDiagnosticCount(t, "TEST-ZERO-MARKERS-01", p, nil)
		return nil
	})
}

// TestCountViolationMarkers_TwoMarkers loads the meta-fixture
// testdata/violation_marker_meta_fixtures/two_markers/ which contains exactly
// two spec.Violation() calls. CountViolationMarkers must return 2; the same
// fixture is also asserted via AssertDiagnosticCount with two synthetic
// diagnostics — naturally satisfying the upstream funnel rule.
func TestCountViolationMarkers_TwoMarkers(t *testing.T) {
	t.Parallel()

	pattern := "./tools/archtest/testdata/violation_marker_meta_fixtures/two_markers"
	_ = RunTyped(t, TypedOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		got := CountViolationMarkers(p)
		assert.Equal(t, 2, got,
			"two_markers fixture must yield 2 spec.Violation callees, got %d", got)
		AssertDiagnosticCount(t, "TEST-TWO-MARKERS-01", p, []Diagnostic{
			{Rel: "two_markers/usage.go", Line: 7, Message: "synthetic1"},
			{Rel: "two_markers/usage.go", Line: 11, Message: "synthetic2"},
		})
		return nil
	})
}

// TestCountViolationMarkers_NilPass returns 0 for a nil Pass (defensive).
func TestCountViolationMarkers_NilPass(t *testing.T) {
	t.Parallel()
	got := CountViolationMarkers(nil)
	assert.Equal(t, 0, got, "nil Pass must yield count 0, got %d", got)
}

// TestCountViolationMarkers_ASTOnlyPass returns 0 for a Pass without
// TypesInfo (callee resolution requires types).
func TestCountViolationMarkers_ASTOnlyPass(t *testing.T) {
	t.Parallel()
	got := CountViolationMarkers(&Pass{}) // no TypesInfo
	assert.Equal(t, 0, got,
		"AST-only Pass (no TypesInfo) must yield count 0, got %d", got)
}

// TestAssertDiagnosticCount_Match asserts the helper passes silently when
// len(got) equals the marker count for the loaded fixture.
func TestAssertDiagnosticCount_Match(t *testing.T) {
	t.Parallel()

	pattern := "./tools/archtest/testdata/violation_marker_meta_fixtures/two_markers"

	// Synthesize a "got" slice of length 2 to match the 2 markers.
	synthetic := []Diagnostic{
		{Rel: "two_markers/usage.go", Line: 7, Message: "synthetic1"},
		{Rel: "two_markers/usage.go", Line: 11, Message: "synthetic2"},
	}

	_ = RunTyped(t, TypedOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		probe := &countTestProbe{TB: t}
		AssertDiagnosticCount(probe, "TEST-MATCH-01", p, synthetic)
		assert.False(t, probe.failed,
			"AssertDiagnosticCount must not fail when len(got)==markerCount; reasons=%v",
			probe.messages)
		return nil
	})
}

// TestAssertDiagnosticCount_Mismatch asserts the helper reports a mismatch
// when len(got) differs from the marker count.
func TestAssertDiagnosticCount_Mismatch(t *testing.T) {
	t.Parallel()

	pattern := "./tools/archtest/testdata/violation_marker_meta_fixtures/two_markers"

	// Wrong length on purpose — only 1 diag where 2 markers exist.
	synthetic := []Diagnostic{
		{Rel: "two_markers/usage.go", Line: 7, Message: "synthetic1"},
	}

	_ = RunTyped(t, TypedOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		probe := &countTestProbe{TB: t}
		AssertDiagnosticCount(probe, "TEST-MISMATCH-01", p, synthetic)
		assert.True(t, probe.failed,
			"AssertDiagnosticCount must fail when len(got)!=markerCount")
		joined := strings.Join(probe.messages, " ")
		assert.Contains(t, joined, "TEST-MISMATCH-01",
			"failure message must include ruleID")
		assert.Contains(t, joined, "got 1",
			"failure message must include actual got count")
		assert.Contains(t, joined, "want 2",
			"failure message must include expected marker count")
		return nil
	})
}

// countTestProbe is a testing.TB stub that records Errorf/Fatalf calls
// instead of failing the enclosing test. Used by TestAssertDiagnosticCount_*
// to assert the helper's failure-reporting contract without panicking out
// of the surrounding *testing.T.
type countTestProbe struct {
	testing.TB
	failed   bool
	messages []string
}

func (p *countTestProbe) Errorf(format string, args ...any) {
	p.failed = true
	p.messages = append(p.messages, fmt.Sprintf(format, args...))
}

func (p *countTestProbe) Fatalf(format string, args ...any) {
	p.failed = true
	p.messages = append(p.messages, fmt.Sprintf(format, args...))
}

func (p *countTestProbe) Fatal(args ...any) {
	p.failed = true
	p.messages = append(p.messages, fmt.Sprint(args...))
}

func (p *countTestProbe) Error(args ...any) {
	p.failed = true
	p.messages = append(p.messages, fmt.Sprint(args...))
}

func (p *countTestProbe) Helper() {}

// TestViolationMarkerMetaFixture_ImportPathIntact is a smoke check protecting
// the meta-fixture against accidental edits (the count test relies on the
// import being exactly fixturespecViolationPkgPath so callee resolution lands
// on fixturespec.Violation).
func TestViolationMarkerMetaFixture_ImportPathIntact(t *testing.T) {
	t.Parallel()

	pattern := "./tools/archtest/testdata/violation_marker_meta_fixtures/two_markers"
	found := false
	_ = RunTyped(t, TypedOpts{}, []string{pattern}, func(p *Pass) []Diagnostic {
		for _, f := range p.Files {
			for _, imp := range f.Imports {
				if v, ok := StringLitValue(imp.Path); ok && v == fixturespecViolationPkgPath {
					found = true
				}
			}
		}
		return nil
	})
	assert.True(t, found,
		"two_markers/usage.go must import %q", fixturespecViolationPkgPath)
}
