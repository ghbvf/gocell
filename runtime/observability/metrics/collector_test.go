package metrics

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInMemoryCollector_Handler(t *testing.T) {
	c := NewInMemoryCollector()
	c.RecordRequest("auditcore", http.MethodPost, "/z", 500, 0.004)
	c.RecordRequest("accesscore", http.MethodPost, "/api", 201, 0.1)
	c.RecordRequest("accesscore", http.MethodGet, "/api", 200, 0.05)
	c.RecordRequest("accesscore", http.MethodGet, "/api", 200, 0.03)
	c.RecordRequest("accesscore", http.MethodGet, "/admin", 404, 0.002)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, req)

	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)

	type entry struct {
		Cell       string `json:"cell"`
		Method     string `json:"method"`
		Route      string `json:"route"`
		Status     int    `json:"status"`
		Count      int64  `json:"count"`
		DurationMs int64  `json:"duration_sum_ms"`
	}
	var result struct {
		Data []entry `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Equal(t, []entry{
		{Cell: "accesscore", Method: http.MethodGet, Route: "/admin", Status: 404, Count: 1, DurationMs: 2},
		{Cell: "accesscore", Method: http.MethodGet, Route: "/api", Status: 200, Count: 2, DurationMs: 80},
		{Cell: "accesscore", Method: http.MethodPost, Route: "/api", Status: 201, Count: 1, DurationMs: 100},
		{Cell: "auditcore", Method: http.MethodPost, Route: "/z", Status: 500, Count: 1, DurationMs: 4},
	}, result.Data, "Handler must emit typed request keys sorted by cell, route, method, status")
}

func TestInMemoryCollector_Snapshot(t *testing.T) {
	c := NewInMemoryCollector()
	c.RecordRequest("accesscore", "GET", "/a", 200, 0.001)
	c.RecordRequest("accesscore", "GET", "/a", 200, 0.002)

	snap := c.Snapshot()
	key := RequestKey{Cell: "accesscore", Method: "GET", Route: "/a", Status: 200}
	assert.Equal(t, int64(2), snap.RequestCounts[key])
	assert.True(t, snap.DurationSumsMs[key] >= 0)
}

func TestInMemoryCollector_PerCellSeparation(t *testing.T) {
	c := NewInMemoryCollector()
	c.RecordRequest("accesscore", "GET", "/api/v1/sessions", 200, 0.001)
	c.RecordRequest("auditcore", "GET", "/api/v1/sessions", 200, 0.002)

	snap := c.Snapshot()
	assert.Equal(t, int64(1), snap.RequestCounts[RequestKey{
		Cell: "accesscore", Method: "GET", Route: "/api/v1/sessions", Status: 200,
	}])
	assert.Equal(t, int64(1), snap.RequestCounts[RequestKey{
		Cell: "auditcore", Method: "GET", Route: "/api/v1/sessions", Status: 200,
	}])
}

// Verify interface compliance at compile time.
var _ Collector = (*InMemoryCollector)(nil)
