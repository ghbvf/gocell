package metrics

import (
	"context"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
)

// CellLabel is the sealed, validated cell dimension for a metric series. It is
// the only type the Collector / GRPCCollector record methods accept for the
// `cell` label.
//
// Sealed construction (Hard, same shape as kernel/outbox.Entry): the single
// field is unexported and the sole exported constructor is [ResolveCellLabel],
// so constructing a non-zero CellLabel outside this package is a compile error.
// Passing a raw, unvalidated cell-id string to a collector is therefore
// unexpressible — every cell label that reaches a collector has been checked
// against the assembly's closed cell-id set, and an out-of-set or absent cell
// degrades to [RuntimeCellSentinel] rather than polluting the platform SLO
// series.
//
// This is the M12b (#1093) runtime defense-in-depth, deliberately LAYERED with —
// not contradicting — the M12a build-time closed-set gate
// (composition.Builder.Build):
//
//   - M12a hard-rejects an out-of-set cell id at BUILD time: a misconfigured
//     assembly must fail fast before serving ("越界 cellID 硬拒、不降级").
//   - M12b degrades an out-of-set cell id to the sentinel at the RUNTIME metric
//     write point: a live request must not panic over a metric label, so the one
//     label degrades rather than crashing or polluting the series ("→ _runtime").
//
// "不降级" governs the build path; "→ sentinel" governs the runtime label path.
// See .claude/rules/gocell/observability.md "HTTP Metrics cell Label".
type CellLabel struct {
	// v is the validated cell id, or "" for the framework sentinel. Unexported:
	// a non-zero CellLabel cannot be constructed outside this package.
	v string
}

// ResolveCellLabel is the SOLE constructor of a non-zero CellLabel. It reads the
// owning cell id written into ctx by the listener-root CellAttribution
// middleware and returns it ONLY when present AND a member of valid — the
// assembly's closed cell-id set, threaded in by the router via
// WithCellIDClosedSet. An absent cell id, an empty one, or one not in valid
// (including an empty/nil valid set, which is never a member) yields the zero
// CellLabel, whose String() renders as RuntimeCellSentinel.
//
// Test code that needs a CellLabel without a real request context should use
// metricstest.Label(id) (runtime/observability/metrics/metricstest), which
// routes through this funnel.
func ResolveCellLabel(ctx context.Context, valid map[string]struct{}) CellLabel {
	v, ok := ctxkeys.CellIDFrom(ctx)
	if !ok || v == "" {
		return CellLabel{}
	}
	if _, member := valid[v]; !member {
		return CellLabel{}
	}
	return CellLabel{v: v}
}

// String returns the metric label value: the validated cell id, or
// RuntimeCellSentinel for the zero value (absent / out-of-set / framework path).
// The zero-value-is-sentinel rule means a CellLabel{} produced by any path —
// including the unavoidable Go zero value — can never emit an empty or forged
// cell label.
func (c CellLabel) String() string {
	if c.v == "" {
		return RuntimeCellSentinel
	}
	return c.v
}
