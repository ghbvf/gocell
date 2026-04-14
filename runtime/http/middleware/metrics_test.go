package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/stretchr/testify/assert"
)

// --- Standalone tests (no chi router → route = "unmatched") ---

func TestMetrics_RecordsMetrics(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := Recorder(Metrics(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	snap := c.Snapshot()
	// Without chi router, route pattern falls back to "unmatched".
	key := "POST unmatched 201"
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_DefaultStatus200(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := Recorder(Metrics(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	snap := c.Snapshot()
	key := "GET unmatched 200"
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_PanicRecordsStatus500(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := Recorder(Metrics(c)(Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))))

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	snap := c.Snapshot()
	key := "GET unmatched 500"
	assert.Equal(t, int64(1), snap.RequestCounts[key], "panic request must be recorded as status 500 in metrics")
}

func TestMetrics_Standalone(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := Metrics(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	req := httptest.NewRequest(http.MethodPost, "/jobs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)

	snap := c.Snapshot()
	key := "POST unmatched 202"
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_MultipleRequests(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := Recorder(Metrics(c)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	for range 5 {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	snap := c.Snapshot()
	key := "GET unmatched 200"
	assert.Equal(t, int64(5), snap.RequestCounts[key])
}

// --- Chi-integrated tests (route pattern extraction) ---

func TestMetrics_RoutePatternCollapse(t *testing.T) {
	c := metrics.NewInMemoryCollector()

	r := chi.NewRouter()
	r.Use(Recorder, Metrics(c))
	r.Get("/api/v1/users/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Hit with different path parameters — all should collapse to one metric key.
	for _, id := range []string{"1", "2", "100", "abc"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/users/"+id, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	}

	snap := c.Snapshot()
	key := "GET /api/v1/users/{id} 200"
	assert.Equal(t, int64(4), snap.RequestCounts[key],
		"parameterized routes must collapse to route pattern, not actual path")
	assert.Len(t, snap.RequestCounts, 1,
		"all requests to same route pattern must produce exactly one metric key")
}

func TestMetrics_UnmatchedRouteUsesSentinel(t *testing.T) {
	c := metrics.NewInMemoryCollector()

	r := chi.NewRouter()
	r.Use(Recorder, Metrics(c))
	r.Get("/exists", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Hit random 404 paths — all should collapse to "unmatched" sentinel.
	for _, path := range []string{"/random1", "/random2", "/attack-path"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
	}

	snap := c.Snapshot()
	key := "GET unmatched 404"
	assert.Equal(t, int64(3), snap.RequestCounts[key],
		"unmatched routes must all map to sentinel 'unmatched' label")
}

func TestMetrics_ChiStaticRoute(t *testing.T) {
	c := metrics.NewInMemoryCollector()

	r := chi.NewRouter()
	r.Use(Recorder, Metrics(c))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	snap := c.Snapshot()
	key := "GET /healthz 200"
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_ChiNestedRoutes(t *testing.T) {
	c := metrics.NewInMemoryCollector()

	r := chi.NewRouter()
	r.Use(Recorder, Metrics(c))
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/orders/{orderID}", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/42", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	snap := c.Snapshot()
	key := "GET /api/v1/orders/{orderID} 200"
	assert.Equal(t, int64(1), snap.RequestCounts[key],
		"nested routes must produce combined route pattern")
}
