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
	// Recorder creates the shared RecorderState that Metrics reads.
	handler := Recorder(Metrics(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})))

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
	// Recorder creates the shared RecorderState that Metrics reads.
	handler := Recorder(Metrics(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	snap := c.Snapshot()
	key := "GET /health 200"
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_PanicRecordsStatus500(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	// New chain order: Recorder → Metrics → Recovery → handler.
	// Recovery catches panic and writes 500; Metrics sees the 500 status
	// because it shares the RecorderState created by Recorder.
	handler := Recorder(Metrics(c)(Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))))

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	snap := c.Snapshot()
	key := "GET /panic 500"
	assert.Equal(t, int64(1), snap.RequestCounts[key], "panic request must be recorded as status 500 in metrics")
}

// TestMetrics_Standalone verifies Metrics works without Recorder middleware,
// creating its own RecorderState.
func TestMetrics_Standalone(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	// No Recorder — Metrics creates its own RecorderState.
	handler := Metrics(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	req := httptest.NewRequest(http.MethodPost, "/jobs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)

	snap := c.Snapshot()
	key := "POST /jobs 202"
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_MultipleRequests(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	// Recorder creates the shared RecorderState that Metrics reads.
	handler := Recorder(Metrics(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	for range 5 {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	snap := c.Snapshot()
	key := "GET /test 200"
	assert.Equal(t, int64(5), snap.RequestCounts[key])
}
