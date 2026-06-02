package metrics

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
)

// testLabel constructs a CellLabel for white-box tests by routing the id
// through the sole ResolveCellLabel funnel, preserving the sealed-construction
// guarantee. An empty id yields the RuntimeCellSentinel label.
func testLabel(id string) CellLabel {
	if id == "" {
		return ResolveCellLabel(context.Background(), nil)
	}
	return ResolveCellLabel(ctxkeys.WithCellID(context.Background(), id), map[string]struct{}{id: {}})
}

func TestInMemoryCollector_Handler(t *testing.T) {
	ctx := context.Background()
	c := NewInMemoryCollector()
	c.RecordRequest(ctx, testLabel("auditcore"), http.MethodPost, "/z", 500, 0.004)
	c.RecordRequest(ctx, testLabel("accesscore"), http.MethodPost, "/api", 201, 0.1)
	c.RecordRequest(ctx, testLabel("accesscore"), http.MethodGet, "/api", 200, 0.05)
	c.RecordRequest(ctx, testLabel("accesscore"), http.MethodGet, "/api", 200, 0.03)
	c.RecordRequest(ctx, testLabel("accesscore"), http.MethodGet, "/admin", 404, 0.002)
	c.RecordBodyLimitRejection(ctx, testLabel("accesscore"), "/api/v1/upload")
	c.RecordBodyLimitRejection(ctx, testLabel("configcore"), "/api/v1/config")
	c.RecordBodyLimitRejection(ctx, testLabel("configcore"), "/api/v1/config")

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, req)

	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)

	type requestEntry struct {
		Cell       string `json:"cell"`
		Method     string `json:"method"`
		Route      string `json:"route"`
		Status     int    `json:"status"`
		Count      int64  `json:"count"`
		DurationMs int64  `json:"duration_sum_ms"`
	}
	type bodyLimitEntry struct {
		Cell  string `json:"cell"`
		Route string `json:"route"`
		Count int64  `json:"count"`
	}
	var result struct {
		Data struct {
			Requests            []requestEntry   `json:"requests"`
			BodyLimitRejections []bodyLimitEntry `json:"bodyLimitRejections"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Equal(t, []requestEntry{
		{Cell: "accesscore", Method: http.MethodGet, Route: "/admin", Status: 404, Count: 1, DurationMs: 2},
		{Cell: "accesscore", Method: http.MethodGet, Route: "/api", Status: 200, Count: 2, DurationMs: 80},
		{Cell: "accesscore", Method: http.MethodPost, Route: "/api", Status: 201, Count: 1, DurationMs: 100},
		{Cell: "auditcore", Method: http.MethodPost, Route: "/z", Status: 500, Count: 1, DurationMs: 4},
	}, result.Data.Requests, "Handler must emit typed request keys sorted by cell, route, method, status")
	assert.Equal(t, []bodyLimitEntry{
		{Cell: "accesscore", Route: "/api/v1/upload", Count: 1},
		{Cell: "configcore", Route: "/api/v1/config", Count: 2},
	}, result.Data.BodyLimitRejections, "Handler must emit body-limit rejections sorted by cell, route")
}

func TestInMemoryCollector_Snapshot(t *testing.T) {
	ctx := context.Background()
	c := NewInMemoryCollector()
	c.RecordRequest(ctx, testLabel("accesscore"), "GET", "/a", 200, 0.001)
	c.RecordRequest(ctx, testLabel("accesscore"), "GET", "/a", 200, 0.002)

	snap := c.Snapshot()
	key := RequestKey{Cell: "accesscore", Method: "GET", Route: "/a", Status: 200}
	assert.Equal(t, int64(2), snap.RequestCounts[key])
	assert.True(t, snap.DurationSumsMs[key] >= 0)
}

func TestInMemoryCollector_PerCellSeparation(t *testing.T) {
	ctx := context.Background()
	c := NewInMemoryCollector()
	c.RecordRequest(ctx, testLabel("accesscore"), "GET", "/api/v1/sessions", 200, 0.001)
	c.RecordRequest(ctx, testLabel("auditcore"), "GET", "/api/v1/sessions", 200, 0.002)

	snap := c.Snapshot()
	assert.Equal(t, int64(1), snap.RequestCounts[RequestKey{
		Cell: "accesscore", Method: "GET", Route: "/api/v1/sessions", Status: 200,
	}])
	assert.Equal(t, int64(1), snap.RequestCounts[RequestKey{
		Cell: "auditcore", Method: "GET", Route: "/api/v1/sessions", Status: 200,
	}])
}

func TestInMemoryCollector_RecordBodyLimitRejection(t *testing.T) {
	ctx := context.Background()
	c := NewInMemoryCollector()

	c.RecordBodyLimitRejection(ctx, testLabel("accesscore"), "/api/v1/upload")
	c.RecordBodyLimitRejection(ctx, testLabel("accesscore"), "/api/v1/upload")
	c.RecordBodyLimitRejection(ctx, testLabel("configcore"), "/api/v1/config")

	snap := c.Snapshot()
	assert.Equal(t, int64(2), snap.BodyLimitRejections[BodyLimitRejectionKey{
		Cell:  "accesscore",
		Route: "/api/v1/upload",
	}])
	assert.Equal(t, int64(1), snap.BodyLimitRejections[BodyLimitRejectionKey{
		Cell:  "configcore",
		Route: "/api/v1/config",
	}])
}

func TestInMemoryCollector_RecordBodyLimitRejection_NoSideEffectOnRequest(t *testing.T) {
	ctx := context.Background()
	c := NewInMemoryCollector()

	c.RecordBodyLimitRejection(ctx, testLabel("_runtime"), "unmatched")

	snap := c.Snapshot()
	// RecordRequest map must remain empty.
	assert.Empty(t, snap.RequestCounts,
		"body-limit rejection must not affect the request counts map")
}
