package metrics

import "context"

// NopProvider is a no-op Provider used when no metrics backend is injected.
// It validates label sets (so label-drift bugs surface in test) but records
// nothing.
//
// ref: opentelemetry-go metric/noop/noop.go@main — null-object pattern.
// GoCell's Nop still enforces label validation so a misbehaving caller is
// caught in unit tests whose wire has not yet been extended with a real
// Provider, preventing silent drift into production.
type NopProvider struct{}

// CounterVec returns a no-op CounterVec that still enforces label
// correctness at With() time.
func (NopProvider) CounterVec(opts CounterOpts) (CounterVec, error) {
	return nopCounterVec{labels: append([]string(nil), opts.LabelNames...)}, nil
}

// HistogramVec returns a no-op HistogramVec that still enforces label
// correctness at With() time.
func (NopProvider) HistogramVec(opts HistogramOpts) (HistogramVec, error) {
	return nopHistogramVec{labels: append([]string(nil), opts.LabelNames...)}, nil
}

// GaugeVec returns a no-op GaugeVec that still enforces label correctness
// at With() time.
func (NopProvider) GaugeVec(opts GaugeOpts) (GaugeVec, error) {
	return nopGaugeVec{labels: append([]string(nil), opts.LabelNames...)}, nil
}

// IsReal reports whether p actually records metrics — i.e. non-nil and not the
// NopProvider sentinel. It is the single source for the "wire instrumentation
// only when a real metrics backend is configured" decision, shared by the HTTP
// bootstrap path (Bootstrap.hasRealMetricsProvider) and the gRPC interceptor
// chain (interceptor.NewServerInterceptors PDP-metrics wrap, #2008 F8): both
// gate metric autowiring identically so a Nop provider never builds a no-op
// decorator that bypasses the metric-autowire funnel.
func IsReal(p Provider) bool {
	if p == nil {
		return false
	}
	_, isNop := p.(NopProvider)
	return !isNop
}

type nopCounterVec struct{ labels []string }

func (v nopCounterVec) Registered() bool { return true }
func (v nopCounterVec) With(l Labels) Counter {
	MustValidateLabels(v.labels, l)
	return nopCounter{}
}

type nopHistogramVec struct{ labels []string }

func (v nopHistogramVec) Registered() bool { return true }
func (v nopHistogramVec) With(l Labels) Histogram {
	MustValidateLabels(v.labels, l)
	return nopHistogram{}
}

type nopGaugeVec struct{ labels []string }

func (v nopGaugeVec) Registered() bool { return true }
func (v nopGaugeVec) With(l Labels) Gauge {
	MustValidateLabels(v.labels, l)
	return nopGauge{}
}

type nopCounter struct{}

func (nopCounter) Inc(_ context.Context)            {}
func (nopCounter) Add(_ context.Context, _ float64) {}

type nopHistogram struct{}

func (nopHistogram) Observe(_ context.Context, _ float64) {}

// nopGauge is the no-op Gauge returned by NopProvider. All mutating methods are
// deliberate no-ops: NopProvider implements the Provider contract without
// recording anything, so any Gauge obtained from it silently discards writes.
type nopGauge struct{}

// Set is a no-op; see nopGauge.
func (nopGauge) Set(_ context.Context, _ float64) {}

// Inc is a no-op; see nopGauge.
func (nopGauge) Inc(_ context.Context) {}

// Dec is a no-op; see nopGauge.
func (nopGauge) Dec(_ context.Context) {}

// Add is a no-op; see nopGauge.
func (nopGauge) Add(_ context.Context, _ float64) {}
