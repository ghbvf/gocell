// Package metricstest provides a shared conformance suite for implementations
// of [metrics.Collector]. Any implementation — InMemoryCollector (dev/test) or
// providerCollector (production Prometheus/OTel-backed) — runs
// RunCollectorConformance to verify the shared recording contract.
//
// Because Collector is a write-only sink and the two production implementations
// have different observation mechanisms (InMemoryCollector.Snapshot vs. spy
// counter on a provider), the suite is parameterised through CollectorHarness:
// each implementation supplies its own New() constructor that returns a
// self-contained (Collector, CollectorObserver) pair. All observation state is
// local to that pair; the harness itself holds no mutable shared state. Subtests
// run serially (see RunCollectorConformance) so the suite is deterministic.
//
// The suite file is a plain .go file (not _test.go) so it can be imported by
// _test.go files in other packages without being stripped from the build graph
// (mirrors the sagajournaltest pattern).
package metricstest

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

const (
	testSessionsRoute = "/api/v1/sessions"
	testUploadRoute   = "/api/v1/upload"
)

// Label builds a [metrics.CellLabel] for a genuine closed-set MEMBER by routing
// a cell id through the sole [metrics.ResolveCellLabel] funnel (ctx-injected cell
// + singleton allow-set in which id IS a member). The suite therefore constructs
// no CellLabel out of band, so the sealed-construction guarantee stays intact
// even in test code.
//
// Do NOT call Label(RuntimeCellSentinel) to obtain the framework sentinel: that
// resolves the sentinel string via the membership-HIT path (id placed in the
// allow-set), which never happens in production. Use [RuntimeLabel] instead so
// the sentinel is produced via its real miss-path provenance.
func Label(id string) metrics.CellLabel {
	if id == "" {
		return RuntimeLabel()
	}
	return metrics.ResolveCellLabel(
		ctxkeys.WithCellID(context.Background(), id),
		map[string]struct{}{id: {}},
	)
}

// RuntimeLabel returns the framework sentinel CellLabel via its real production
// provenance — the [metrics.ResolveCellLabel] MISS path (no owning cell id in
// ctx) — rather than passing RuntimeCellSentinel through [Label] as if it were a
// closed-set member. Tests asserting the framework / unmatched / out-of-set path
// must use this so the sentinel's construction mirrors production (an absent or
// out-of-set cell degraded by the funnel), not a synthetic member hit.
func RuntimeLabel() metrics.CellLabel {
	return metrics.ResolveCellLabel(context.Background(), nil)
}

// RequestKey mirrors metrics.RequestKey for harness observation without
// requiring the test-side to import the internal key type directly.
type RequestKey = metrics.RequestKey

// BodyLimitRejectionKey mirrors metrics.BodyLimitRejectionKey.
type BodyLimitRejectionKey = metrics.BodyLimitRejectionKey

// CollectorObserver is the read-side companion returned by
// CollectorHarness.New. It is bound exclusively to the Collector returned in
// the same New call; each subtest holds its own observer instance with no
// cross-subtest sharing.
//
//   - RequestCount: returns how many times RecordRequest was called for the
//     given key on that Collector. Returns 0 if the key has not been recorded.
//   - BodyLimitRejectionCount: returns how many times RecordBodyLimitRejection
//     was called for the given key. Returns 0 if never recorded.
//   - LastCtxForBodyLimitRejection: returns the context that was passed to the
//     most recent RecordBodyLimitRejection call, or nil if no call was made.
//     Implementations that cannot observe ctx may return nil; the corresponding
//     harness subtest will skip the ctx-forwarding assertion.
type CollectorObserver interface {
	RequestCount(key RequestKey) int64
	BodyLimitRejectionCount(key BodyLimitRejectionKey) int64
	LastCtxForBodyLimitRejection() context.Context
}

// CollectorHarness wraps one Collector implementation with a factory that
// produces self-contained (Collector, CollectorObserver) pairs. The harness
// itself must hold no mutable state; all per-subtest state lives in the
// returned pair, so each subtest is fully isolated.
type CollectorHarness interface {
	// New constructs a fresh Collector and its bound CollectorObserver for one
	// subtest. The returned observer's read methods reflect only the Collector
	// returned alongside it. Implementations must not write to shared harness
	// fields, so the harness can be reused across subtests.
	New(t *testing.T) (metrics.Collector, CollectorObserver)
}

// RunCollectorConformance runs the full Collector conformance suite against
// the supplied harness. Every subtest is registered as a t.Run so individual
// cases can be filtered with -run.
//
// Conformance assertions cover:
//  1. RecordRequest: count increments correctly per unique label set.
//  2. RecordRequest: cell and route labels are independent dimensions (no
//     label cross-contamination).
//  3. RecordBodyLimitRejection: count increments correctly.
//  4. RecordBodyLimitRejection: the caller ctx is forwarded to the
//     underlying instrument (OTel exemplar/baggage passthrough). Skipped
//     when the harness cannot observe ctx (LastCtxForBodyLimitRejection nil).
//  5. RecordBodyLimitRejection: must not pollute the RecordRequest series.
//  6. RecordBodyLimitRejection: body-limit counter is isolated per
//     (cell, route) key.
func RunCollectorConformance(t *testing.T, h CollectorHarness) {
	t.Helper()

	cases := []struct {
		name string
		run  func(*testing.T, CollectorHarness)
	}{
		{"RecordRequest_CountIncrements", conformRecordRequestCount},
		{"RecordRequest_LabelIsolation_CellRoute", conformRecordRequestLabelIsolation},
		{"RecordBodyLimitRejection_CountIncrements", conformBodyLimitRejectionCount},
		{"RecordBodyLimitRejection_CtxForwarded", conformBodyLimitRejectionCtxForwarded},
		{"RecordBodyLimitRejection_NoSideEffectOnRequest", conformBodyLimitRejectionNoSideEffect},
		{"RecordBodyLimitRejection_KeyIsolation", conformBodyLimitRejectionKeyIsolation},
	}
	// Subtests run serially, not in parallel: each case is a sub-millisecond
	// in-memory counting check, so parallelism yields no wall-clock benefit and
	// only widens the surface for harness/observer state races. Serial execution
	// makes the suite deterministic.
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, h)
		})
	}
}

// conformRecordRequestCount verifies RecordRequest increments per unique key.
func conformRecordRequestCount(t *testing.T, h CollectorHarness) {
	t.Helper()
	col, obs := h.New(t)

	ctx := context.Background()
	col.RecordRequest(ctx, Label("accesscore"), "GET", testSessionsRoute, 200, 0.01)
	col.RecordRequest(ctx, Label("accesscore"), "GET", testSessionsRoute, 200, 0.02)

	key := RequestKey{Cell: "accesscore", Method: "GET", Route: testSessionsRoute, Status: 200}
	got := obs.RequestCount(key)
	if got != 2 {
		t.Errorf("RecordRequest count: got %d, want 2 (two calls same key)", got)
	}
}

// conformRecordRequestLabelIsolation verifies that cell and route labels are
// independent: two calls with different cell values must not collapse into one
// counter entry.
func conformRecordRequestLabelIsolation(t *testing.T, h CollectorHarness) {
	t.Helper()
	col, obs := h.New(t)

	ctx := context.Background()
	col.RecordRequest(ctx, Label("accesscore"), "GET", testSessionsRoute, 200, 0.01)
	col.RecordRequest(ctx, Label("auditcore"), "GET", testSessionsRoute, 200, 0.01)

	key1 := RequestKey{Cell: "accesscore", Method: "GET", Route: testSessionsRoute, Status: 200}
	key2 := RequestKey{Cell: "auditcore", Method: "GET", Route: testSessionsRoute, Status: 200}
	if got := obs.RequestCount(key1); got != 1 {
		t.Errorf("accesscore count: got %d, want 1", got)
	}
	if got := obs.RequestCount(key2); got != 1 {
		t.Errorf("auditcore count: got %d, want 1", got)
	}
}

// conformBodyLimitRejectionCount verifies RecordBodyLimitRejection increments
// the body-limit rejection counter.
func conformBodyLimitRejectionCount(t *testing.T, h CollectorHarness) {
	t.Helper()
	col, obs := h.New(t)

	ctx := context.Background()
	col.RecordBodyLimitRejection(ctx, Label("accesscore"), testUploadRoute)
	col.RecordBodyLimitRejection(ctx, Label("accesscore"), testUploadRoute)

	key := BodyLimitRejectionKey{Cell: "accesscore", Route: testUploadRoute}
	if got := obs.BodyLimitRejectionCount(key); got != 2 {
		t.Errorf("BodyLimitRejection count: got %d, want 2", got)
	}
}

// conformBodyLimitRejectionCtxForwarded verifies the caller ctx is forwarded
// to the underlying instrument. Skipped when the harness returns nil from
// LastCtxForBodyLimitRejection (implementation cannot observe ctx).
func conformBodyLimitRejectionCtxForwarded(t *testing.T, h CollectorHarness) {
	t.Helper()
	col, obs := h.New(t)

	type sentinelKey struct{}
	want := "sentinel-ctx-value"
	ctx := context.WithValue(context.Background(), sentinelKey{}, want)
	col.RecordBodyLimitRejection(ctx, Label("accesscore"), testUploadRoute)

	lastCtx := obs.LastCtxForBodyLimitRejection()
	if lastCtx == nil {
		t.Skip("harness cannot observe forwarded ctx — skipping ctx-forwarding assertion")
	}
	got, _ := lastCtx.Value(sentinelKey{}).(string)
	if got != want {
		t.Errorf("forwarded ctx value = %q, want %q (implementation substituted a different ctx — exemplar/baggage lost)", got, want)
	}
}

// conformBodyLimitRejectionNoSideEffect verifies RecordBodyLimitRejection does
// not pollute the RecordRequest counter series.
func conformBodyLimitRejectionNoSideEffect(t *testing.T, h CollectorHarness) {
	t.Helper()
	col, obs := h.New(t)

	ctx := context.Background()
	col.RecordBodyLimitRejection(ctx, RuntimeLabel(), "unmatched")

	// The request counter for the same (cell, route) tuple must remain zero.
	// RequestKey has Method and Status in addition; check that no request was
	// recorded by verifying all common label combinations return 0.
	for _, status := range []int{200, 413, 500} {
		for _, method := range []string{"GET", "POST"} {
			key := RequestKey{Cell: "_runtime", Method: method, Route: "unmatched", Status: status}
			if got := obs.RequestCount(key); got != 0 {
				t.Errorf("RecordBodyLimitRejection polluted request counter at %+v: got %d, want 0", key, got)
			}
		}
	}
}

// conformBodyLimitRejectionKeyIsolation verifies the body-limit counter is
// keyed per (cell, route): two distinct keys must not affect each other.
func conformBodyLimitRejectionKeyIsolation(t *testing.T, h CollectorHarness) {
	t.Helper()
	col, obs := h.New(t)

	ctx := context.Background()
	col.RecordBodyLimitRejection(ctx, Label("accesscore"), testUploadRoute)
	col.RecordBodyLimitRejection(ctx, Label("configcore"), "/api/v1/config")

	key1 := BodyLimitRejectionKey{Cell: "accesscore", Route: testUploadRoute}
	key2 := BodyLimitRejectionKey{Cell: "configcore", Route: "/api/v1/config"}
	if got := obs.BodyLimitRejectionCount(key1); got != 1 {
		t.Errorf("accesscore/upload count: got %d, want 1", got)
	}
	if got := obs.BodyLimitRejectionCount(key2); got != 1 {
		t.Errorf("configcore/config count: got %d, want 1", got)
	}
}
