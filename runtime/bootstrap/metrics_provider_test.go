package bootstrap

import (
	"testing"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"

	"github.com/ghbvf/gocell/kernel/clock"
)

func TestWithMetricsProvider_NopDefault(t *testing.T) {
	b := New(WithClock(clock.Real()))
	p := b.MetricsProvider()
	if p == nil {
		t.Fatal("MetricsProvider must never return nil")
	}
	if _, ok := p.(kernelmetrics.NopProvider); !ok {
		t.Fatalf("default provider must be NopProvider, got %T", p)
	}
}

func TestWithMetricsProvider_StoresValue(t *testing.T) {
	custom := &recordingProvider{}
	b := New(WithClock(clock.Real()), WithMetricsProvider(custom))
	if b.MetricsProvider() != custom {
		t.Fatalf("MetricsProvider did not store the injected provider")
	}
}

func TestWithMetricsProvider_NilRetainsDefault(t *testing.T) {
	b := New(WithClock(clock.Real()), WithMetricsProvider(nil))
	if _, ok := b.MetricsProvider().(kernelmetrics.NopProvider); !ok {
		t.Fatalf("nil provider must keep the NopProvider default, got %T", b.MetricsProvider())
	}
}

// recordingProvider is a compile-time-checked Provider fixture (records
// nothing useful beyond identity).
type recordingProvider struct{}

func (recordingProvider) CounterVec(_ kernelmetrics.CounterOpts) (kernelmetrics.CounterVec, error) {
	return kernelmetrics.NopProvider{}.CounterVec(kernelmetrics.CounterOpts{})
}

func (recordingProvider) HistogramVec(_ kernelmetrics.HistogramOpts) (kernelmetrics.HistogramVec, error) {
	return kernelmetrics.NopProvider{}.HistogramVec(kernelmetrics.HistogramOpts{})
}
func (recordingProvider) Unregister(_ kernelmetrics.Collector) error { return nil }

var _ kernelmetrics.Provider = recordingProvider{}
