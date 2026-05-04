package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	kctxkeys "github.com/ghbvf/gocell/kernel/ctxkeys"
	pkgctxkeys "github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/runtime/auth"
)

func TestAccessLog_LogsFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	// Recorder creates the shared RecorderState that AccessLog reads.
	handler := Recorder(AccessLog(clock.Real())(inner))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", nil)
	// Simulate request_id already in context
	ctx := pkgctxkeys.WithRequestID(req.Context(), "req-123")
	ctx = pkgctxkeys.WithCorrelationID(ctx, "corr-123")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	var logEntry map[string]any
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)

	assert.Equal(t, "POST", logEntry["method"])
	assert.Equal(t, "/api/v1/users", logEntry["path"])
	assert.Equal(t, float64(201), logEntry["status"])
	assert.Contains(t, logEntry, "duration_ms")
	assert.Equal(t, "req-123", logEntry["request_id"])
	assert.Equal(t, "corr-123", logEntry["correlation_id"])
}

// TestAccessLog_Standalone verifies AccessLog works without Recorder middleware,
// creating its own RecorderState.
func TestAccessLog_Standalone(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	// No Recorder — AccessLog creates its own RecorderState.
	handler := AccessLog(clock.Real())(inner)

	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var logEntry map[string]any
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)
	assert.Equal(t, float64(404), logEntry["status"])
}

func TestAccessLog_DefaultStatus200(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No explicit WriteHeader → default 200
		_, _ = w.Write([]byte("ok"))
	})
	// Recorder creates the shared RecorderState that AccessLog reads.
	handler := Recorder(AccessLog(clock.Real())(inner))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var logEntry map[string]any
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)
	assert.Equal(t, float64(200), logEntry["status"])
}

func TestAccessLog_TraceID_WhenSet(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Recorder(AccessLog(clock.Real())(inner))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := pkgctxkeys.WithTraceID(req.Context(), "abc123trace")
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var logEntry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &logEntry))
	assert.Equal(t, "abc123trace", logEntry["trace_id"])
}

func TestAccessLog_CellID_WhenSet(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Recorder(AccessLog(clock.Real())(inner))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/access/users", nil)
	req = req.WithContext(kctxkeys.WithCellID(req.Context(), "accesscore"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var logEntry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &logEntry))
	assert.Equal(t, "accesscore", logEntry["cell_id"])
}

func TestAccessLog_Listener_WhenSet(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	handler := ListenerContext("internal")(Recorder(AccessLog(clock.Real())(inner)))

	req := httptest.NewRequest(http.MethodPatch, "/internal/v1/config/key", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var logEntry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &logEntry))
	assert.Equal(t, "internal", logEntry["listener"])
}

func TestAccessLog_NoListener_WhenNotSet(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Recorder(AccessLog(clock.Real())(inner))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var logEntry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &logEntry))
	assert.NotContains(t, logEntry, "listener")
}

func TestAccessLog_NoTraceID_WhenNotSet(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Recorder(AccessLog(clock.Real())(inner))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var logEntry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &logEntry))
	assert.Nil(t, logEntry["trace_id"], "trace_id must not appear when no tracer is configured")
}

func TestAccessLog_CallerCell_ServicePrincipal(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Recorder(AccessLog(clock.Real())(inner))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/access/roles", nil)
	req = req.WithContext(auth.TestServiceContext("accesscore"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var logEntry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &logEntry))
	assert.Equal(t, "accesscore", logEntry["caller_cell"])
	assert.Nil(t, logEntry["subject"], "subject must not appear for service principals")
}

func TestAccessLog_Subject_UserPrincipal(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Recorder(AccessLog(clock.Real())(inner))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me", nil)
	ctx := auth.WithPrincipal(req.Context(), &auth.Principal{
		Kind:    auth.PrincipalUser,
		Subject: "user-abc123",
	})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var logEntry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &logEntry))
	assert.Equal(t, "user-abc123", logEntry["subject"])
	assert.Nil(t, logEntry["caller_cell"], "caller_cell must not appear for user principals")
}

func TestAccessLog_NoPrincipalAttrs_WhenNoPrincipal(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Recorder(AccessLog(clock.Real())(inner))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/public", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var logEntry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &logEntry))
	assert.Nil(t, logEntry["caller_cell"], "caller_cell must not appear without principal")
	assert.Nil(t, logEntry["subject"], "subject must not appear without principal")
}

func TestAccessLog_NoCallerCell_ServicePrincipalEmptyCallerCellID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Recorder(AccessLog(clock.Real())(inner))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/some", nil)
	ctx := auth.WithPrincipal(req.Context(), &auth.Principal{
		Kind:         auth.PrincipalService,
		CallerCellID: "", // empty — must not emit field
	})
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var logEntry map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &logEntry))
	assert.Nil(t, logEntry["caller_cell"], "caller_cell must not appear when CallerCellID is empty")
}
