package metrics

import (
	"context"
	"fmt"
	"sync"

	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// grpcProtectionMetricName is the metric name for the protection rejection
// counter. Declared as a constant (used ≥ 3 times: registration, help, and
// error message) per the "same string ≥3 → constant" coding rule.
const grpcProtectionMetricName = "grpc_protection_rejected_total"

// RuntimeCellSentinel is the framework "owner" used as the cell label for
// requests that do not belong to any cell-owned namespace. It is the single,
// transport-neutral source of the "_runtime" sentinel: both the HTTP middleware
// (runtime/http/middleware.Metrics / CellAttribution / body-limit rejection) and
// the gRPC metrics interceptor read this constant, so the value is declared
// exactly once. See observability.md "HTTP Metrics cell Label".
const RuntimeCellSentinel = "_runtime"

// GRPCCollector records gRPC unary server request metrics. It mirrors the HTTP
// Collector but emits a distinct metric family (grpc_server_*) with a
// gRPC-shaped label set: method (the full RPC method, e.g.
// "/pkg.Service/Method"), code (the gRPC status code name, e.g. "OK" /
// "Internal"), and cell (the coarse owner dimension).
//
// The method is named RecordRPC (not RecordRequest like the HTTP Collector)
// because the gRPC signature is intentionally distinct — code (a gRPC status
// code name) replaces the HTTP status int + route; the two interfaces are not
// unified.
//
// cell is the sealed [CellLabel] from [ResolveCellLabel]; the collector never
// infers it. gRPC cell attribution is wired (#1383 / #1152): the interceptor
// chain's UnaryCellAttribution writes the owning cell into ctx and UnaryMetrics
// passes ResolveCellLabel(ctx, validCellIDs) (the assembly closed set), so the
// cell reflects the owning cell when registered/in-set and degrades to
// RuntimeCellSentinel ("_runtime") otherwise.
//
// RecordProtectionRejection records one increment on
// grpc_protection_rejected_total{type,method,cell} for a request rejected by a
// protection interceptor (rate-limit deny or circuit-breaker open). ptype must
// be one of the two sealed ProtectionType accessors (ProtectionRateLimit /
// ProtectionCircuit); the caller must resolve cell and method from the
// interceptor's ctx and info before calling. No duration is recorded — rejections
// are cheap enough that a latency histogram adds no actionable signal. See
// GRPC-PROTECTION-REJECTED-TYPE-LABEL-VALUES-FROZEN-01 for the type label freeze.
type GRPCCollector interface {
	RecordRPC(ctx context.Context, cell CellLabel, method, code string, durationSeconds float64)
	RecordProtectionRejection(ctx context.Context, cell CellLabel, method string, ptype ProtectionType)
}

// grpcProviderCollector implements GRPCCollector on top of a provider-neutral
// metrics.Provider, mirroring providerCollector for the HTTP surface.
type grpcProviderCollector struct {
	requests   kernelmetrics.CounterVec
	duration   kernelmetrics.HistogramVec
	protection kernelmetrics.CounterVec
}

var _ GRPCCollector = (*grpcProviderCollector)(nil)

// NewGRPCProviderCollector builds a GRPCCollector that records through a
// kernel-level metrics.Provider (Prometheus-backed, OTel-backed, Nop, …).
// DurationBuckets default to DefaultDurationBuckets, shared with the HTTP
// collector so dashboards use a uniform bucket layout across transports. The
// default's finest bucket is 5ms; intra-mesh RPCs that cluster below that can
// inject finer buckets via ProviderCollectorConfig.DurationBuckets.
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

	prot, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       grpcProtectionMetricName,
		Help:       "Total gRPC requests rejected by a protection interceptor (rate-limit / circuit-breaker).",
		LabelNames: []string{"type", "method", "cell"},
	})
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register %s "+
			"(likely duplicate — another collector on this Provider may already own the name): %w",
			grpcProtectionMetricName, err)
	}

	return &grpcProviderCollector{requests: reqs, duration: dur, protection: prot}, nil
}

// RecordRPC emits an increment on grpc_server_requests_total and a sample on
// grpc_server_request_duration_seconds, labeled identically.
func (c *grpcProviderCollector) RecordRPC(ctx context.Context, cell CellLabel, method, code string, durationSeconds float64) {
	labels := kernelmetrics.Labels{
		"method": method,
		"code":   code,
		"cell":   cell.String(),
	}
	c.requests.With(labels).Inc(ctx)
	c.duration.With(labels).Observe(ctx, durationSeconds)
}

// RecordProtectionRejection emits an increment on grpc_protection_rejected_total
// labeled by type (ProtectionType.String()), method, and cell.
func (c *grpcProviderCollector) RecordProtectionRejection(ctx context.Context, cell CellLabel, method string, ptype ProtectionType) {
	c.protection.With(kernelmetrics.Labels{
		"type":   ptype.String(),
		"method": method,
		"cell":   cell.String(),
	}).Inc(ctx)
}

// GRPCRequestKey identifies one gRPC request metric series. Method holds the
// full RPC method ("/pkg.Service/Method"); its cardinality is bounded because
// gRPC methods are a registration-time enumerated set (unlike an HTTP path,
// which is collapsed to a route template to bound cardinality — gRPC needs no
// such templating).
type GRPCRequestKey struct {
	Cell   string
	Method string
	Code   string
}

// GRPCProtectionKey identifies one grpc_protection_rejected_total metric series.
// Type is the ProtectionType string value ("ratelimit" or "circuit").
type GRPCProtectionKey struct {
	Type   string
	Method string
	Cell   string
}

// InMemoryGRPCCollector is a simple in-memory GRPCCollector for development and
// testing. It records request counts and cumulative duration per label set.
type InMemoryGRPCCollector struct {
	mu               sync.Mutex
	counts           map[GRPCRequestKey]int64
	durationMicros   map[GRPCRequestKey]int64
	protectionCounts map[GRPCProtectionKey]int64
}

// NewInMemoryGRPCCollector creates an InMemoryGRPCCollector.
func NewInMemoryGRPCCollector() *InMemoryGRPCCollector {
	return &InMemoryGRPCCollector{
		counts:           make(map[GRPCRequestKey]int64),
		durationMicros:   make(map[GRPCRequestKey]int64),
		protectionCounts: make(map[GRPCProtectionKey]int64),
	}
}

// RecordRPC records a completed gRPC unary request.
func (c *InMemoryGRPCCollector) RecordRPC(_ context.Context, cell CellLabel, method, code string, durationSeconds float64) {
	key := GRPCRequestKey{Cell: cell.String(), Method: method, Code: code}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[key]++
	c.durationMicros[key] += int64(durationSeconds * 1e6)
}

// RecordProtectionRejection records one increment on
// grpc_protection_rejected_total for the given type/method/cell label set.
func (c *InMemoryGRPCCollector) RecordProtectionRejection(_ context.Context, cell CellLabel, method string, ptype ProtectionType) {
	key := GRPCProtectionKey{Type: ptype.String(), Method: method, Cell: cell.String()}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.protectionCounts[key]++
}

// Count returns the number of recorded requests for the given label set.
func (c *InMemoryGRPCCollector) Count(cellID, method, code string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[GRPCRequestKey{Cell: cellID, Method: method, Code: code}]
}

// ProtectionCount returns the number of recorded protection rejections for the
// given type/method/cell label set. Intended for test assertions only.
func (c *InMemoryGRPCCollector) ProtectionCount(ptype, method, cell string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.protectionCounts[GRPCProtectionKey{Type: ptype, Method: method, Cell: cell}]
}
