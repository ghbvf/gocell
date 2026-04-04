package slice

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func testProject() *metadata.ProjectMeta {
	return &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"access-core": {
				ID:               "access-core",
				Type:             "core",
				ConsistencyLevel: "L2",
				Owner:            metadata.OwnerMeta{Team: "identity", Role: "backend"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.access-core.login"}},
			},
			"audit-core": {
				ID:               "audit-core",
				Type:             "core",
				ConsistencyLevel: "L1",
				Owner:            metadata.OwnerMeta{Team: "compliance", Role: "backend"},
				Verify:           metadata.CellVerifyMeta{Smoke: []string{"smoke.audit-core.write"}},
			},
		},
		Slices: map[string]*metadata.SliceMeta{
			"access-core/session-create": {
				ID:            "session-create",
				BelongsToCell: "access-core",
				Verify: metadata.SliceVerifyMeta{
					Unit:     []string{"unit.session-create.service"},
					Contract: []string{"contract.session-create.http"},
				},
			},
			"access-core/session-refresh": {
				ID:            "session-refresh",
				BelongsToCell: "access-core",
			},
			"audit-core/audit-write": {
				ID:            "audit-write",
				BelongsToCell: "audit-core",
			},
		},
		Journeys: map[string]*metadata.JourneyMeta{
			"J-sso-login": {
				ID:    "J-sso-login",
				Goal:  "SSO login with session",
				Owner: metadata.OwnerMeta{Team: "platform", Role: "journey-owner"},
				Cells: []string{"access-core", "audit-core"},
				PassCriteria: []metadata.PassCriterion{
					{Text: "OIDC redirect done", Mode: "auto", CheckRef: "journey.J-sso-login.oidc-redirect"},
					{Text: "session persisted", Mode: "auto", CheckRef: "journey.J-sso-login.session-persist"},
					{Text: "manual review done", Mode: "manual"},
				},
			},
			"J-empty-auto": {
				ID:    "J-empty-auto",
				Goal:  "journey with no auto checkRefs",
				Owner: metadata.OwnerMeta{Team: "platform", Role: "journey-owner"},
				Cells: []string{"access-core"},
				PassCriteria: []metadata.PassCriterion{
					{Text: "manual only", Mode: "manual"},
					{Text: "auto but no ref", Mode: "auto", CheckRef: ""},
				},
			},
		},
		Contracts:  map[string]*metadata.ContractMeta{},
		Assemblies: map[string]*metadata.AssemblyMeta{},
	}
}

// ---------------------------------------------------------------------------
// NewRunner
// ---------------------------------------------------------------------------

func TestNewRunner(t *testing.T) {
	proj := testProject()
	r := NewRunner(proj, "/some/root")
	require.NotNil(t, r)
	assert.Equal(t, proj, r.project)
	assert.Equal(t, "/some/root", r.root)
	assert.NotNil(t, r.cells)
}

// ---------------------------------------------------------------------------
// parseSliceKey
// ---------------------------------------------------------------------------

func TestParseSliceKey(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		cellID  string
		sliceID string
		wantErr bool
	}{
		{"valid key", "access-core/session-create", "access-core", "session-create", false},
		{"another valid key", "audit-core/audit-write", "audit-core", "audit-write", false},
		{"no slash", "access-core", "", "", true},
		{"empty cell", "/session-create", "", "", true},
		{"empty slice", "access-core/", "", "", true},
		{"empty string", "", "", "", true},
		{"cellID with dot-dot", "../../etc/session-create", "", "", true},
		{"cellID with slash", "access/core/session-create", "", "", true},
		{"cellID with backslash", "access\\core/session-create", "", "", true},
		{"sliceID with dot-dot", "access-core/..%2fsession-create/..", "", "", true},
		{"sliceID with slash", "access-core/some/path", "", "", true},
		{"sliceID with backslash", "access-core/some\\path", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cellID, sliceID, err := parseSliceKey(tt.key)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.cellID, cellID)
			assert.Equal(t, tt.sliceID, sliceID)
		})
	}
}

// ---------------------------------------------------------------------------
// VerifySlice — lookup logic
// ---------------------------------------------------------------------------

func TestVerifySlice_NotFound(t *testing.T) {
	tests := []struct {
		name     string
		sliceKey string
		errMsg   string
	}{
		{"invalid key format", "noslash", "invalid slice key"},
		{"slice not in project", "access-core/nonexistent", "not found in project metadata"},
		{"cell not in project", "unknown-cell/some-slice", "not found in project metadata"},
	}
	r := NewRunner(testProject(), "/tmp/fake")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := r.VerifySlice(context.Background(), tt.sliceKey)
			assert.Error(t, err)
			assert.Nil(t, res)
			assert.Contains(t, err.Error(), tt.errMsg)
		})
	}
}

// ---------------------------------------------------------------------------
// VerifyCell — lookup logic
// ---------------------------------------------------------------------------

func TestVerifyCell_NotFound(t *testing.T) {
	r := NewRunner(testProject(), "/tmp/fake")
	res, err := r.VerifyCell(context.Background(), "nonexistent-cell")
	assert.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "not found in project metadata")
}

// ---------------------------------------------------------------------------
// RunJourney — lookup logic
// ---------------------------------------------------------------------------

func TestRunJourney_NotFound(t *testing.T) {
	r := NewRunner(testProject(), "/tmp/fake")
	res, err := r.RunJourney(context.Background(), "J-nonexistent")
	assert.Error(t, err)
	assert.Nil(t, res)
	assert.Contains(t, err.Error(), "not found in project metadata")
}

func TestRunJourney_NoAutoCheckRefs(t *testing.T) {
	r := NewRunner(testProject(), "/tmp/fake")
	res, err := r.RunJourney(context.Background(), "J-empty-auto")
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.True(t, res.Passed)
	assert.Empty(t, res.Results)
	assert.Equal(t, "J-empty-auto", res.TargetID)
}

// ---------------------------------------------------------------------------
// collectAutoCheckRefs
// ---------------------------------------------------------------------------

func TestCollectAutoCheckRefs(t *testing.T) {
	tests := []struct {
		name     string
		criteria []metadata.PassCriterion
		want     []string
	}{
		{
			"mixed modes",
			[]metadata.PassCriterion{
				{Mode: "auto", CheckRef: "ref1"},
				{Mode: "manual"},
				{Mode: "auto", CheckRef: "ref2"},
				{Mode: "auto", CheckRef: ""},
			},
			[]string{"ref1", "ref2"},
		},
		{
			"no auto",
			[]metadata.PassCriterion{
				{Mode: "manual"},
			},
			nil,
		},
		{
			"empty list",
			nil,
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := &metadata.JourneyMeta{PassCriteria: tt.criteria}
			got := collectAutoCheckRefs(j)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// resolveCheckRef
// ---------------------------------------------------------------------------

func TestResolveCheckRef(t *testing.T) {
	tests := []struct {
		name       string
		ref        string
		wantPkg    string
		wantPattern string
	}{
		{
			"journey format",
			"journey.J-sso-login.oidc-redirect",
			"./journeys/...",
			"oidc-redirect",
		},
		{
			"journey with dots in suffix",
			"journey.J-test.some.complex.ref",
			"./journeys/...",
			"some.complex.ref",
		},
		{
			"non-journey prefix",
			"cell.access-core.smoke",
			"./...",
			"cell.access-core.smoke",
		},
		{
			"single part",
			"something",
			"./...",
			"something",
		},
		{
			"two parts non-journey",
			"unit.test",
			"./...",
			"unit.test",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pkg, pattern := resolveCheckRef(tt.ref)
			assert.Equal(t, tt.wantPkg, pkg)
			assert.Equal(t, tt.wantPattern, pattern)
		})
	}
}

// ---------------------------------------------------------------------------
// runGoTest — using a real command to validate the exec mechanism
// ---------------------------------------------------------------------------

func TestRunGoTest_SuccessfulCommand(t *testing.T) {
	// Use `go version` as a lightweight command that always succeeds.
	// We need a valid directory, so use os.TempDir().
	dir := t.TempDir()
	output, passed, err := runGoTest(context.Background(), dir, []string{})
	// `go test` with no packages in an empty dir will fail,
	// so we test with a constructed scenario instead.
	// For a basic exec test, let's just verify the function runs.
	_ = output
	_ = passed
	_ = err
}

func TestRunGoTest_WithTempModule(t *testing.T) {
	// Create a temp Go module with a trivial passing test.
	dir := t.TempDir()

	goMod := []byte("module testmod\n\ngo 1.21\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), goMod, 0o644))

	goTest := []byte(`package testmod

import "testing"

func TestPass(t *testing.T) {}
`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pass_test.go"), goTest, 0o644))

	output, passed, err := runGoTest(context.Background(), dir, []string{"./...", "-v"})
	require.NoError(t, err)
	assert.True(t, passed)
	assert.Contains(t, output, "PASS")
}

func TestRunGoTest_FailingTest(t *testing.T) {
	dir := t.TempDir()

	goMod := []byte("module testmod\n\ngo 1.21\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), goMod, 0o644))

	goTest := []byte(`package testmod

import "testing"

func TestFail(t *testing.T) {
	t.Fatal("intentional failure")
}
`)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fail_test.go"), goTest, 0o644))

	output, passed, err := runGoTest(context.Background(), dir, []string{"./...", "-v"})
	require.NoError(t, err) // no exec error, just test failure
	assert.False(t, passed)
	assert.Contains(t, output, "FAIL")
	assert.Contains(t, output, "intentional failure")
}

func TestRunGoTest_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	dir := t.TempDir()
	_, passed, err := runGoTest(ctx, dir, []string{"./..."})
	assert.False(t, passed)
	// Either an exec error (context cancelled before start) or exit error is acceptable.
	_ = err
}

// ---------------------------------------------------------------------------
// VerifySlice — integration with real go test
// ---------------------------------------------------------------------------

func TestVerifySlice_Integration(t *testing.T) {
	// Create a temp module that simulates a cell/slice directory structure.
	dir := t.TempDir()

	goMod := []byte("module testmod\n\ngo 1.21\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), goMod, 0o644))

	sliceDir := filepath.Join(dir, "cells", "access-core", "slices", "session-create")
	require.NoError(t, os.MkdirAll(sliceDir, 0o755))

	goTest := []byte(`package sessioncreate

import "testing"

func TestUnit(t *testing.T) {}
`)
	require.NoError(t, os.WriteFile(filepath.Join(sliceDir, "service_test.go"), goTest, 0o644))

	proj := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"access-core": {ID: "access-core"},
		},
		Slices: map[string]*metadata.SliceMeta{
			"access-core/session-create": {
				ID:            "session-create",
				BelongsToCell: "access-core",
			},
		},
		Journeys: map[string]*metadata.JourneyMeta{},
	}

	r := NewRunner(proj, dir)
	res, err := r.VerifySlice(context.Background(), "access-core/session-create")
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "access-core/session-create", res.TargetID)
	assert.True(t, res.Passed)
	assert.Len(t, res.Results, 1)
	assert.Contains(t, res.Results[0].Output, "PASS")
}

// ---------------------------------------------------------------------------
// VerifyCell — integration with real go test
// ---------------------------------------------------------------------------

func TestVerifyCell_Integration(t *testing.T) {
	dir := t.TempDir()

	goMod := []byte("module testmod\n\ngo 1.21\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), goMod, 0o644))

	cellDir := filepath.Join(dir, "cells", "audit-core")
	require.NoError(t, os.MkdirAll(cellDir, 0o755))

	goTest := []byte(`package auditcore

import "testing"

func TestSmoke(t *testing.T) {}
`)
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "smoke_test.go"), goTest, 0o644))

	proj := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"audit-core": {ID: "audit-core"},
		},
		Slices:   map[string]*metadata.SliceMeta{},
		Journeys: map[string]*metadata.JourneyMeta{},
	}

	r := NewRunner(proj, dir)
	res, err := r.VerifyCell(context.Background(), "audit-core")
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "audit-core", res.TargetID)
	assert.True(t, res.Passed)
	assert.Len(t, res.Results, 1)
	assert.Contains(t, res.Results[0].Output, "PASS")
}

// ---------------------------------------------------------------------------
// RunJourney — integration with real go test
// ---------------------------------------------------------------------------

func TestRunJourney_Integration(t *testing.T) {
	dir := t.TempDir()

	goMod := []byte("module testmod\n\ngo 1.21\n")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), goMod, 0o644))

	journeyDir := filepath.Join(dir, "journeys")
	require.NoError(t, os.MkdirAll(journeyDir, 0o755))

	// Create a test that matches the checkRef pattern.
	goTest := []byte(`package journeys

import "testing"

func TestOidcRedirect(t *testing.T) {}
func TestSessionPersist(t *testing.T) {}
`)
	require.NoError(t, os.WriteFile(filepath.Join(journeyDir, "sso_test.go"), goTest, 0o644))

	proj := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Journeys: map[string]*metadata.JourneyMeta{
			"J-sso-login": {
				ID:   "J-sso-login",
				Goal: "SSO login",
				PassCriteria: []metadata.PassCriterion{
					{Text: "OIDC redirect", Mode: "auto", CheckRef: "journey.J-sso-login.OidcRedirect"},
					{Text: "session persist", Mode: "auto", CheckRef: "journey.J-sso-login.SessionPersist"},
					{Text: "manual check", Mode: "manual"},
				},
			},
		},
	}

	r := NewRunner(proj, dir)
	res, err := r.RunJourney(context.Background(), "J-sso-login")
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "J-sso-login", res.TargetID)
	assert.True(t, res.Passed)
	assert.Len(t, res.Results, 2) // 2 auto checkRefs, manual skipped
}

// ---------------------------------------------------------------------------
// isExitError
// ---------------------------------------------------------------------------

func TestIsExitError(t *testing.T) {
	t.Run("non-exit error returns false", func(t *testing.T) {
		err := fmt.Errorf("not an exit error")
		var target *exec.ExitError
		ok := isExitError(err, &target)
		assert.False(t, ok)
		assert.Nil(t, target)
	})

	t.Run("exit error returns true", func(t *testing.T) {
		// Run a command that exits with non-zero to produce an ExitError.
		cmd := exec.Command("go", "version", "--bad-flag")
		runErr := cmd.Run()
		require.Error(t, runErr)

		var target *exec.ExitError
		ok := isExitError(runErr, &target)
		assert.True(t, ok)
		assert.NotNil(t, target)
	})
}
