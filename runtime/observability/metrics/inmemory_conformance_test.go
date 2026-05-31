package metrics_test

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/observability/metrics/metricstest"
)

// TestInMemoryCollector_Conformance runs the shared Collector conformance
// suite against InMemoryCollector. This satisfies the enrollment requirement
// checked by COLLECTOR-CONFORMANCE-ENROLLMENT-01.
func TestInMemoryCollector_Conformance(t *testing.T) {
	metricstest.RunCollectorConformance(t, &inMemoryHarness{})
}

// inMemoryHarness adapts InMemoryCollector to metricstest.CollectorHarness.
// Snapshot() provides the observation surface.
type inMemoryHarness struct {
	col *metrics.InMemoryCollector
}

func (h *inMemoryHarness) New(_ *testing.T) metrics.Collector {
	h.col = metrics.NewInMemoryCollector()
	return h.col
}

func (h *inMemoryHarness) RequestCount(key metricstest.RequestKey) int64 {
	snap := h.col.Snapshot()
	return snap.RequestCounts[key]
}

func (h *inMemoryHarness) BodyLimitRejectionCount(key metricstest.BodyLimitRejectionKey) int64 {
	snap := h.col.Snapshot()
	return snap.BodyLimitRejections[key]
}

// LastCtxForBodyLimitRejection — InMemoryCollector discards the ctx (it only
// stores counts), so return nil to skip the ctx-forwarding assertion.
func (h *inMemoryHarness) LastCtxForBodyLimitRejection() context.Context {
	return nil
}
