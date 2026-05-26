package main

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// TestLogAssemblyMaturity verifies that logAssemblyMaturity emits an Info
// record with the expected message, a total_cells attr, and per-phase counts.
//
// Subtests are sequential (not t.Parallel on subtests) because logAssemblyMaturity
// writes to slog.Default, which is a process-global — parallel subtests would
// race on the captured records. The parent test is parallel-safe at the
// package level (it uses withSlogCapture which swaps slog.Default per-test).
func TestLogAssemblyMaturity(t *testing.T) {
	t.Run("mixed phases", func(t *testing.T) {
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

		attrs := collectAttrs(recs[0])
		assert.Equal(t, int64(3), attrs["total_cells"], "total_cells must equal len(cells)")
		assert.Equal(t, int64(2), attrs["asset"], "asset count")
		assert.Equal(t, int64(1), attrs["candidate"], "candidate count")
		// phases with zero count are omitted
		_, hasExp := attrs["experimental"]
		assert.False(t, hasExp, "experimental phase with 0 cells must be omitted")
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
		attrs := collectAttrs(recs[0])
		assert.Equal(t, int64(2), attrs["total_cells"])
		assert.Equal(t, int64(2), attrs["experimental"])
	})

	t.Run("empty cell list produces no log", func(t *testing.T) {
		capture := withSlogCapture(t)
		logAssemblyMaturity(nil)
		assert.Empty(t, capture.Snapshot(), "empty cell list must not emit any log record")
	})
}

// collectAttrs returns a flat map of the top-level int64 attrs on r.
func collectAttrs(r slog.Record) map[string]int64 {
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
