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
	c.RecordRequest("accesscore", http.MethodGet, "/api", 200, 0.05)
	c.RecordRequest("accesscore", http.MethodGet, "/api", 200, 0.03)
	c.RecordRequest("accesscore", http.MethodPost, "/api", 201, 0.1)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, req)

	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	entries, ok := result["data"].([]any)
	require.True(t, ok, "Handler must wrap list payloads under the unified \"data\" key")
	assert.Len(t, entries, 2) // GET /api 200 and POST /api 201
}

func TestInMemoryCollector_Snapshot(t *testing.T) {
	c := NewInMemoryCollector()
	c.RecordRequest("accesscore", "GET", "/a", 200, 0.001)
	c.RecordRequest("accesscore", "GET", "/a", 200, 0.002)

	snap := c.Snapshot()
	assert.Equal(t, int64(2), snap.RequestCounts["accesscore GET /a 200"])
	assert.True(t, snap.DurationSumsMs["accesscore GET /a 200"] >= 0)
}

func TestInMemoryCollector_PerCellSeparation(t *testing.T) {
	c := NewInMemoryCollector()
	c.RecordRequest("accesscore", "GET", "/api/v1/sessions", 200, 0.001)
	c.RecordRequest("auditcore", "GET", "/api/v1/sessions", 200, 0.002)

	snap := c.Snapshot()
	assert.Equal(t, int64(1), snap.RequestCounts["accesscore GET /api/v1/sessions 200"])
	assert.Equal(t, int64(1), snap.RequestCounts["auditcore GET /api/v1/sessions 200"])
}

// Verify interface compliance at compile time.
var _ Collector = (*InMemoryCollector)(nil)
