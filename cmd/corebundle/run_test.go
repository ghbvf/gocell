package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/http/health/healthtest"
)

// TestReleaseUnhandedResources_ClosesPoolWhenNotHanded verifies that the
// startup-abort defer closes the postgres pool when bootstrap.Run never took
// ownership (handedToBootstrap == false). Regression for F2: the same defer now
// also covers the redis client (created in LoadSharedDepsFromEnv), which leaked
// before the fix. The redis concrete-close path is nil-safe here and exercised
// end-to-end in real/redis integration tests.
func TestReleaseUnhandedResources_ClosesPoolWhenNotHanded(t *testing.T) {
	pool := &fakeManagedResource{}
	locals := &cmdLocals{poolMR: pool, redisClient: nil}

	releaseUnhandedResources(context.Background(), locals, false)

	assert.True(t, pool.closeCalled, "pool must be closed when startup aborts before handoff")
}

// TestReleaseUnhandedResources_ClosesBrokerResources verifies the startup-abort
// defer also closes the event-transport broker connection (#1940): it is dialed
// eagerly inside LoadSharedDepsFromEnv, so a failure between Load and bootstrap.Run
// (provisionCapabilities / composition.Build) would otherwise leak the open broker
// socket. handedToBootstrap == false → close; handed → no-op.
func TestReleaseUnhandedResources_ClosesBrokerResources(t *testing.T) {
	broker := &fakeManagedResource{}
	locals := &cmdLocals{brokerResources: []kernellifecycle.ManagedResource{broker}}
	releaseUnhandedResources(context.Background(), locals, false)
	assert.True(t, broker.closeCalled, "broker connection must be closed when startup aborts before handoff")

	handed := &fakeManagedResource{}
	handedLocals := &cmdLocals{brokerResources: []kernellifecycle.ManagedResource{handed}}
	releaseUnhandedResources(context.Background(), handedLocals, true)
	assert.False(t, handed.closeCalled, "handed-off broker is owned by bootstrap; defer must not close it")
}

// TestReleaseUnhandedResources_NoCloseWhenHanded verifies the handoff gate: once
// bootstrap.Run owns the resources (handedToBootstrap == true) the defer must be
// a no-op, otherwise the resource would be double-closed.
func TestReleaseUnhandedResources_NoCloseWhenHanded(t *testing.T) {
	pool := &fakeManagedResource{}
	locals := &cmdLocals{poolMR: pool, redisClient: nil}

	releaseUnhandedResources(context.Background(), locals, true)

	assert.False(t, pool.closeCalled, "handed-off resources are owned by bootstrap; defer must not close them")
}

// TestReleaseUnhandedResources_NilSafe verifies the defer tolerates a nil pool
// and a nil redis client (the common memory-mode / pre-provision states) without
// panicking.
func TestReleaseUnhandedResources_NilSafe(t *testing.T) {
	locals := &cmdLocals{poolMR: nil, redisClient: nil}

	assert.NotPanics(t, func() {
		releaseUnhandedResources(context.Background(), locals, false)
	})
}

// withSlogCapture redirects slog.Default for the duration of the test.
// Delegates to healthtest.NewCapture.
func withSlogCapture(t *testing.T) *healthtest.CaptureHandler {
	t.Helper()
	return healthtest.NewCapture(t)
}

// TestLogAssemblyMaturity verifies that logAssemblyMaturity emits an Info
// record with the expected message, a total_cells attr, and per-lifecycle
// counts inside a "lifecycle" slog.Group.
//
// Subtests are sequential (not t.Parallel on subtests) because logAssemblyMaturity
// writes to slog.Default, which is a process-global — parallel subtests would
// race on the captured records. The parent test is parallel-safe at the
// package level (it uses withSlogCapture which swaps slog.Default per-test).
func TestLogAssemblyMaturity(t *testing.T) {
	t.Run("mixed lifecycles", func(t *testing.T) {
		capture := withSlogCapture(t)

		cells := []cell.Cell{
			cell.MustNewBaseCell(&metadata.CellMeta{ID: "alpha", Lifecycle: "asset"}),
			cell.MustNewBaseCell(&metadata.CellMeta{ID: "beta", Lifecycle: "asset"}),
			cell.MustNewBaseCell(&metadata.CellMeta{ID: "gamma", Lifecycle: "candidate"}),
		}
		logAssemblyMaturity(cells)

		recs := capture.Snapshot()
		require.Len(t, recs, 1)
		assert.Equal(t, "corebundle: assembly maturity composition", recs[0].Message)
		assert.Equal(t, slog.LevelInfo, recs[0].Level)

		top := collectTopAttrs(recs[0])
		assert.Equal(t, int64(3), top["total_cells"], "total_cells must equal len(cells)")

		lc := collectGroupAttrs(recs[0], "lifecycle")
		assert.Equal(t, int64(2), lc["asset"], "asset count")
		assert.Equal(t, int64(1), lc["candidate"], "candidate count")
		// zero counts are still present in the stable group (no omission)
		assert.Equal(t, int64(0), lc["experimental"], "experimental count must be 0")
		assert.Equal(t, int64(0), lc["maintenance"], "maintenance count must be 0")
		assert.Equal(t, int64(0), lc["retired"], "retired count must be 0")
	})

	t.Run("all experimental (empty lifecycle)", func(t *testing.T) {
		capture := withSlogCapture(t)

		cells := []cell.Cell{
			cell.MustNewBaseCell(&metadata.CellMeta{ID: "a"}), // empty → experimental
			cell.MustNewBaseCell(&metadata.CellMeta{ID: "b"}),
		}
		logAssemblyMaturity(cells)

		recs := capture.Snapshot()
		require.Len(t, recs, 1)
		top := collectTopAttrs(recs[0])
		assert.Equal(t, int64(2), top["total_cells"])
		lc := collectGroupAttrs(recs[0], "lifecycle")
		assert.Equal(t, int64(2), lc["experimental"])
	})

	t.Run("mixed with maintenance and retired", func(t *testing.T) {
		capture := withSlogCapture(t)

		cells := []cell.Cell{
			cell.MustNewBaseCell(&metadata.CellMeta{ID: "a", Lifecycle: "maintenance"}),
			cell.MustNewBaseCell(&metadata.CellMeta{ID: "b", Lifecycle: "retired"}),
			cell.MustNewBaseCell(&metadata.CellMeta{ID: "c", Lifecycle: "asset"}),
		}
		logAssemblyMaturity(cells)

		recs := capture.Snapshot()
		require.Len(t, recs, 1)
		top := collectTopAttrs(recs[0])
		assert.Equal(t, int64(3), top["total_cells"])
		lc := collectGroupAttrs(recs[0], "lifecycle")
		assert.Equal(t, int64(1), lc["maintenance"], "maintenance count")
		assert.Equal(t, int64(1), lc["retired"], "retired count")
		assert.Equal(t, int64(1), lc["asset"], "asset count")
		assert.Equal(t, int64(0), lc["experimental"], "experimental count must be 0")
		assert.Equal(t, int64(0), lc["candidate"], "candidate count must be 0")
	})

	t.Run("empty cell list produces no log", func(t *testing.T) {
		capture := withSlogCapture(t)
		logAssemblyMaturity(nil)
		assert.Empty(t, capture.Snapshot(), "empty cell list must not emit any log record")
	})
}

// collectTopAttrs returns a flat map of the top-level int64 attrs on r.
func collectTopAttrs(r slog.Record) map[string]int64 {
	out := make(map[string]int64)
	r.Attrs(func(a slog.Attr) bool {
		v := a.Value.Resolve()
		if v.Kind() == slog.KindInt64 {
			out[a.Key] = v.Int64()
		}
		return true
	})
	return out
}

// collectGroupAttrs returns a flat map of int64 attrs inside the named
// slog.Group on record r. Returns an empty map if the group is absent.
func collectGroupAttrs(r slog.Record, groupKey string) map[string]int64 {
	out := make(map[string]int64)
	r.Attrs(func(a slog.Attr) bool {
		if a.Key != groupKey {
			return true
		}
		v := a.Value.Resolve()
		if v.Kind() != slog.KindGroup {
			return true
		}
		for _, ga := range v.Group() {
			gv := ga.Value.Resolve()
			if gv.Kind() == slog.KindInt64 {
				out[ga.Key] = gv.Int64()
			}
		}
		return true
	})
	return out
}

// collectGroupStringAttrs returns a flat map of string attrs inside the named
// slog.Group on record r. Returns an empty map if the group is absent.
func collectGroupStringAttrs(r slog.Record, groupKey string) map[string]string {
	out := make(map[string]string)
	r.Attrs(func(a slog.Attr) bool {
		if a.Key != groupKey {
			return true
		}
		v := a.Value.Resolve()
		if v.Kind() != slog.KindGroup {
			return true
		}
		for _, ga := range v.Group() {
			gv := ga.Value.Resolve()
			if gv.Kind() == slog.KindString {
				out[ga.Key] = gv.String()
			}
		}
		return true
	})
	return out
}

// TestLogAssemblyMaturity_CellIDsPerPhase verifies that logAssemblyMaturity
// emits per-phase cell ID lists alongside counts. Cell IDs for a phase are
// emitted as a "<phase>_cells" string attr containing a comma-separated list.
// Zero-count phases produce no "_cells" attr (no empty noise).
func TestLogAssemblyMaturity_CellIDsPerPhase(t *testing.T) {
	capture := withSlogCapture(t)

	cells := []cell.Cell{
		cell.MustNewBaseCell(&metadata.CellMeta{ID: "alpha", Lifecycle: "asset"}),
		cell.MustNewBaseCell(&metadata.CellMeta{ID: "beta", Lifecycle: "asset"}),
		cell.MustNewBaseCell(&metadata.CellMeta{ID: "gamma", Lifecycle: "candidate"}),
	}
	logAssemblyMaturity(cells)

	recs := capture.Snapshot()
	require.Len(t, recs, 1)

	lcStr := collectGroupStringAttrs(recs[0], "lifecycle")
	// asset phase has two cells: both IDs must appear in the comma-separated value.
	assetCells := lcStr["asset_cells"]
	assert.Contains(t, assetCells, "alpha", "alpha must be in asset_cells")
	assert.Contains(t, assetCells, "beta", "beta must be in asset_cells")
	// candidate phase has one cell.
	assert.Equal(t, "gamma", lcStr["candidate_cells"], "candidate_cells must be 'gamma'")
	// zero-count phases must not emit a _cells attr.
	assert.Empty(t, lcStr["experimental_cells"], "experimental_cells must be absent for zero-count phase")
	assert.Empty(t, lcStr["maintenance_cells"], "maintenance_cells must be absent for zero-count phase")
	assert.Empty(t, lcStr["retired_cells"], "retired_cells must be absent for zero-count phase")
}
