package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

func requestKey(cell, method, route string, status int) metrics.RequestKey {
	return metrics.RequestKey{Cell: cell, Method: method, Route: route, Status: status}
}

// --- Standalone tests (no chi router → route = "unmatched") ---

func TestMetrics_RecordsMetrics(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := Recorder(Metrics(c, clock.Real())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	snap := c.Snapshot()
	key := requestKey("_runtime", http.MethodPost, "unmatched", http.StatusCreated)
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_DefaultStatus200(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := Recorder(Metrics(c, clock.Real())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	snap := c.Snapshot()
	key := requestKey("_runtime", http.MethodGet, "unmatched", http.StatusOK)
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_PanicRecordsStatus500(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	panicHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	})
	handler := Recorder(Metrics(c, clock.Real())(Recovery(panicHandler)))

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	snap := c.Snapshot()
	key := requestKey("_runtime", http.MethodGet, "unmatched", http.StatusInternalServerError)
	assert.Equal(t, int64(1), snap.RequestCounts[key], "panic request must be recorded as status 500 in metrics")
}

func TestMetrics_Standalone(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := Metrics(c, clock.Real())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))

	req := httptest.NewRequest(http.MethodPost, "/jobs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)

	snap := c.Snapshot()
	key := requestKey("_runtime", http.MethodPost, "unmatched", http.StatusAccepted)
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_RouteResolverFallback(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := Metrics(c, clock.Real(), WithRoutePatternResolver(func(method, path string) (string, bool) {
		assert.Equal(t, http.MethodGet, method)
		assert.Equal(t, "/api/v1/access/users/42", path)
		return "/api/v1/access/users/{id}", true
	}))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users/42", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	snap := c.Snapshot()
	key := requestKey("_runtime", http.MethodGet, "/api/v1/access/users/{id}", http.StatusUnauthorized)
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_MultipleRequests(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := Recorder(Metrics(c, clock.Real())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	for range 5 {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	snap := c.Snapshot()
	key := requestKey("_runtime", http.MethodGet, "unmatched", http.StatusOK)
	assert.Equal(t, int64(5), snap.RequestCounts[key])
}

func TestMetrics_ReadsCellIDFromContext(t *testing.T) {
	c := metrics.NewInMemoryCollector()

	r := chi.NewRouter()
	r.Use(
		Recorder,
		CellAttribution(func(_, path string) (string, bool) {
			switch path {
			case "/api/v1/users/42":
				return "accesscore", true
			case "/api/v1/audit/99":
				return "auditcore", true
			default:
				return "", false
			}
		}),
		Metrics(c, clock.Real()),
	)
	r.Get("/api/v1/users/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Get("/api/v1/audit/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// One unmatched request — falls through with the sentinel.
	r.Get("/orphan-but-matched", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/users/42"},
		{http.MethodGet, "/api/v1/audit/99"},
		{http.MethodGet, "/orphan-but-matched"},
		{http.MethodGet, "/totally-missing"},
	} {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
	}

	snap := c.Snapshot()
	for _, key := range []metrics.RequestKey{
		requestKey("accesscore", http.MethodGet, "/api/v1/users/{id}", http.StatusOK),
		requestKey("auditcore", http.MethodGet, "/api/v1/audit/{id}", http.StatusOK),
		requestKey("_runtime", http.MethodGet, "/orphan-but-matched", http.StatusOK),
		requestKey("_runtime", http.MethodGet, "unmatched", http.StatusNotFound),
	} {
		assert.Equalf(t, int64(1), snap.RequestCounts[key],
			"want exactly one observation under %q; full snapshot=%v", key, snap.RequestCounts)
	}
}

// --- Chi-integrated tests (route pattern extraction) ---

func TestMetrics_RoutePatternCollapse(t *testing.T) {
	c := metrics.NewInMemoryCollector()

	r := chi.NewRouter()
	r.Use(
		Recorder,
		CellAttribution(func(_, path string) (string, bool) {
			if path != "" {
				return "test-cell", true
			}
			return "", false
		}),
		Metrics(c, clock.Real()),
	)
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
	key := requestKey("test-cell", http.MethodGet, "/api/v1/users/{id}", http.StatusOK)
	assert.Equal(t, int64(4), snap.RequestCounts[key],
		"parameterized routes must collapse to route pattern, not actual path")
	assert.Len(t, snap.RequestCounts, 1,
		"all requests to same route pattern must produce exactly one metric key")
}

func TestMetrics_UnmatchedRouteUsesSentinel(t *testing.T) {
	c := metrics.NewInMemoryCollector()

	r := chi.NewRouter()
	r.Use(Recorder, Metrics(c, clock.Real()))
	r.Get("/exists", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Hit random 404 paths — all should collapse to "unmatched" under
	// cell="_runtime" (no root attribution resolved a cell).
	for _, path := range []string{"/random1", "/random2", "/attack-path"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
	}

	snap := c.Snapshot()
	key := requestKey("_runtime", http.MethodGet, "unmatched", http.StatusNotFound)
	assert.Equal(t, int64(3), snap.RequestCounts[key],
		"unmatched routes must all map to sentinel 'unmatched' label under cell=_runtime")
}

func TestMetrics_ChiStaticRoute(t *testing.T) {
	c := metrics.NewInMemoryCollector()

	r := chi.NewRouter()
	r.Use(Recorder, Metrics(c, clock.Real()))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	snap := c.Snapshot()
	key := requestKey("_runtime", http.MethodGet, "/healthz", http.StatusOK)
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_ChiNestedRoutes(t *testing.T) {
	c := metrics.NewInMemoryCollector()

	r := chi.NewRouter()
	r.Use(
		Recorder,
		CellAttribution(func(_, path string) (string, bool) {
			if path != "" {
				return "test-cell", true
			}
			return "", false
		}),
		Metrics(c, clock.Real()),
	)
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/orders/{orderID}", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/42", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	snap := c.Snapshot()
	key := requestKey("test-cell", http.MethodGet, "/api/v1/orders/{orderID}", http.StatusOK)
	assert.Equal(t, int64(1), snap.RequestCounts[key],
		"nested routes must produce combined route pattern under the per-cell label")
}
