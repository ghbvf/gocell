package metrics

import (
	"context"
	"fmt"
	"sync"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// GRPCCollector records gRPC unary server request metrics. It mirrors the HTTP
// Collector but emits a distinct metric family (grpc_server_*) with a
// gRPC-shaped label set: method (the full RPC method, e.g.
// "/pkg.Service/Method"), code (the gRPC status code name, e.g. "OK" /
// "Internal"), and cell (the coarse owner dimension, "_runtime" until cell
// attribution is wired for gRPC).
//
// cellID is supplied by the caller; the collector never infers it. Use the
// owning cell ID once gRPC cell attribution lands, or RuntimeCellIDSentinel
// ("_runtime") for framework / unattributed traffic.
type GRPCCollector interface {
	RecordRPC(ctx context.Context, cellID, method, code string, durationSeconds float64)
}

// grpcProviderCollector implements GRPCCollector on top of a provider-neutral
// metrics.Provider, mirroring providerCollector for the HTTP surface.
type grpcProviderCollector struct {
	requests kernelmetrics.CounterVec
	duration kernelmetrics.HistogramVec
}

var _ GRPCCollector = (*grpcProviderCollector)(nil)

// NewGRPCProviderCollector builds a GRPCCollector that records through a
// kernel-level metrics.Provider (Prometheus-backed, OTel-backed, Nop, …).
// DurationBuckets default to DefaultDurationBuckets, shared with the HTTP
// collector so dashboards use a uniform bucket layout across transports.
func NewGRPCProviderCollector(p kernelmetrics.Provider, cfg ProviderCollectorConfig) (GRPCCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: Provider is required")
	}
	if len(cfg.DurationBuckets) == 0 {
		cfg.DurationBuckets = DefaultDurationBuckets
	}

	reqs, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       "grpc_server_requests_total",
		Help:       "Total number of gRPC unary server requests.",
		LabelNames: []string{"method", "code", "cell"},
	})
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register grpc_server_requests_total "+
			"(likely duplicate — another collector on this Provider may already own the name): %w", err)
	}
	dur, err := p.HistogramVec(kernelmetrics.HistogramOpts{
		Name:       "grpc_server_request_duration_seconds",
		Help:       "gRPC unary server request duration in seconds.",
		LabelNames: []string{"method", "code", "cell"},
		Buckets:    cfg.DurationBuckets,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register grpc_server_request_duration_seconds "+
			"(likely duplicate — another collector on this Provider may already own the name): %w", err)
	}

	return &grpcProviderCollector{requests: reqs, duration: dur}, nil
}

// RecordRPC emits an increment on grpc_server_requests_total and a sample on
// grpc_server_request_duration_seconds, labeled identically.
func (c *grpcProviderCollector) RecordRPC(ctx context.Context, cellID, method, code string, durationSeconds float64) {
	labels := kernelmetrics.Labels{
		"method": method,
		"code":   code,
		"cell":   cellID,
	}
	c.requests.With(labels).Inc(ctx)
	c.duration.With(labels).Observe(ctx, durationSeconds)
}

// GRPCRequestKey identifies one low-cardinality gRPC request metric series.
type GRPCRequestKey struct {
	Cell   string
	Method string
	Code   string
}

// InMemoryGRPCCollector is a simple in-memory GRPCCollector for development and
// testing. It records request counts and cumulative duration per label set.
type InMemoryGRPCCollector struct {
	mu             sync.Mutex
	counts         map[GRPCRequestKey]int64
	durationMicros map[GRPCRequestKey]int64
}

// NewInMemoryGRPCCollector creates an InMemoryGRPCCollector.
func NewInMemoryGRPCCollector() *InMemoryGRPCCollector {
	return &InMemoryGRPCCollector{
		counts:         make(map[GRPCRequestKey]int64),
		durationMicros: make(map[GRPCRequestKey]int64),
	}
}

// RecordRPC records a completed gRPC unary request.
func (c *InMemoryGRPCCollector) RecordRPC(_ context.Context, cellID, method, code string, durationSeconds float64) {
	key := GRPCRequestKey{Cell: cellID, Method: method, Code: code}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[key]++
	c.durationMicros[key] += int64(durationSeconds * 1e6)
}

// Count returns the number of recorded requests for the given label set.
func (c *InMemoryGRPCCollector) Count(cellID, method, code string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[GRPCRequestKey{Cell: cellID, Method: method, Code: code}]
}
