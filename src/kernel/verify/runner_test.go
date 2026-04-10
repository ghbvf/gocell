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
		{"access-core/session-login", "access-core", "session-login", false},
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

func TestCollectAutoCheckRefs(t *testing.T) {
	j := &metadata.JourneyMeta{
		PassCriteria: []metadata.PassCriterion{
			{Mode: "auto", CheckRef: "journey.J-a.test-one"},
			{Mode: "manual", CheckRef: ""},
			{Mode: "auto", CheckRef: ""},
			{Mode: "auto", CheckRef: "journey.J-b.test-two"},
		},
	}
	got := collectAutoCheckRefs(j)
	assert.Equal(t, []string{"journey.J-a.test-one", "journey.J-b.test-two"}, got)
}

func TestCollectAutoCheckRefs_Empty(t *testing.T) {
	j := &metadata.JourneyMeta{
		PassCriteria: []metadata.PassCriterion{
			{Mode: "manual", Text: "manual only"},
		},
	}
	got := collectAutoCheckRefs(j)
	assert.Nil(t, got)
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

func TestResolveJourneyPkg_IntegrationDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "tests", "integration"), 0o755))
	r := NewRunner(nil, dir)
	pkg, extra := r.resolveJourneyPkg(resolvedRef{Kind: PrefixJourney})
	assert.Equal(t, "./tests/integration/...", pkg)
	assert.Contains(t, extra, "-tags=integration")
}

func TestResolveJourneyPkg_JourneysDir(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "journeys"), 0o755))
	r := NewRunner(nil, dir)
	pkg, extra := r.resolveJourneyPkg(resolvedRef{Kind: PrefixJourney})
	assert.Equal(t, "./journeys/...", pkg)
	assert.Nil(t, extra)
}

func TestResolveJourneyPkg_Fallback(t *testing.T) {
	r := NewRunner(nil, t.TempDir())
	pkg, extra := r.resolveJourneyPkg(resolvedRef{Kind: PrefixJourney})
	assert.Equal(t, "./...", pkg)
	assert.Nil(t, extra)
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
	assert.False(t, isZeroMatch(""))
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
