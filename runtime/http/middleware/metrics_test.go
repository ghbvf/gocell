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
	// No upstream WithCellIDContext layer — Metrics seeds its own state with
	// RuntimeCellIDSentinel and reads back the same value.
	key := "_runtime POST unmatched 201"
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
	key := "_runtime GET unmatched 200"
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
	key := "_runtime GET unmatched 500"
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
	key := "_runtime POST unmatched 202"
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
	key := "_runtime GET unmatched 200"
	assert.Equal(t, int64(5), snap.RequestCounts[key])
}

// TestMetrics_SubMuxOverridesRuntimeSentinel pins the "single injection /
// two-layer override" contract: Metrics installs a *cellIDState seeded with
// RuntimeCellIDSentinel; a downstream WithCellIDContext (analogous to
// bootstrap.mountOneRouteGroup at the chi sub-mux layer) mutates the same
// pointer; the recorder sees the per-cell value, not the sentinel.
func TestMetrics_SubMuxOverridesRuntimeSentinel(t *testing.T) {
	c := metrics.NewInMemoryCollector()

	r := chi.NewRouter()
	r.Use(Recorder, Metrics(c, clock.Real()))
	r.Group(func(sub chi.Router) {
		sub.Use(WithCellIDContext("accesscore"))
		sub.Get("/api/v1/users/{id}", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})
	r.Group(func(sub chi.Router) {
		sub.Use(WithCellIDContext("auditcore"))
		sub.Get("/api/v1/audit/{id}", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
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
	for _, key := range []string{
		"accesscore GET /api/v1/users/{id} 200",
		"auditcore GET /api/v1/audit/{id} 200",
		"_runtime GET /orphan-but-matched 200",
		"_runtime GET unmatched 404",
	} {
		assert.Equalf(t, int64(1), snap.RequestCounts[key],
			"want exactly one observation under %q; full snapshot=%v", key, snap.RequestCounts)
	}
}

// --- Chi-integrated tests (route pattern extraction) ---

func TestMetrics_RoutePatternCollapse(t *testing.T) {
	c := metrics.NewInMemoryCollector()

	r := chi.NewRouter()
	r.Use(Recorder, Metrics(c, clock.Real()))
	r.Group(func(sub chi.Router) {
		sub.Use(WithCellIDContext("test-cell"))
		sub.Get("/api/v1/users/{id}", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	// Hit with different path parameters — all should collapse to one metric key.
	for _, id := range []string{"1", "2", "100", "abc"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/users/"+id, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	}

	snap := c.Snapshot()
	key := "test-cell GET /api/v1/users/{id} 200"
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

	// Hit random 404 paths — all should collapse to "unmatched" sentinel
	// under cell="_runtime" (no sub-mux WithCellIDContext fired).
	for _, path := range []string{"/random1", "/random2", "/attack-path"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
	}

	snap := c.Snapshot()
	key := "_runtime GET unmatched 404"
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
	// /healthz on a router with no per-route WithCellIDContext keeps the
	// listener-root sentinel — this is the framework-owned probe path.
	key := "_runtime GET /healthz 200"
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_ChiNestedRoutes(t *testing.T) {
	c := metrics.NewInMemoryCollector()

	r := chi.NewRouter()
	r.Use(Recorder, Metrics(c, clock.Real()))
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(WithCellIDContext("test-cell"))
		r.Get("/orders/{orderID}", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/orders/42", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	snap := c.Snapshot()
	key := "test-cell GET /api/v1/orders/{orderID} 200"
	assert.Equal(t, int64(1), snap.RequestCounts[key],
		"nested routes must produce combined route pattern under the per-cell label")
}
