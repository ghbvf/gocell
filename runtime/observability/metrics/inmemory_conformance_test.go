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
	metricstest.RunCollectorConformance(t, inMemoryHarness{})
}

// inMemoryHarness implements metricstest.CollectorHarness for InMemoryCollector.
// It holds no mutable state; each New call returns a self-contained
// (Collector, CollectorObserver) pair, making parallel subtests race-free.
type inMemoryHarness struct{}

func (inMemoryHarness) New(_ *testing.T) (metrics.Collector, metricstest.CollectorObserver) {
	col := metrics.NewInMemoryCollector()
	return col, &inMemoryObserver{col: col}
}

// inMemoryObserver is the read-side for a single InMemoryCollector instance.
// It is bound to one collector and never shared across subtests.
type inMemoryObserver struct {
	col *metrics.InMemoryCollector
}

func (o *inMemoryObserver) RequestCount(key metricstest.RequestKey) int64 {
	snap := o.col.Snapshot()
	return snap.RequestCounts[key]
}

func (o *inMemoryObserver) BodyLimitRejectionCount(key metricstest.BodyLimitRejectionKey) int64 {
	snap := o.col.Snapshot()
	return snap.BodyLimitRejections[key]
}

// LastCtxForBodyLimitRejection — InMemoryCollector discards the ctx (it only
// stores counts), so return nil to skip the ctx-forwarding assertion.
func (o *inMemoryObserver) LastCtxForBodyLimitRejection() context.Context {
	return nil
}
