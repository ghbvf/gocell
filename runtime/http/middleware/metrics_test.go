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

// withTestCellID wraps a handler chain with the cell-id context middleware
// the bootstrap layer installs in production. Tests that exercise Metrics
// directly need this so MustCellIDFrom does not panic — the contract under
// test is "given a request with cell id in ctx, RecordRequest receives it",
// not "Metrics tolerates missing ctx".
func withTestCellID(cellID string, h http.Handler) http.Handler {
	return WithCellIDContext(cellID)(h)
}

// --- Standalone tests (no chi router → route = "unmatched") ---

func TestMetrics_RecordsMetrics(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := withTestCellID("test-cell", Recorder(Metrics(c, clock.Real())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	snap := c.Snapshot()
	// Without chi router, route pattern falls back to "unmatched".
	key := "test-cell POST unmatched 201"
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_DefaultStatus200(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := withTestCellID("test-cell", Recorder(Metrics(c, clock.Real())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	snap := c.Snapshot()
	key := "test-cell GET unmatched 200"
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_PanicRecordsStatus500(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	panicHandler := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("boom")
	})
	handler := withTestCellID("test-cell", Recorder(Metrics(c, clock.Real())(Recovery(panicHandler))))

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	snap := c.Snapshot()
	key := "test-cell GET unmatched 500"
	assert.Equal(t, int64(1), snap.RequestCounts[key], "panic request must be recorded as status 500 in metrics")
}

func TestMetrics_Standalone(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := withTestCellID("test-cell", Metrics(c, clock.Real())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})))

	req := httptest.NewRequest(http.MethodPost, "/jobs", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusAccepted, rec.Code)

	snap := c.Snapshot()
	key := "test-cell POST unmatched 202"
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_MultipleRequests(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	handler := withTestCellID("test-cell", Recorder(Metrics(c, clock.Real())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))))

	for range 5 {
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	snap := c.Snapshot()
	key := "test-cell GET unmatched 200"
	assert.Equal(t, int64(5), snap.RequestCounts[key])
}

// TestMetrics_CellIDFromCtxFlowsToCollector verifies the contract that the
// per-request cell id from ctx (installed upstream by WithCellIDContext)
// reaches the Collector — not a global, not a constant, not a fallback.
func TestMetrics_CellIDFromCtxFlowsToCollector(t *testing.T) {
	c := metrics.NewInMemoryCollector()

	for _, cellID := range []string{"accesscore", "auditcore", "_runtime"} {
		handler := withTestCellID(cellID, Recorder(Metrics(c, clock.Real())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))))
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	snap := c.Snapshot()
	for _, cellID := range []string{"accesscore", "auditcore", "_runtime"} {
		key := cellID + " GET unmatched 200"
		assert.Equal(t, int64(1), snap.RequestCounts[key], "missing key %q in %v", key, snap.RequestCounts)
	}
}

// TestMetrics_PanicsWhenCellIDMissing locks the fail-fast contract: if a
// request reaches the Metrics middleware without a cell id in ctx (i.e. the
// listener-root WithCellIDContext sentinel layer was bypassed), the recorder
// must panic so framework wiring bugs surface immediately, instead of being
// swallowed by safeObserve and silently emitting metrics under a fallback.
func TestMetrics_PanicsWhenCellIDMissing(t *testing.T) {
	c := metrics.NewInMemoryCollector()
	// No WithCellIDContext wrapper — this is the bug scenario.
	handler := Recorder(Metrics(c, clock.Real())(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})))

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req) // safeObserve recovers the panic; assert no record was emitted

	snap := c.Snapshot()
	assert.Empty(t, snap.RequestCounts,
		"missing cell id in ctx must abort RecordRequest (no fallback emission); got %v", snap.RequestCounts)
}

// --- Chi-integrated tests (route pattern extraction) ---

func TestMetrics_RoutePatternCollapse(t *testing.T) {
	c := metrics.NewInMemoryCollector()

	r := chi.NewRouter()
	r.Use(WithCellIDContext("test-cell"), Recorder, Metrics(c, clock.Real()))
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
	key := "test-cell GET /api/v1/users/{id} 200"
	assert.Equal(t, int64(4), snap.RequestCounts[key],
		"parameterized routes must collapse to route pattern, not actual path")
	assert.Len(t, snap.RequestCounts, 1,
		"all requests to same route pattern must produce exactly one metric key")
}

func TestMetrics_UnmatchedRouteUsesSentinel(t *testing.T) {
	c := metrics.NewInMemoryCollector()

	r := chi.NewRouter()
	r.Use(WithCellIDContext("_runtime"), Recorder, Metrics(c, clock.Real()))
	r.Get("/exists", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Hit random 404 paths — all should collapse to "unmatched" sentinel and
	// carry the framework "_runtime" cell id (no RouteGroup matched).
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
	r.Use(WithCellIDContext("test-cell"), Recorder, Metrics(c, clock.Real()))
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	snap := c.Snapshot()
	key := "test-cell GET /healthz 200"
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestMetrics_ChiNestedRoutes(t *testing.T) {
	c := metrics.NewInMemoryCollector()

	r := chi.NewRouter()
	r.Use(WithCellIDContext("test-cell"), Recorder, Metrics(c, clock.Real()))
	r.Route("/api/v1", func(r chi.Router) {
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
		"nested routes must produce combined route pattern")
}
