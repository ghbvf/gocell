package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/stretchr/testify/assert"
)

func TestMetrics_RecordsMetrics(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := Metrics(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	snap := c.Snapshot()
	key := "POST /api/v1/users 201"
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_DefaultStatus200(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := Metrics(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	snap := c.Snapshot()
	key := "GET /health 200"
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_MultipleRequests(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := Metrics(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for range 5 {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	snap := c.Snapshot()
	key := "GET /test 200"
	assert.Equal(t, int64(5), snap.RequestCounts[key])
}
