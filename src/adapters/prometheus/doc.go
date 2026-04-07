// Package prometheus provides a Prometheus adapter that implements the
// runtime/observability/metrics.Collector interface using the official
// Prometheus client library.
//
// It exposes two metrics:
//   - gocell_http_requests_total (CounterVec): total HTTP requests by method, path, status, cell
//   - gocell_http_request_duration_seconds (HistogramVec): request duration by method, path, status, cell
//
// The Collector also provides a Handler() method that returns an http.Handler
// for the /metrics endpoint, serving metrics in Prometheus exposition format.
//
// ref: github.com/prometheus/client_golang -- Registry, CounterVec, HistogramVec
// Adopted: isolated Registry per Collector, promhttp.HandlerFor exposition.
// Deviated: wrapped behind runtime/metrics.Collector so cells remain
// decoupled from Prometheus imports.
package prometheus
