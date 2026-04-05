package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccessLog_LogsFields(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	slog.SetDefault(logger)
	defer slog.SetDefault(slog.Default())

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	})
	handler := AccessLog(inner)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/users", nil)
	// Simulate request_id already in context
	ctx := ctxkeys.WithRequestID(req.Context(), "req-123")
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
}

func TestAccessLog_DefaultStatus200(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	slog.SetDefault(logger)
	defer slog.SetDefault(slog.Default())

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// No explicit WriteHeader → default 200
		_, _ = w.Write([]byte("ok"))
	})
	handler := AccessLog(inner)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	var logEntry map[string]any
	err := json.Unmarshal(buf.Bytes(), &logEntry)
	require.NoError(t, err)
	assert.Equal(t, float64(200), logEntry["status"])
}
