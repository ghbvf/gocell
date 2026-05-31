package metrics_test

import (
	"context"
	"strconv"
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/observability/metrics/metricstest"
)

// TestProviderCollector_Conformance runs the shared Collector conformance
// suite against providerCollector (via spyProvider). This satisfies the
// enrollment requirement checked by COLLECTOR-CONFORMANCE-ENROLLMENT-01.
func TestProviderCollector_Conformance(t *testing.T) {
	metricstest.RunCollectorConformance(t, providerCollectorHarness{})
}

// providerCollectorHarness implements metricstest.CollectorHarness for
// providerCollector. It holds no mutable state; each New call returns a
// self-contained (Collector, CollectorObserver) pair backed by a fresh
// spyProvider, so parallel subtests are race-free.
type providerCollectorHarness struct{}

func (providerCollectorHarness) New(t *testing.T) (metrics.Collector, metricstest.CollectorObserver) {
	t.Helper()
	spy := newSpyProvider()
	col, err := metrics.NewProviderCollector(spy, metrics.ProviderCollectorConfig{})
	if err != nil {
		t.Fatalf("providerCollectorHarness.New: %v", err)
	}
	return col, &providerCollectorObserver{spy: spy}
}

// providerCollectorObserver is the read-side for a single providerCollector
// instance. It is bound to one spyProvider and never shared across subtests.
type providerCollectorObserver struct {
	spy *spyProvider
}

func (o *providerCollectorObserver) RequestCount(key metricstest.RequestKey) int64 {
	var total int64
	for _, op := range o.spy.counterOps["http_requests_total"] {
		if matchRequestKey(op.labels, key) {
			total += int64(op.value)
		}
	}
	return total
}

func (o *providerCollectorObserver) BodyLimitRejectionCount(key metricstest.BodyLimitRejectionKey) int64 {
	var total int64
	for _, op := range o.spy.counterOps["http_request_body_limit_rejections_total"] {
		if op.labels["cell"] == key.Cell && op.labels["route"] == key.Route {
			total += int64(op.value)
		}
	}
	return total
}

// LastCtxForBodyLimitRejection returns the ctx from the most recent
// RecordBodyLimitRejection call, proving ctx forwarding to the instrument.
func (o *providerCollectorObserver) LastCtxForBodyLimitRejection() context.Context {
	ops := o.spy.counterOps["http_request_body_limit_rejections_total"]
	if len(ops) == 0 {
		return nil
	}
	return ops[len(ops)-1].ctx
}

// matchRequestKey reports whether a spy counter label set matches the given
// RequestKey. Status is stored as a decimal string in the spy (strconv.Itoa).
func matchRequestKey(labels kernelmetrics.Labels, key metricstest.RequestKey) bool {
	return labels["cell"] == key.Cell &&
		labels["method"] == key.Method &&
		labels["route"] == key.Route &&
		labels["status"] == strconv.Itoa(key.Status)
}
