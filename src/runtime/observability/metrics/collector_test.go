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
	c.RecordRequest(http.MethodGet, "/api", 200, 0.05)
	c.RecordRequest(http.MethodGet, "/api", 200, 0.03)
	c.RecordRequest(http.MethodPost, "/api", 201, 0.1)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	c.Handler().ServeHTTP(rec, req)

	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	body, err := io.ReadAll(rec.Body)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	entries, ok := result["metrics"].([]any)
	require.True(t, ok)
	assert.Len(t, entries, 2) // GET /api 200 and POST /api 201
}

func TestInMemoryCollector_Snapshot(t *testing.T) {
	c := NewInMemoryCollector()
	c.RecordRequest("GET", "/a", 200, 0.001)
	c.RecordRequest("GET", "/a", 200, 0.002)

	snap := c.Snapshot()
	assert.Equal(t, int64(2), snap.RequestCounts["GET /a 200"])
	assert.True(t, snap.DurationSumsMs["GET /a 200"] >= 0)
}

// Verify interface compliance at compile time.
var _ Collector = (*InMemoryCollector)(nil)
