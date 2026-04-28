package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
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

func TestRunGoTest_SkipOnlyReal(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.21\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skip_test.go"), []byte(`package testmod
import "testing"
func TestOnlySkip(t *testing.T) { t.Skip("stub") }
`), 0o644))

	res := runGoTest(context.Background(), dir, []string{"./...", "-v", "-run", "^TestOnlySkip$"})
	require.NoError(t, res.Err)
	assert.True(t, res.Passed, "go test exits 0 for skipped tests")
	assert.True(t, res.SkippedOnly, "stub-only tests must not count as verified")
}

func TestRunGoTest_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := runGoTest(ctx, t.TempDir(), []string{"./..."})
	assert.False(t, res.Passed)
}

func TestRunGoTest_UsesCallerSelectedGoAndAdditivePATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell script as the fake go binary")
	}

	dir := t.TempDir()
	realGo, err := exec.LookPath("go")
	require.NoError(t, err)

	selectedBin := filepath.Join(dir, "selected-go-bin")
	sentinelBin := filepath.Join(dir, "sentinel-bin")
	require.NoError(t, os.Mkdir(selectedBin, 0o755))
	require.NoError(t, os.Mkdir(sentinelBin, 0o755))

	logPath := filepath.Join(dir, "go-wrapper.log")
	require.NoError(t, os.WriteFile(filepath.Join(selectedBin, "go"), []byte(`#!/bin/sh
printf '%s\n' "$*" >> "$GO_WRAPPER_LOG"
exec "$REAL_GO" "$@"
`), 0o755))

	t.Setenv("PATH", selectedBin+string(os.PathListSeparator)+sentinelBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("REAL_GO", realGo)
	t.Setenv("GO_WRAPPER_LOG", logPath)
	t.Setenv("GOCELL_VERIFY_SELECTED_BIN", selectedBin)
	t.Setenv("GOCELL_VERIFY_SENTINEL_BIN", sentinelBin)

	moduleDir := filepath.Join(dir, "module")
	require.NoError(t, os.Mkdir(moduleDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module testmod\n\ngo 1.21\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(moduleDir, "path_test.go"), []byte(`package testmod
import (
	"os"
	"strings"
	"testing"
)

func TestPATHIsAdditive(t *testing.T) {
	path := os.Getenv("PATH")
	selected := os.Getenv("GOCELL_VERIFY_SELECTED_BIN")
	sentinel := os.Getenv("GOCELL_VERIFY_SENTINEL_BIN")
	if selected == "" || sentinel == "" {
		t.Fatal("missing test path markers")
	}
	selectedIndex := strings.Index(path, selected)
	if selectedIndex < 0 {
		t.Fatalf("PATH does not include selected go dir: %s", path)
	}
	sentinelIndex := strings.Index(path, sentinel)
	if sentinelIndex < 0 {
		t.Fatalf("PATH lost caller-provided entries: %s", path)
	}
	if selectedIndex > sentinelIndex {
		t.Fatalf("selected go dir should be prepended before caller entries: %s", path)
	}
}
`), 0o644))

	res := runGoTest(context.Background(), moduleDir, []string{"./...", "-v"})
	require.NoError(t, res.Err)
	assert.True(t, res.Passed, res.Output)

	logBytes, err := os.ReadFile(logPath)
	require.NoError(t, err)
	assert.Contains(t, string(logBytes), "test ./... -v")
}

func TestGoTestEnvPreservesCallerEnvAndPrependsGoDir(t *testing.T) {
	selectedDir := t.TempDir()
	callerBin := filepath.Join(t.TempDir(), "caller-bin")
	callerGoRoot := filepath.Join(t.TempDir(), "caller-goroot")
	t.Setenv("PATH", callerBin)
	t.Setenv("GOROOT", callerGoRoot)

	env := goTestEnv(filepath.Join(selectedDir, goToolName()))
	pathValue := envValue(env, "PATH")
	pathParts := strings.Split(pathValue, string(os.PathListSeparator))
	require.NotEmpty(t, pathParts)
	assert.Equal(t, selectedDir, pathParts[0])
	assert.Contains(t, pathValue, callerBin)
	assert.Equal(t, callerGoRoot, envValue(env, "GOROOT"))
}

func envValue(env []string, key string) string {
	for _, entry := range env {
		envKey, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(envKey, key) {
			return value
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// VerifySlice — integration with real go test (fallback path)
// ---------------------------------------------------------------------------

func TestVerifySlice_Integration(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testmod\n\ngo 1.21\n"), 0o644))

	sliceDir := filepath.Join(dir, "cells", "accesscore", "slices", "session-create")
	require.NoError(t, os.MkdirAll(sliceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(sliceDir, "service_test.go"), []byte(`package sessioncreate
import "testing"
func TestUnit(t *testing.T) {}
`), 0o644))

	proj := &metadata.ProjectMeta{
		Cells:    map[string]*metadata.CellMeta{"accesscore": {ID: "accesscore"}},
		Slices:   map[string]*metadata.SliceMeta{"accesscore/session-create": {ID: "session-create", BelongsToCell: "accesscore"}},
		Journeys: map[string]*metadata.JourneyMeta{},
	}

	r := NewRunner(proj, dir)
	res, err := r.VerifySlice(context.Background(), "accesscore/session-create")
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

	cellDir := filepath.Join(dir, "cells", "auditcore")
	require.NoError(t, os.MkdirAll(cellDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(cellDir, "smoke_test.go"), []byte(`package auditcore
import "testing"
func TestWrite(t *testing.T) {}
`), 0o644))

	proj := &metadata.ProjectMeta{
		Cells: map[string]*metadata.CellMeta{
			"auditcore": {
				ID:     "auditcore",
				Verify: metadata.CellVerifyMeta{Smoke: []string{"smoke.auditcore.write"}},
			},
		},
		Slices:   map[string]*metadata.SliceMeta{},
		Journeys: map[string]*metadata.JourneyMeta{},
	}

	r := NewRunner(proj, dir)
	res, err := r.VerifyCell(context.Background(), "auditcore")
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
func TestJSsologinOidcRedirect(t *testing.T) {}
func TestJSsologinSessionPersist(t *testing.T) {}
`), 0o644))

	proj := &metadata.ProjectMeta{
		Cells:  map[string]*metadata.CellMeta{},
		Slices: map[string]*metadata.SliceMeta{},
		Journeys: map[string]*metadata.JourneyMeta{
			"J-ssologin": {
				ID: "J-ssologin",
				PassCriteria: []metadata.PassCriterion{
					{Text: "OIDC redirect", Mode: "auto", CheckRef: "journey.J-ssologin.oidc-redirect"},
					{Text: "session persist", Mode: "auto", CheckRef: "journey.J-ssologin.session-persist"},
					{Text: "manual check", Mode: "manual"},
				},
			},
		},
	}

	r := NewRunner(proj, dir)
	res, err := r.RunJourney(context.Background(), "J-ssologin")
	require.NoError(t, err)
	assert.True(t, res.Passed)
	assert.Len(t, res.Results, 2)
	assert.Equal(t, []string{"manual check"}, res.ManualPending)
}
