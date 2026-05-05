package middleware

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecovery_NoPanic(t *testing.T) {
	// Recorder creates the shared RecorderState that Recovery reads.
	handler := Recorder(Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
}

func TestRecovery_PanicString(t *testing.T) {
	handler := Recorder(Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})))

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var body map[string]any
	err := json.NewDecoder(rec.Body).Decode(&body)
	require.NoError(t, err)

	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ERR_INTERNAL", errObj["code"])
	assert.Equal(t, "internal server error", errObj["message"])
	assert.Equal(t, []any{}, errObj["details"], "canonical envelope must include empty details object")
}

func TestRecovery_PanicBodyDoesNotLeakPanicValue(t *testing.T) {
	const leakSentinel = "recovery-body-leak-sentinel-7c1"
	handler := Recorder(Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("password=" + leakSentinel)
	})))

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.False(t, strings.Contains(rec.Body.String(), leakSentinel),
		"500 body must remain generic and never include panic payload")
	assert.Contains(t, rec.Body.String(), "internal server error")
}

func TestRecovery_PanicLogRedactsPanicValue(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const leakSentinel = "recovery-log-leak-sentinel-2b8"
	handler := Recorder(Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("token=" + leakSentinel)
	})))

	req := httptest.NewRequest(http.MethodGet, "/panic", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, logs.String(), leakSentinel)
	assert.Contains(t, logs.String(), "REDACTED")
}

func TestRecovery_PanicError(t *testing.T) {
	handler := Recorder(Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(42)
	})))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// TestRecovery_Standalone verifies Recovery works without Recorder middleware,
// creating its own RecorderState for committed-response detection.
func TestRecovery_Standalone(t *testing.T) {
	handler := Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("standalone panic")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var body map[string]any
	err := json.NewDecoder(rec.Body).Decode(&body)
	require.NoError(t, err)
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ERR_INTERNAL", errObj["code"])
}

// TestRecovery_StandaloneCommitted verifies Recovery detects committed
// responses even without Recorder middleware in the chain.
func TestRecovery_StandaloneCommitted(t *testing.T) {
	handler := Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("late standalone panic")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code, "status must remain 200 — already committed")
	assert.Equal(t, "partial", rec.Body.String(), "body must not have JSON error appended")
}

func TestRecovery_PanicAfterPartialWrite(t *testing.T) {
	handler := Recorder(Recovery(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
		panic("late panic")
	})))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Response was already committed (200 + partial body written).
	// Recovery must NOT append JSON error to the already-committed response.
	assert.Equal(t, http.StatusOK, rec.Code, "status must remain 200 — already committed")
	assert.Equal(t, "partial", rec.Body.String(), "body must not have JSON error appended")
}
