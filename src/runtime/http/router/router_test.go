package router

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"nhooyr.io/websocket"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
	rtws "github.com/ghbvf/gocell/runtime/websocket"
	ws "github.com/ghbvf/gocell/adapters/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubCell struct{ *cell.BaseCell }

func newStubCell(id string) *stubCell {
	return &stubCell{BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore})}
}

func TestRouterImplementsRouteMux(t *testing.T) {
	r := New()
	var mux cell.RouteMux = r
	assert.NotNil(t, mux)
}

func TestHealthEndpoints(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "test"})
	c := newStubCell("cell-1")
	require.NoError(t, asm.Register(c))
	require.NoError(t, asm.Start(context.Background()))
	defer func() { _ = asm.Stop(context.Background()) }()

	hh := health.New(asm)
	r := New(WithHealthHandler(hh))

	// Test /healthz
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "healthy", body["status"])

	// Test /readyz
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/readyz", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMetricsEndpoint(t *testing.T) {
	mc := metrics.NewInMemoryCollector()
	r := New(
		WithMetricsCollector(mc),
		WithMetricsHandler(mc.Handler()),
	)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
}

func TestHandleAndServe(t *testing.T) {
	r := New()
	r.Handle("/test", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":"ok"}`))
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRouteGroup(t *testing.T) {
	r := New()
	r.Route("/api/v1", func(mux cell.RouteMux) {
		mux.Handle("/ping", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"data":"pong"}`))
		}))
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ping", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGroup(t *testing.T) {
	r := New()
	r.Group(func(mux cell.RouteMux) {
		mux.Handle("/grouped", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/grouped", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMount(t *testing.T) {
	r := New()
	subRouter := chi.NewRouter()
	subRouter.Get("/hello", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	r.Mount("/sub", subRouter)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/sub/hello", nil)
	r.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRouterChain_WebSocketUpgrade(t *testing.T) {
	hub := rtws.NewHub(rtws.HubConfig{ReadLimit: 4096}, func(_ context.Context, _ string, _ []byte) {})

	go func() { _ = hub.Start(context.Background()) }()
	require.Eventually(t, func() bool { return hub.IsRunning() }, 2*time.Second, time.Millisecond)
	t.Cleanup(func() { _ = hub.Stop(context.Background()) })

	r := New()
	r.Mount("/ws", ws.UpgradeHandler(hub, ws.UpgradeConfig{}))

	srv := httptest.NewServer(r)
	defer srv.Close()

	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/ws"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err, "WebSocket upgrade through router middleware chain must succeed")
	conn.CloseNow()
}

func TestPanicRequestRecordedInAccessLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	slog.SetDefault(logger)
	defer slog.SetDefault(slog.Default())

	r := New()
	r.Handle("/boom", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		panic("access log panic test")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	// Parse all JSON log entries and find the access log (level=INFO, msg="http request").
	var found bool
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry["msg"] == "http request" {
			found = true
			assert.Equal(t, float64(500), entry["status"], "access log must capture status 500 for panic requests")
			break
		}
	}
	assert.True(t, found, "access log entry must exist for panic request")
}

func TestPanicRequestRecordedInMetrics(t *testing.T) {
	mc := metrics.NewInMemoryCollector()
	r := New(WithMetricsCollector(mc))
	r.Handle("/boom", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		panic("metrics panic test")
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	snap := mc.Snapshot()
	key := "GET /boom 500"
	assert.Equal(t, int64(1), snap.RequestCounts[key], "metrics must record panic request as status 500")
}

func TestNormalRequestUnchanged(t *testing.T) {
	mc := metrics.NewInMemoryCollector()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	slog.SetDefault(logger)
	defer slog.SetDefault(slog.Default())

	r := New(WithMetricsCollector(mc))
	r.Handle("/ok", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":"ok"}`))
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ok", nil)
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, `{"data":"ok"}`, rec.Body.String())

	// Verify access log has status 200.
	var found bool
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry["msg"] == "http request" {
			found = true
			assert.Equal(t, float64(200), entry["status"])
			break
		}
	}
	assert.True(t, found, "access log entry must exist")

	// Verify metrics recorded status 200.
	snap := mc.Snapshot()
	key := "GET /ok 200"
	assert.Equal(t, int64(1), snap.RequestCounts[key])
}

func TestDefaultMiddlewareApplied(t *testing.T) {
	r := New()
	r.Handle("/mid-test", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/mid-test", nil)
	r.ServeHTTP(rec, req)

	// Security headers should be set by default middleware.
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	// RequestID middleware should set X-Request-Id.
	assert.NotEmpty(t, rec.Header().Get("X-Request-Id"))
}
