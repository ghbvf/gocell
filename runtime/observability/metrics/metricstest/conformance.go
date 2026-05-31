// Package metricstest provides a shared conformance suite for implementations
// of [metrics.Collector]. Any implementation — InMemoryCollector (dev/test) or
// providerCollector (production Prometheus/OTel-backed) — runs
// RunCollectorConformance to verify the shared recording contract.
//
// Because Collector is a write-only sink and the two production implementations
// have different observation mechanisms (InMemoryCollector.Snapshot vs. spy
// counter on a provider), the suite is parameterised through CollectorHarness:
// each implementation supplies its own New() constructor and read-back helpers.
//
// The suite file is a plain .go file (not _test.go) so it can be imported by
// _test.go files in other packages without being stripped from the build graph
// (mirrors the sagajournaltest pattern).
package metricstest

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// RequestKey mirrors metrics.RequestKey for harness observation without
// requiring the test-side to import the internal key type directly.
type RequestKey = metrics.RequestKey

// BodyLimitRejectionKey mirrors metrics.BodyLimitRejectionKey.
type BodyLimitRejectionKey = metrics.BodyLimitRejectionKey

// CollectorHarness wraps one Collector implementation with the observation
// surface the conformance suite needs. Each implementation provides:
//
//   - New: builds a fresh, isolated Collector instance. Called once per subtest.
//   - RequestCount: returns how many times RecordRequest was called for the
//     given key on the last collector produced by New. Returns 0 if the key has
//     not been recorded.
//   - BodyLimitRejectionCount: returns how many times RecordBodyLimitRejection
//     was called for the given key. Returns 0 if never recorded.
//   - LastCtxForBodyLimitRejection: returns the context that was passed to the
//     most recent RecordBodyLimitRejection call, or nil if no call was made.
//     Implementations that cannot observe ctx may return nil; the corresponding
//     harness subtest will skip the ctx-forwarding assertion.
type CollectorHarness interface {
	// New constructs a fresh Collector instance for one subtest. The returned
	// Collector is the target of all subsequent Read-side observations until
	// the next New call.
	New(t *testing.T) metrics.Collector

	// RequestCount returns the accumulated count for the given key on the
	// last Collector created by New.
	RequestCount(key RequestKey) int64

	// BodyLimitRejectionCount returns the accumulated count for the given
	// key on the last Collector created by New.
	BodyLimitRejectionCount(key BodyLimitRejectionKey) int64

	// LastCtxForBodyLimitRejection returns the context.Context that was
	// supplied to the most recent RecordBodyLimitRejection call, or nil if
	// no call was made or the implementation cannot observe the ctx. A nil
	// return causes the ctx-forwarding assertion to be skipped.
	LastCtxForBodyLimitRejection() context.Context
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
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t, h)
		})
	}
}

// conformRecordRequestCount verifies RecordRequest increments per unique key.
func conformRecordRequestCount(t *testing.T, h CollectorHarness) {
	t.Helper()
	col := h.New(t)

	ctx := context.Background()
	col.RecordRequest(ctx, "accesscore", "GET", "/api/v1/sessions", 200, 0.01)
	col.RecordRequest(ctx, "accesscore", "GET", "/api/v1/sessions", 200, 0.02)

	key := RequestKey{Cell: "accesscore", Method: "GET", Route: "/api/v1/sessions", Status: 200}
	got := h.RequestCount(key)
	if got != 2 {
		t.Errorf("RecordRequest count: got %d, want 2 (two calls same key)", got)
	}
}

// conformRecordRequestLabelIsolation verifies that cell and route labels are
// independent: two calls with different cell values must not collapse into one
// counter entry.
func conformRecordRequestLabelIsolation(t *testing.T, h CollectorHarness) {
	t.Helper()
	col := h.New(t)

	ctx := context.Background()
	col.RecordRequest(ctx, "accesscore", "GET", "/api/v1/sessions", 200, 0.01)
	col.RecordRequest(ctx, "auditcore", "GET", "/api/v1/sessions", 200, 0.01)

	key1 := RequestKey{Cell: "accesscore", Method: "GET", Route: "/api/v1/sessions", Status: 200}
	key2 := RequestKey{Cell: "auditcore", Method: "GET", Route: "/api/v1/sessions", Status: 200}
	if got := h.RequestCount(key1); got != 1 {
		t.Errorf("accesscore count: got %d, want 1", got)
	}
	if got := h.RequestCount(key2); got != 1 {
		t.Errorf("auditcore count: got %d, want 1", got)
	}
}

// conformBodyLimitRejectionCount verifies RecordBodyLimitRejection increments
// the body-limit rejection counter.
func conformBodyLimitRejectionCount(t *testing.T, h CollectorHarness) {
	t.Helper()
	col := h.New(t)

	ctx := context.Background()
	col.RecordBodyLimitRejection(ctx, "accesscore", "/api/v1/upload")
	col.RecordBodyLimitRejection(ctx, "accesscore", "/api/v1/upload")

	key := BodyLimitRejectionKey{Cell: "accesscore", Route: "/api/v1/upload"}
	if got := h.BodyLimitRejectionCount(key); got != 2 {
		t.Errorf("BodyLimitRejection count: got %d, want 2", got)
	}
}

// conformBodyLimitRejectionCtxForwarded verifies the caller ctx is forwarded
// to the underlying instrument. Skipped when the harness returns nil from
// LastCtxForBodyLimitRejection (implementation cannot observe ctx).
func conformBodyLimitRejectionCtxForwarded(t *testing.T, h CollectorHarness) {
	t.Helper()
	col := h.New(t)

	type sentinelKey struct{}
	want := "sentinel-ctx-value"
	ctx := context.WithValue(context.Background(), sentinelKey{}, want)
	col.RecordBodyLimitRejection(ctx, "accesscore", "/api/v1/upload")

	lastCtx := h.LastCtxForBodyLimitRejection()
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
	col := h.New(t)

	_ = col
	ctx := context.Background()
	col.RecordBodyLimitRejection(ctx, "_runtime", "unmatched")

	// The request counter for the same (cell, route) tuple must remain zero.
	// RequestKey has Method and Status in addition; check that no request was
	// recorded by verifying all common label combinations return 0.
	for _, status := range []int{200, 413, 500} {
		for _, method := range []string{"GET", "POST"} {
			key := RequestKey{Cell: "_runtime", Method: method, Route: "unmatched", Status: status}
			if got := h.RequestCount(key); got != 0 {
				t.Errorf("RecordBodyLimitRejection polluted request counter at %+v: got %d, want 0", key, got)
			}
		}
	}
}

// conformBodyLimitRejectionKeyIsolation verifies the body-limit counter is
// keyed per (cell, route): two distinct keys must not affect each other.
func conformBodyLimitRejectionKeyIsolation(t *testing.T, h CollectorHarness) {
	t.Helper()
	col := h.New(t)

	ctx := context.Background()
	col.RecordBodyLimitRejection(ctx, "accesscore", "/api/v1/upload")
	col.RecordBodyLimitRejection(ctx, "configcore", "/api/v1/config")

	key1 := BodyLimitRejectionKey{Cell: "accesscore", Route: "/api/v1/upload"}
	key2 := BodyLimitRejectionKey{Cell: "configcore", Route: "/api/v1/config"}
	if got := h.BodyLimitRejectionCount(key1); got != 1 {
		t.Errorf("accesscore/upload count: got %d, want 1", got)
	}
	if got := h.BodyLimitRejectionCount(key2); got != 1 {
		t.Errorf("configcore/config count: got %d, want 1", got)
	}
}
