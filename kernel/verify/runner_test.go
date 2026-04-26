package verify

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
)

func TestParseSliceKey(t *testing.T) {
	tests := []struct {
		key     string
		wantC   string
		wantS   string
		wantErr bool
	}{
		{"accesscore/session-login", "accesscore", "session-login", false},
		{"a/b", "a", "b", false},
		{"noslash", "", "", true},
		{"/leading", "", "", true},
		{"trailing/", "", "", true},
		{"../evil/s", "", "", true},
		{`c\s`, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			c, s, err := parseSliceKey(tt.key)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantC, c)
			assert.Equal(t, tt.wantS, s)
		})
	}
}

func TestVerifySlice_NotFound(t *testing.T) {
	r := NewRunner(&metadata.ProjectMeta{
		Slices: map[string]*metadata.SliceMeta{},
	}, t.TempDir())
	_, err := r.VerifySlice(context.Background(), "cell/missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestVerifyCell_NotFound(t *testing.T) {
	r := NewRunner(&metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{},
	}, t.TempDir())
	_, err := r.VerifyCell(context.Background(), "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRunJourney_NotFound(t *testing.T) {
	r := NewRunner(&metadata.ProjectMeta{
		Journeys: map[string]*metadata.JourneyMeta{},
	}, t.TempDir())
	_, err := r.RunJourney(context.Background(), "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestRunJourney_ManualPending(t *testing.T) {
	r := NewRunner(&metadata.ProjectMeta{
		Journeys: map[string]*metadata.JourneyMeta{
			"J-test": {
				ID: "J-test",
				PassCriteria: []metadata.PassCriterion{
					{Mode: "manual", Text: "Check the UI renders correctly"},
					{Mode: "manual", Text: "Verify email was sent"},
				},
			},
		},
	}, t.TempDir())

	result, err := r.RunJourney(context.Background(), "J-test")
	require.NoError(t, err)
	assert.True(t, result.Passed, "manual-only should pass with no auto criteria")
	assert.Equal(t, []string{
		"Check the UI renders correctly",
		"Verify email was sent",
	}, result.ManualPending)
	// R3-6: assert warning TestResult exists
	require.Len(t, result.Results, 1)
	assert.Contains(t, result.Results[0].Output, "warning")
}

func TestRunJourney_AutoNoCheckRef(t *testing.T) {
	r := NewRunner(&metadata.ProjectMeta{
		Journeys: map[string]*metadata.JourneyMeta{
			"J-test": {
				ID: "J-test",
				PassCriteria: []metadata.PassCriterion{
					{Mode: "auto", Text: "Unverifiable criterion", CheckRef: ""},
				},
			},
		},
	}, t.TempDir())

	result, err := r.RunJourney(context.Background(), "J-test")
	require.NoError(t, err)
	assert.False(t, result.Passed, "auto without checkRef should fail")
	require.Len(t, result.Results, 1)
	assert.Contains(t, result.Results[0].Output, "no checkRef")
}

func TestRunJourney_InvalidRef(t *testing.T) {
	r := NewRunner(&metadata.ProjectMeta{
		Journeys: map[string]*metadata.JourneyMeta{
			"J-test": {
				ID: "J-test",
				PassCriteria: []metadata.PassCriterion{
					{Mode: "auto", CheckRef: "bad-ref"},
				},
			},
		},
	}, t.TempDir())

	result, err := r.RunJourney(context.Background(), "J-test")
	require.NoError(t, err)
	assert.False(t, result.Passed, "invalid ref should fail")
	require.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0].Error(), "ERR_CHECKREF_INVALID")
}

func TestRunActiveJourneys_ManualOnlyActiveFails(t *testing.T) {
	r := NewRunner(&metadata.ProjectMeta{
		Journeys: map[string]*metadata.JourneyMeta{
			"J-test": {
				ID:        "J-test",
				Lifecycle: "active",
				PassCriteria: []metadata.PassCriterion{
					{Mode: "manual", Text: "Security signoff"},
				},
			},
		},
	}, t.TempDir())

	result, err := r.RunActiveJourneys(context.Background())
	require.NoError(t, err)
	assert.False(t, result.Passed, "active journeys need at least one auto checkRef")
	require.NotEmpty(t, result.Results)
	assert.Contains(t, result.Results[len(result.Results)-1].Output, "active journey has no auto checkRef")
}

func TestRunActiveJourneys_NilProjectPasses(t *testing.T) {
	r := NewRunner(nil, t.TempDir())

	result, err := r.RunActiveJourneys(context.Background())
	require.NoError(t, err)
	assert.True(t, result.Passed)
	assert.Empty(t, result.Results)
}

func TestRunActiveJourneys_SkipsInactiveJourneys(t *testing.T) {
	r := NewRunner(&metadata.ProjectMeta{
		Journeys: map[string]*metadata.JourneyMeta{
			"J-draft": {
				ID:        "J-draft",
				Lifecycle: "experimental",
				PassCriteria: []metadata.PassCriterion{
					{Mode: "manual", Text: "Explore manually"},
				},
			},
		},
	}, t.TempDir())

	result, err := r.RunActiveJourneys(context.Background())
	require.NoError(t, err)
	assert.True(t, result.Passed)
	assert.Empty(t, result.Results)
}

func TestRunActiveJourneys_AutoCheckRefPasses(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.21\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "journeys"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "journeys", "journey_test.go"), []byte(`package journeys
import "testing"
func TestJActiveHappyPath(t *testing.T) {}
`), 0o644))

	r := NewRunner(&metadata.ProjectMeta{
		Journeys: map[string]*metadata.JourneyMeta{
			"J-active": {
				ID:        "J-active",
				Lifecycle: "active",
				PassCriteria: []metadata.PassCriterion{
					{Mode: "auto", Text: "Happy path", CheckRef: "journey.J-active.happy-path"},
				},
			},
		},
	}, dir)

	result, err := r.RunActiveJourneys(context.Background())
	require.NoError(t, err)
	assert.True(t, result.Passed)
	require.Len(t, result.Results, 1)
	assert.Equal(t, "journey.J-active.happy-path", result.Results[0].Name)
}

func TestRunJourneyCheckRef_RejectsMismatchedJourneyScope(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.21\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "journeys"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "journeys", "journey_test.go"), []byte(`package journeys
import "testing"
func TestJOtherHappyPath(t *testing.T) {}
`), 0o644))

	r := NewRunner(&metadata.ProjectMeta{}, dir)
	tr, errs := r.RunJourneyCheckRef(
		context.Background(),
		&metadata.JourneyMeta{ID: "J-current"},
		"journey.J-other.happy-path",
	)

	assert.False(t, tr.Passed, "a journey must not borrow another journey's passing test")
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), `belongs to journey "J-other"`)
}

func TestRunJourneyCheckRef_RejectsNonJourneyRef(t *testing.T) {
	r := NewRunner(&metadata.ProjectMeta{}, t.TempDir())

	tr, errs := r.RunJourneyCheckRef(
		context.Background(),
		&metadata.JourneyMeta{ID: "J-current"},
		"smoke.accesscore.startup",
	)

	assert.False(t, tr.Passed)
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "must use journey prefix")
}

func TestResolveJourneyPkg_IntegrationDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tests", "integration"), 0o755))
	r := NewRunner(nil, dir)
	pkg, extra := r.resolveJourneyPkg(&metadata.JourneyMeta{}, resolvedRef{Kind: PrefixJourney})
	assert.Equal(t, "./tests/integration/...", pkg)
	assert.Contains(t, extra, "-tags=integration")
}

func TestResolveJourneyPkg_ExampleJourneyPrefersExamplePackage(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "examples", "todoorder"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tests", "integration"), 0o755))

	r := NewRunner(nil, dir)
	pkg, extra := r.resolveJourneyPkg(&metadata.JourneyMeta{
		File: "examples/todoorder/journeys/J-ordercreate.yaml",
	}, resolvedRef{Kind: PrefixJourney})

	assert.Equal(t, "./examples/todoorder/...", pkg)
	assert.Nil(t, extra)
}

func TestResolveJourneyPkg_JourneysDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "journeys"), 0o755))
	r := NewRunner(nil, dir)
	pkg, extra := r.resolveJourneyPkg(&metadata.JourneyMeta{}, resolvedRef{Kind: PrefixJourney})
	assert.Equal(t, "./journeys/...", pkg)
	assert.Nil(t, extra)
}

func TestResolveJourneyPkg_Fallback(t *testing.T) {
	r := NewRunner(nil, t.TempDir())
	pkg, extra := r.resolveJourneyPkg(&metadata.JourneyMeta{}, resolvedRef{Kind: PrefixJourney})
	assert.Equal(t, "./...", pkg)
	assert.Nil(t, extra)
}

func TestExampleNameFromJourneyFile(t *testing.T) {
	name, ok := exampleNameFromJourneyFile("examples/todoorder/journeys/J-ordercreate.yaml")
	require.True(t, ok)
	assert.Equal(t, "todoorder", name)

	_, ok = exampleNameFromJourneyFile("journeys/J-ordercreate.yaml")
	assert.False(t, ok)
}

func TestResolveSlicePkg_PrefersGoFiles(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "cells", "c", "slices")
	// Create metadata dir with only YAML
	yamlDir := filepath.Join(base, "my-slice")
	require.NoError(t, os.MkdirAll(yamlDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(yamlDir, "slice.yaml"), []byte("id: my-slice"), 0o644))
	// Create Go package dir
	goDir := filepath.Join(base, "myslice")
	require.NoError(t, os.MkdirAll(goDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(goDir, "service.go"), []byte("package myslice"), 0o644))

	pkg := resolveSlicePkg(dir, "c", "my-slice")
	assert.Contains(t, pkg, "myslice", "should prefer dir with Go files")
	assert.NotContains(t, pkg, "my-slice")
}

func TestResolveSlicePkg_FallbackToMetadata(t *testing.T) {
	pkg := resolveSlicePkg(t.TempDir(), "c", "nonexistent")
	assert.Contains(t, pkg, "nonexistent")
}

func TestIsZeroMatch(t *testing.T) {
	assert.True(t, isZeroMatch("testing: warning: no tests to run\nPASS"))
	assert.True(t, isZeroMatch("?   \tpkg\t[no test files]"))
	assert.False(t, isZeroMatch("--- PASS: TestFoo (0.00s)\nPASS"))
	assert.False(t, isZeroMatch("testing: warning: no tests to run\n--- SKIP: TestFoo (0.00s)\nPASS"))
	assert.False(t, isZeroMatch(""))
}

func TestIsSkipOnly(t *testing.T) {
	assert.True(t, isSkipOnly("=== RUN   TestFoo\n--- SKIP: TestFoo (0.00s)\nPASS"))
	assert.False(t, isSkipOnly("=== RUN   TestFoo\n--- PASS: TestFoo (0.00s)\nPASS"))
	assert.False(t, isSkipOnly(""))
}

func TestVerifyCell_NoSmoke(t *testing.T) {
	r := NewRunner(&metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"demo": {ID: "demo", Verify: metadata.CellVerifyMeta{}},
		},
	}, t.TempDir())

	result, err := r.VerifyCell(context.Background(), "demo")
	require.NoError(t, err)
	assert.True(t, result.Passed, "no smoke refs = warning but pass")
	require.Len(t, result.Results, 1, "should have a warning TestResult")
	assert.Contains(t, result.Results[0].Output, "warning")
}

func TestRunRefs_AllInvalid(t *testing.T) {
	r := NewRunner(&metadata.ProjectMeta{
		Slices: map[string]*metadata.SliceMeta{
			"c/s": {
				ID:            "s",
				BelongsToCell: "c",
				Verify: metadata.SliceVerifyMeta{
					Unit: []string{"bad-ref", "also-bad"},
				},
			},
		},
	}, t.TempDir())

	result, err := r.VerifySlice(context.Background(), "c/s")
	require.NoError(t, err)
	assert.False(t, result.Passed, "all invalid refs should fail")
	assert.Len(t, result.Errors, 2, "two invalid refs = two errors")
}

func TestRecordResult_ZeroMatchMessage(t *testing.T) {
	result := &VerifyResult{TargetID: "test", Passed: true}
	res := goTestResult{Output: "testing: warning: no tests to run\nPASS", Passed: true, ZeroMatch: true}
	recordResult(result, "ref", res, "./pkg/...", "SomePattern")
	assert.False(t, result.Passed)
	require.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0].Error(), "check your YAML ref")
}

func TestRecordResult_SkipOnlyFails(t *testing.T) {
	result := &VerifyResult{TargetID: "test", Passed: true}
	res := goTestResult{Output: "--- SKIP: TestFoo (0.00s)\nPASS", Passed: true, SkippedOnly: true}
	recordResult(result, "ref", res, "./pkg/...", "^TestFoo$")
	assert.False(t, result.Passed)
	require.Len(t, result.Errors, 1)
	assert.Contains(t, result.Errors[0].Error(), "only skipped tests")
}
