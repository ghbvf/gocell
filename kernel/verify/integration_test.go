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

// ---------------------------------------------------------------------------
// runGoTest — integration with real go test
// ---------------------------------------------------------------------------

func TestRunGoTest_WithTempModule(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.21\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pass_test.go"), []byte(`package testmod
import "testing"
func TestPass(t *testing.T) {}
`), 0o644))

	res := runGoTest(context.Background(), dir, []string{"./...", "-v"})
	require.NoError(t, res.Err)
	assert.True(t, res.Passed)
	assert.False(t, res.ZeroMatch)
	assert.Contains(t, res.Output, "PASS")
}

func TestRunGoTest_FailingTest(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.21\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fail_test.go"), []byte(`package testmod
import "testing"
func TestFail(t *testing.T) { t.Fatal("intentional failure") }
`), 0o644))

	res := runGoTest(context.Background(), dir, []string{"./...", "-v"})
	require.NoError(t, res.Err)
	assert.False(t, res.Passed)
	assert.Contains(t, res.Output, "intentional failure")
}

func TestRunGoTest_ZeroMatchReal(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.21\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pass_test.go"), []byte(`package testmod
import "testing"
func TestPass(t *testing.T) {}
`), 0o644))

	res := runGoTest(context.Background(), dir, []string{"./...", "-v", "-run", "NeverMatchAnything"})
	require.NoError(t, res.Err)
	assert.True(t, res.Passed, "go test exits 0 on zero match")
	assert.True(t, res.ZeroMatch, "should detect zero match")
}

func TestRunGoTest_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := runGoTest(ctx, t.TempDir(), []string{"./..."})
	assert.False(t, res.Passed)
}

// ---------------------------------------------------------------------------
// VerifySlice — integration with real go test (fallback path)
// ---------------------------------------------------------------------------

func TestVerifySlice_Integration(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.21\n"), 0o644))

	sliceDir := filepath.Join(dir, "cells", "access-core", "slices", "session-create")
	require.NoError(t, os.MkdirAll(sliceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sliceDir, "service_test.go"), []byte(`package sessioncreate
import "testing"
func TestUnit(t *testing.T) {}
`), 0o644))

	proj := &metadata.ProjectMeta{
		Cells:    map[string]*metadata.CellMeta{"access-core": {ID: "access-core"}},
		Slices:   map[string]*metadata.SliceMeta{"access-core/session-create": {ID: "session-create", BelongsToCell: "access-core"}},
		Journeys: map[string]*metadata.JourneyMeta{},
	}

	r := NewRunner(proj, dir)
	res, err := r.VerifySlice(context.Background(), "access-core/session-create")
	require.NoError(t, err)
	assert.True(t, res.Passed)
	assert.Contains(t, res.Results[0].Output, "PASS")
}

// ---------------------------------------------------------------------------
// VerifySlice — integration with metadata-driven refs
// ---------------------------------------------------------------------------

func TestVerifySlice_WithMetadataRefs(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.21\n"), 0o644))

	sliceDir := filepath.Join(dir, "cells", "c", "slices", "s")
	require.NoError(t, os.MkdirAll(sliceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sliceDir, "svc_test.go"), []byte(`package s
import "testing"
func TestService(t *testing.T) {}
func TestHandler(t *testing.T) {}
`), 0o644))

	proj := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{"c": {ID: "c"}},
		Slices: map[string]*metadata.SliceMeta{
			"c/s": {
				ID: "s", BelongsToCell: "c",
				Verify: metadata.SliceVerifyMeta{
					Unit: []string{"unit.s.service"},
				},
			},
		},
		Journeys: map[string]*metadata.JourneyMeta{},
	}

	r := NewRunner(proj, dir)
	res, err := r.VerifySlice(context.Background(), "c/s")
	require.NoError(t, err)
	assert.True(t, res.Passed)
	require.Len(t, res.Results, 1)
	assert.Contains(t, res.Results[0].Output, "PASS")
}

// ---------------------------------------------------------------------------
// VerifyCell — integration with metadata-driven smoke
// ---------------------------------------------------------------------------

func TestVerifyCell_Integration(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.21\n"), 0o644))

	cellDir := filepath.Join(dir, "cells", "audit-core")
	require.NoError(t, os.MkdirAll(cellDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "smoke_test.go"), []byte(`package auditcore
import "testing"
func TestWrite(t *testing.T) {}
`), 0o644))

	proj := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"audit-core": {
				ID:    "audit-core",
				Verify: metadata.CellVerifyMeta{Smoke: []string{"smoke.audit-core.write"}},
			},
		},
		Slices:   map[string]*metadata.SliceMeta{},
		Journeys: map[string]*metadata.JourneyMeta{},
	}

	r := NewRunner(proj, dir)
	res, err := r.VerifyCell(context.Background(), "audit-core")
	require.NoError(t, err)
	assert.True(t, res.Passed)
	require.Len(t, res.Results, 1)
	assert.Contains(t, res.Results[0].Output, "PASS")
}

func TestVerifyCell_InvalidSmokeRef(t *testing.T) {
	r := NewRunner(&metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"c": {ID: "c", Verify: metadata.CellVerifyMeta{Smoke: []string{"totally-invalid"}}},
		},
	}, t.TempDir())

	res, err := r.VerifyCell(context.Background(), "c")
	require.NoError(t, err)
	assert.False(t, res.Passed, "non-legacy invalid ref should fail")
	require.Len(t, res.Errors, 1)
	assert.Contains(t, res.Errors[0].Error(), "ERR_CHECKREF_INVALID")
}

// ---------------------------------------------------------------------------
// RunJourney — integration with real go test
// ---------------------------------------------------------------------------

func TestRunJourney_Integration(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.21\n"), 0o644))

	journeyDir := filepath.Join(dir, "journeys")
	require.NoError(t, os.MkdirAll(journeyDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(journeyDir, "sso_test.go"), []byte(`package journeys
import "testing"
func TestJSsoLoginOidcRedirect(t *testing.T) {}
func TestJSsoLoginSessionPersist(t *testing.T) {}
`), 0o644))

	proj := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Journeys: map[string]*metadata.JourneyMeta{
			"J-sso-login": {
				ID: "J-sso-login",
				PassCriteria: []metadata.PassCriterion{
					{Text: "OIDC redirect", Mode: "auto", CheckRef: "journey.J-sso-login.oidc-redirect"},
					{Text: "session persist", Mode: "auto", CheckRef: "journey.J-sso-login.session-persist"},
					{Text: "manual check", Mode: "manual"},
				},
			},
		},
	}

	r := NewRunner(proj, dir)
	res, err := r.RunJourney(context.Background(), "J-sso-login")
	require.NoError(t, err)
	assert.True(t, res.Passed)
	assert.Len(t, res.Results, 2)
	assert.Equal(t, []string{"manual check"}, res.ManualPending)
}

