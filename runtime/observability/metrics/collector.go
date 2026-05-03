// Package metrics provides HTTP request instrumentation interfaces and an
// in-memory implementation. Production deployments wire
// NewProviderCollector against a kernel/observability/metrics.Provider
// (backed by adapters/prometheus or adapters/otel); InMemoryCollector is
// retained for dev / tests that want an observable collector without
// reaching for a Provider.
package metrics

import (
	"encoding/json"
	"fmt"
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
	// cellID identifies the cell that owns the matched RouteGroup, or
	// "_runtime" for framework-owned paths (healthz/readyz/metrics, unmatched
	// 404s, listeners with no business RouteGroup attached). Bootstrap
	// installs ctx-injecting middleware at both the listener-root and route-
	// group layers so the value is always present at the time middleware
	// invokes RecordRequest; see runtime/http/middleware.WithCellIDContext.
	RecordRequest(cellID, method, route string, status int, durationSeconds float64)
}

// Snapshot is a point-in-time view of recorded metrics.
type Snapshot struct {
	// Key is "method route status" (e.g. "GET /api/v1/users/{id} 200").
	RequestCounts  map[string]int64
	DurationSumsMs map[string]int64
}

// InMemoryCollector is a simple in-memory metrics collector for development
// and testing. It records request counts and cumulative duration.
type InMemoryCollector struct {
	mu        sync.RWMutex
	counts    map[string]*atomic.Int64
	durations map[string]*atomic.Int64 // cumulative duration in microseconds
}

// NewInMemoryCollector creates an InMemoryCollector.
func NewInMemoryCollector() *InMemoryCollector {
	return &InMemoryCollector{
		counts:    make(map[string]*atomic.Int64),
		durations: make(map[string]*atomic.Int64),
	}
}

func metricKey(cellID, method, route string, status int) string {
	return fmt.Sprintf("%s %s %s %d", cellID, method, route, status)
}

// RecordRequest records a completed HTTP request.
func (c *InMemoryCollector) RecordRequest(cellID, method, route string, status int, durationSeconds float64) {
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

// Snapshot returns a point-in-time copy of all metrics.
func (c *InMemoryCollector) Snapshot() Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()

	snap := Snapshot{
		RequestCounts:  make(map[string]int64, len(c.counts)),
		DurationSumsMs: make(map[string]int64, len(c.durations)),
	}
	for k, v := range c.counts {
		snap.RequestCounts[k] = v.Load()
	}
	for k, v := range c.durations {
		snap.DurationSumsMs[k] = v.Load() / 1000 // microseconds → milliseconds
	}
	return snap
}

// Handler returns an http.Handler that serves metrics as JSON.
// For Prometheus-compatible output, wire adapters/prometheus.NewMetricProvider
// into NewProviderCollector and serve the registry via promhttp.
func (c *InMemoryCollector) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := c.Snapshot()

		w.Header().Set("Content-Type", "application/json")

		type entry struct {
			Cell       string `json:"cell"`
			Method     string `json:"method"`
			Route      string `json:"route"`
			Status     int    `json:"status"`
			Count      int64  `json:"count"`
			DurationMs int64  `json:"duration_sum_ms"`
		}

		var entries []entry
		for k, count := range snap.RequestCounts {
			var cell, method, route string
			var status int
			_, _ = fmt.Sscanf(k, "%s %s %s %d", &cell, &method, &route, &status)
			entries = append(entries, entry{
				Cell:       cell,
				Method:     method,
				Route:      route,
				Status:     status,
				Count:      count,
				DurationMs: snap.DurationSumsMs[k],
			})
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].Cell != entries[j].Cell {
				return entries[i].Cell < entries[j].Cell
			}
			return entries[i].Route < entries[j].Route
		})

		// Use the unified list response envelope ({"data": [...]}) consistent
		// with .claude/rules/gocell/api-versioning.md and the other HTTP list
		// endpoints. Inherited "metrics" wrapper would have left this dev/test
		// handler the only inconsistent shape on the metrics surface.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": entries,
		})
	})
}
