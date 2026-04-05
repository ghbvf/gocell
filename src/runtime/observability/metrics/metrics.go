// Package metrics provides HTTP request instrumentation interfaces and an
// in-memory implementation. Production deployments should use an adapter
// (e.g., adapters/prometheus) that implements the Collector interface.
package metrics

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Collector records HTTP request metrics.
type Collector interface {
	// RecordRequest records a completed HTTP request with the given labels.
	RecordRequest(method, path string, status int, durationSeconds float64)
}

// Snapshot is a point-in-time view of recorded metrics.
type Snapshot struct {
	// Key is "method path status".
	RequestCounts    map[string]int64
	DurationSumsMs   map[string]int64
}

// InMemoryCollector is a simple in-memory metrics collector for development
// and testing. It records request counts and cumulative duration.
type InMemoryCollector struct {
	mu       sync.RWMutex
	counts   map[string]*atomic.Int64
	durations map[string]*atomic.Int64 // cumulative duration in microseconds
}

// NewInMemoryCollector creates an InMemoryCollector.
func NewInMemoryCollector() *InMemoryCollector {
	return &InMemoryCollector{
		counts:    make(map[string]*atomic.Int64),
		durations: make(map[string]*atomic.Int64),
	}
}

func metricKey(method, path string, status int) string {
	return fmt.Sprintf("%s %s %d", method, path, status)
}

// RecordRequest records a completed HTTP request.
func (c *InMemoryCollector) RecordRequest(method, path string, status int, durationSeconds float64) {
	key := metricKey(method, path, status)

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
// For Prometheus-compatible output, use adapters/prometheus.
func (c *InMemoryCollector) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snap := c.Snapshot()

		w.Header().Set("Content-Type", "application/json")

		type entry struct {
			Method      string `json:"method"`
			Path        string `json:"path"`
			Status      int    `json:"status"`
			Count       int64  `json:"count"`
			DurationMs  int64  `json:"duration_sum_ms"`
		}

		var entries []entry
		for k, count := range snap.RequestCounts {
			var method, path string
			var status int
			_, _ = fmt.Sscanf(k, "%s %s %d", &method, &path, &status)
			entries = append(entries, entry{
				Method:     method,
				Path:       path,
				Status:     status,
				Count:      count,
				DurationMs: snap.DurationSumsMs[k],
			})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

		_ = json.NewEncoder(w).Encode(map[string]any{
			"metrics": entries,
		})
	})
}

// Middleware returns an HTTP middleware that records request count and duration
// using the provided Collector.
func Middleware(collector Collector) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &metricsRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			duration := time.Since(start).Seconds()
			collector.RecordRequest(r.Method, r.URL.Path, rec.status, duration)
		})
	}
}

// metricsRecorder captures the response status code.
type metricsRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *metricsRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

// Write captures the default 200 status if WriteHeader hasn't been called.
func (r *metricsRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	return r.ResponseWriter.Write(b)
}

// metricsText formats the counter as a Prometheus-like text line for
// debugging/testing. Not a full Prometheus exposition format.
func metricsText(method, path string, status int, count int64) string {
	return fmt.Sprintf("http_requests_total{method=%q,path=%q,status=%q} %d",
		method, path, strconv.Itoa(status), count)
}
