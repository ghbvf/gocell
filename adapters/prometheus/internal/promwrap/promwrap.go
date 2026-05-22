// Package promwrap is the sole sanctioned funnel for constructing Prometheus
// client_golang instruments. Production code outside this package MUST NEVER
// call prom.New* constructors directly.
//
// INVARIANT: METRICS-GAUGEVEC-UPSTREAM-HARD-01
//   - upstream Hard: Go internal/ package closure prevents external imports
//     at compile time; only adapters/prometheus/* subtree can import.
//   - downstream Hard: archtest METRICS-GAUGEVEC-FUNNEL-01 enforces callee
//     form-uniqueness on prom.New* via *types.Info resolution.
//
// ref: prometheus/client_golang promauto/auto.go (separate-package wrap precedent)
package promwrap

import prom "github.com/prometheus/client_golang/prometheus"

// NewCounter is the sole sanctioned constructor for a Prometheus Counter.
func NewCounter(opts prom.CounterOpts) prom.Counter { return prom.NewCounter(opts) }

// NewCounterVec is the sole sanctioned constructor for a Prometheus CounterVec.
func NewCounterVec(opts prom.CounterOpts, labelNames []string) *prom.CounterVec {
	return prom.NewCounterVec(opts, labelNames)
}

// NewGaugeVec is the sole sanctioned constructor for a Prometheus GaugeVec.
func NewGaugeVec(opts prom.GaugeOpts, labelNames []string) *prom.GaugeVec {
	return prom.NewGaugeVec(opts, labelNames)
}

// NewHistogramVec is the sole sanctioned constructor for a Prometheus HistogramVec.
func NewHistogramVec(opts prom.HistogramOpts, labelNames []string) *prom.HistogramVec {
	return prom.NewHistogramVec(opts, labelNames)
}

// NewGauge is the sole sanctioned constructor for a bare Prometheus Gauge.
func NewGauge(opts prom.GaugeOpts) prom.Gauge { return prom.NewGauge(opts) }

// NewGaugeFunc is the sole sanctioned constructor for a callback-style Gauge.
func NewGaugeFunc(opts prom.GaugeOpts, function func() float64) prom.GaugeFunc {
	return prom.NewGaugeFunc(opts, function)
}
