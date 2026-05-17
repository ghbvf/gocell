package healthtest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/http/health"
)

// TestCaptureHandler_RecordsAndSnapshots verifies basic record/snapshot
// invariants: every slog event is recorded; Snapshot returns a defensive copy.
func TestCaptureHandler_RecordsAndSnapshots(t *testing.T) {
	h := &CaptureHandler{}
	logger := slog.New(h)

	logger.Info("first", slog.String("k", "v1"))
	logger.Warn("second", slog.Int("n", 42))

	snap := h.Snapshot()
	require.Len(t, snap, 2)
	assert.Equal(t, "first", snap[0].Message)
	assert.Equal(t, slog.LevelInfo, snap[0].Level)
	assert.Equal(t, "second", snap[1].Message)
	assert.Equal(t, slog.LevelWarn, snap[1].Level)

	// Defensive copy: mutating snap doesn't affect handler state.
	snap[0] = slog.Record{}
	snap2 := h.Snapshot()
	assert.Equal(t, "first", snap2[0].Message, "Snapshot must return defensive copy")
}

// TestCaptureHandler_EnabledAlwaysTrue verifies the simplified handler enables
// every level (so tests don't lose records due to default-level filtering).
func TestCaptureHandler_EnabledAlwaysTrue(t *testing.T) {
	h := &CaptureHandler{}
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		assert.True(t, h.Enabled(context.Background(), level),
			"CaptureHandler.Enabled must return true for level=%v", level)
	}
}

// TestCaptureHandler_WithAttrsAndGroupReturnSelf verifies the documented
// NoOp behavior (returns the same handler — attrs/groups not propagated).
func TestCaptureHandler_WithAttrsAndGroupReturnSelf(t *testing.T) {
	h := &CaptureHandler{}
	assert.Same(t, h, h.WithAttrs([]slog.Attr{slog.String("k", "v")}), "WithAttrs must return self (NoOp)")
	assert.Same(t, h, h.WithGroup("group"), "WithGroup must return self (NoOp)")
}

// TestNewCapture_RedirectsSlogDefaultAndRestoresOnCleanup verifies NewCapture
// installs the capture handler as slog default and t.Cleanup restores it.
func TestNewCapture_RedirectsSlogDefaultAndRestoresOnCleanup(t *testing.T) {
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	var capture *CaptureHandler
	t.Run("sub", func(t *testing.T) {
		capture = NewCapture(t)
		slog.Info("captured")
	})

	require.Len(t, capture.Snapshot(), 1)
	assert.Equal(t, "captured", capture.Snapshot()[0].Message)

	// After sub-test t.Cleanup ran, slog.Default must be restored to prev.
	assert.Same(t, prev, slog.Default(), "NewCapture t.Cleanup must restore prior default")
}

// TestReadyzUnhealthyDeps_FindsGroupRecord verifies the helper plumbing:
// given a slog record with a "readyz unhealthy" msg + "dependencies" slog.Group
// whose sub-attrs are SlogDependencyEntry-valued, the helper returns the
// typed map keyed by dep name.
//
// SlogDependencyEntry has unexported fields and no exported testing
// constructor (by design — see verbose_shape.go godoc). This unit test
// asserts ONLY plumbing correctness using zero-value entries: the helper
// must walk the Group structure and return a map with the right keys and
// the right value type. Semantic content (Status/ErrorMsg actual values)
// is exercised in the health package's own white-box tests via a real
// Handler.
func TestReadyzUnhealthyDeps_FindsGroupRecord(t *testing.T) {
	h := &CaptureHandler{}
	logger := slog.New(h)

	// Non-verbose record: status/reason only (no dependencies group).
	logger.Warn("readyz unhealthy",
		slog.String("status", "unhealthy"),
		slog.String("reason", "readiness_failed"),
	)

	// Verbose record: dependencies as slog.Group with SlogDependencyEntry
	// zero-value sub-attrs (zero-value is acceptable for plumbing test).
	logger.Warn("readyz unhealthy",
		slog.String("status", "unhealthy"),
		slog.String("reason", "readiness_failed"),
		slog.Group("dependencies",
			slog.Any("db", health.SlogDependencyEntry{}),
			slog.Any("redis", health.SlogDependencyEntry{}),
		),
	)

	got := ReadyzUnhealthyDeps(t, h)
	require.Len(t, got, 2)
	require.Contains(t, got, "db")
	require.Contains(t, got, "redis")
	// Zero-value plumbing: accessors return zero defaults.
	assert.Equal(t, "", got["db"].Status())
	assert.Equal(t, int64(0), got["db"].DurationMs())
	assert.Equal(t, "", got["db"].ErrorMsg())
}

// TestReadyzUnhealthyDeps_SkipsNonGroupAttr verifies the helper skips
// records where "dependencies" attr is not a Group (e.g. an Any with a
// non-Group value) — protects against false matches if logDiagnostics
// regresses back to slog.Any(map).
func TestReadyzUnhealthyDeps_SkipsNonGroupAttr(t *testing.T) {
	h := &CaptureHandler{}
	logger := slog.New(h)

	// Wrong shape: slog.Any with a map value (round-4 regression form).
	logger.Warn("readyz unhealthy",
		slog.Any("dependencies", map[string]any{"db": "wrong-shape"}),
	)
	// Right shape: slog.Group.
	logger.Warn("readyz unhealthy",
		slog.Group("dependencies", slog.Any("ok", health.SlogDependencyEntry{})),
	)

	got := ReadyzUnhealthyDeps(t, h)
	require.Len(t, got, 1, "helper must skip non-Group dependencies attr and find the Group one")
	require.Contains(t, got, "ok")
}

// TestHasReadyzDependencyStatus verifies match and no-match paths against
// captured verbose records.
func TestHasReadyzDependencyStatus(t *testing.T) {
	h := &CaptureHandler{}
	logger := slog.New(h)

	// Zero-value entries: Status() returns "" — assert against "".
	logger.Warn("readyz unhealthy",
		slog.Group("dependencies",
			slog.Any("db", health.SlogDependencyEntry{}),
			slog.Any("redis", health.SlogDependencyEntry{}),
		),
	)

	assert.True(t, HasReadyzDependencyStatus(h, "db", ""),
		"zero-value entry's Status() is empty string; match on '' must succeed")
	assert.False(t, HasReadyzDependencyStatus(h, "db", "unhealthy"),
		"wrong status must not match")
	assert.False(t, HasReadyzDependencyStatus(h, "missing", ""),
		"missing key must not match")
}

// TestHasReadyzDependencyStatus_NoRecord verifies the helper returns false when
// no matching "readyz unhealthy" slog record exists.
func TestHasReadyzDependencyStatus_NoRecord(t *testing.T) {
	h := &CaptureHandler{}
	assert.False(t, HasReadyzDependencyStatus(h, "db", "healthy"),
		"empty capture must return false (no slog records)")
}

// TestHasReadyzDependencyStatus_SkipsNonVerboseRecord verifies non-verbose
// "readyz unhealthy" records (no dependencies attr) are skipped without
// false-positive matches.
func TestHasReadyzDependencyStatus_SkipsNonVerboseRecord(t *testing.T) {
	h := &CaptureHandler{}
	logger := slog.New(h)

	logger.Warn("readyz unhealthy",
		slog.String("status", "unhealthy"),
		slog.String("reason", "readiness_failed"),
	)

	assert.False(t, HasReadyzDependencyStatus(h, "db", "unhealthy"),
		"non-verbose record (no dependencies attr) must not match")
}
