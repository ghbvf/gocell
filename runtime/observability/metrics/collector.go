// Package metrics provides HTTP request instrumentation interfaces and an
// in-memory implementation. Production deployments wire
// NewProviderCollector against a kernel/observability/metrics.Provider
// (backed by adapters/prometheus or adapters/otel); InMemoryCollector is
// retained for dev / tests that want an observable collector without
// reaching for a Provider.
package metrics

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
)

// Collector records HTTP request metrics.
type Collector interface {
	// RecordRequest records a completed HTTP request with the given labels.
	// route is the route pattern (e.g. "/api/v1/users/{id}"), not the actual
	// request path. Using route patterns prevents metric cardinality explosion.
	//
	// cellID is the coarse owner dimension for the request. It is supplied by
	// the caller, normally runtime/http/router's root CellAttribution
	// middleware, from RouteGroup ownership before protection middleware can
	// short-circuit. It is not inferred by the collector from assembly,
	// config, route path, tenant, slice, or contract metadata.
	//
	// Use the owning cell ID for cell-owned RouteGroups, or "_runtime" for
	// framework-owned paths (healthz/readyz/metrics, unmatched 404s, listeners
	// with no business RouteGroup attached).
	RecordRequest(ctx context.Context, cellID, method, route string, status int, durationSeconds float64)

	// RecordBodyLimitRejection increments the body-limit rejection counter for
	// the given cell and route. It is called only on the Content-Length
	// fast-path reject (r.ContentLength > maxBytes before the request body is
	// read). Streaming overruns after MaxBytesReader kicks in are already
	// captured by http_requests_total{status=413} via RecordRequest.
	//
	// cellID follows the same semantics as RecordRequest: use the owning cell
	// ID or RuntimeCellIDSentinel ("_runtime") for framework paths.
	// route is the low-cardinality route pattern from RouteFor.
	RecordBodyLimitRejection(ctx context.Context, cellID, route string)
}

// RequestKey identifies one low-cardinality HTTP request metric series.
type RequestKey struct {
	Cell   string
	Method string
	Route  string
	Status int
}

// BodyLimitRejectionKey identifies one body-limit rejection metric series.
type BodyLimitRejectionKey struct {
	Cell  string
	Route string
}

// Snapshot is a point-in-time view of recorded metrics.
type Snapshot struct {
	RequestCounts       map[RequestKey]int64
	DurationSumsMs      map[RequestKey]int64
	BodyLimitRejections map[BodyLimitRejectionKey]int64
}

// InMemoryCollector is a simple in-memory metrics collector for development
// and testing. It records request counts, cumulative duration, and body-limit
// rejection counts.
type InMemoryCollector struct {
	mu                  sync.RWMutex
	counts              map[RequestKey]*atomic.Int64
	durations           map[RequestKey]*atomic.Int64 // cumulative duration in microseconds
	bodyLimitRejections map[BodyLimitRejectionKey]*atomic.Int64
}

// Compile-time interface compliance check.
var _ Collector = (*InMemoryCollector)(nil)

// NewInMemoryCollector creates an InMemoryCollector.
func NewInMemoryCollector() *InMemoryCollector {
	return &InMemoryCollector{
		counts:              make(map[RequestKey]*atomic.Int64),
		durations:           make(map[RequestKey]*atomic.Int64),
		bodyLimitRejections: make(map[BodyLimitRejectionKey]*atomic.Int64),
	}
}

func metricKey(cellID, method, route string, status int) RequestKey {
	return RequestKey{Cell: cellID, Method: method, Route: route, Status: status}
}

// RecordRequest records a completed HTTP request.
func (c *InMemoryCollector) RecordRequest(_ context.Context, cellID, method, route string, status int, durationSeconds float64) {
	key := metricKey(cellID, method, route, status)

	c.mu.RLock()
	cnt, cntOK := c.counts[key]
	dur, durOK := c.durations[key]
	c.mu.RUnlock()

	if !cntOK || !durOK {
		c.mu.Lock()
		if _, ok := c.counts[key]; !ok {
			c.counts[key] = &atomic.Int64{}
		}
		cnt = c.counts[key]
		if _, ok := c.durations[key]; !ok {
			c.durations[key] = &atomic.Int64{}
		}
		dur = c.durations[key]
		c.mu.Unlock()
	}

	cnt.Add(1)
	dur.Add(int64(durationSeconds * 1e6)) // microseconds
}

// RecordBodyLimitRejection increments the body-limit rejection counter for the
// given cell and route. Mirrors the RLock fast-path + Lock lazy-init pattern
// of RecordRequest.
func (c *InMemoryCollector) RecordBodyLimitRejection(_ context.Context, cellID, route string) {
	key := BodyLimitRejectionKey{Cell: cellID, Route: route}

	c.mu.RLock()
	ctr, ok := c.bodyLimitRejections[key]
	c.mu.RUnlock()

	if !ok {
		c.mu.Lock()
		if _, exists := c.bodyLimitRejections[key]; !exists {
			c.bodyLimitRejections[key] = &atomic.Int64{}
		}
		ctr = c.bodyLimitRejections[key]
		c.mu.Unlock()
	}

	ctr.Add(1)
}

// Snapshot returns a point-in-time copy of all metrics.
func (c *InMemoryCollector) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()

	snap := Snapshot{
		RequestCounts:       make(map[RequestKey]int64, len(c.counts)),
		DurationSumsMs:      make(map[RequestKey]int64, len(c.durations)),
		BodyLimitRejections: make(map[BodyLimitRejectionKey]int64, len(c.bodyLimitRejections)),
	}
	for k, v := range c.counts {
		snap.RequestCounts[k] = v.Load()
	}
	for k, v := range c.durations {
		snap.DurationSumsMs[k] = v.Load() / 1000 // microseconds → milliseconds
	}
	for k, v := range c.bodyLimitRejections {
		snap.BodyLimitRejections[k] = v.Load()
	}
	return snap
}

// handlerRequestEntry is the JSON shape for a single request metric entry.
type handlerRequestEntry struct {
	Cell       string `json:"cell"`
	Method     string `json:"method"`
	Route      string `json:"route"`
	Status     int    `json:"status"`
	Count      int64  `json:"count"`
	DurationMs int64  `json:"duration_sum_ms"`
}

// handlerBodyLimitEntry is the JSON shape for a single body-limit rejection entry.
type handlerBodyLimitEntry struct {
	Cell  string `json:"cell"`
	Route string `json:"route"`
	Count int64  `json:"count"`
}

// buildHandlerPayload converts a Snapshot into sorted JSON-serialisable slices.
func buildHandlerPayload(snap Snapshot) ([]handlerRequestEntry, []handlerBodyLimitEntry) {
	requests := make([]handlerRequestEntry, 0, len(snap.RequestCounts))
	for key, count := range snap.RequestCounts {
		requests = append(requests, handlerRequestEntry{
			Cell:       key.Cell,
			Method:     key.Method,
			Route:      key.Route,
			Status:     key.Status,
			Count:      count,
			DurationMs: snap.DurationSumsMs[key],
		})
	}
	sort.Slice(requests, func(i, j int) bool {
		if requests[i].Cell != requests[j].Cell {
			return requests[i].Cell < requests[j].Cell
		}
		if requests[i].Route != requests[j].Route {
			return requests[i].Route < requests[j].Route
		}
		if requests[i].Method != requests[j].Method {
			return requests[i].Method < requests[j].Method
		}
		return requests[i].Status < requests[j].Status
	})

	rejections := make([]handlerBodyLimitEntry, 0, len(snap.BodyLimitRejections))
	for key, count := range snap.BodyLimitRejections {
		rejections = append(rejections, handlerBodyLimitEntry{
			Cell:  key.Cell,
			Route: key.Route,
			Count: count,
		})
	}
	sort.Slice(rejections, func(i, j int) bool {
		if rejections[i].Cell != rejections[j].Cell {
			return rejections[i].Cell < rejections[j].Cell
		}
		return rejections[i].Route < rejections[j].Route
	})

	return requests, rejections
}

// Handler returns an http.Handler that serves metrics as JSON.
// The response shape is:
//
//	{"data": {"requests": [...], "bodyLimitRejections": [...]}}
//
// requests entries are sorted by cell→route→method→status.
// bodyLimitRejections entries are sorted by cell→route.
//
// collector=nil fields are omitted (nil collector disables body-limit rejection
// recording; streaming 413s are still captured by the Metrics middleware via
// RecordRequest and appear in the requests array).
//
// For Prometheus-compatible output, wire adapters/prometheus.NewMetricProvider
// into NewProviderCollector and serve the registry via promhttp.
func (c *InMemoryCollector) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests, rejections := buildHandlerPayload(c.Snapshot())
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"requests":            requests,
				"bodyLimitRejections": rejections,
			},
		})
	})
}
