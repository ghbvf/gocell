package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

func TestBodyLimit_UnderLimit(t *testing.T) {
	handler := BodyLimit(1024, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.Equal(t, "hello", string(body))
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("hello"))
	req.Header.Set("Content-Length", "5")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestBodyLimit_ExactContentLengthOverLimit(t *testing.T) {
	handler := BodyLimit(10, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called")
	}))

	body := bytes.Repeat([]byte("x"), 20)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.ContentLength = 20
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	var respBody map[string]any
	err := json.NewDecoder(rec.Body).Decode(&respBody)
	require.NoError(t, err)
	errObj := respBody["error"].(map[string]any)
	assert.Equal(t, "ERR_BODY_TOO_LARGE", errObj["code"])
	assert.Equal(t, []any{}, errObj["details"], "canonical envelope must include empty details object")
}

func TestBodyLimit_MaxBytesReaderTriggered(t *testing.T) {
	handler := BodyLimit(10, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		// MaxBytesReader returns an error when body exceeds limit
		assert.Error(t, err)
	}))

	// ContentLength not set (or set to less), but actual body is larger
	body := bytes.Repeat([]byte("x"), 20)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.ContentLength = -1 // unknown
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
}

func TestBodyLimit_DefaultLimit(t *testing.T) {
	handler := BodyLimit(0, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("small"))
	req.Header.Set("Content-Length", "5")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestBodyLimit_RecordsRejectionMetric verifies that the Content-Length
// fast-path rejection increments the body-limit rejection counter.
func TestBodyLimit_RecordsRejectionMetric(t *testing.T) {
	mc := metrics.NewInMemoryCollector()
	handler := BodyLimit(10, mc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run when body limit rejects")
	}))

	body := bytes.Repeat([]byte("x"), 20)
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(body))
	req.ContentLength = 20
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	snap := mc.Snapshot()
	// No ctxkeys.CellID in ctx → falls back to RuntimeCellIDSentinel.
	// No RouteResolver in ctx → RouteFor falls back to UnmatchedRoute.
	key := metrics.BodyLimitRejectionKey{
		Cell:  RuntimeCellIDSentinel,
		Route: UnmatchedRoute,
	}
	assert.Equal(t, int64(1), snap.BodyLimitRejections[key],
		"body-limit rejection counter must be incremented exactly once on Content-Length fast-path reject")
}

// TestBodyLimit_NilCollector_NoRejectionCounted verifies that passing nil
// collector does not panic and no counter is incremented.
func TestBodyLimit_NilCollector_NoRejectionCounted(t *testing.T) {
	handler := BodyLimit(10, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run")
	}))

	body := bytes.Repeat([]byte("x"), 20)
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(body))
	req.ContentLength = 20
	rec := httptest.NewRecorder()

	// Must not panic.
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

// TestBodyLimit_UnderLimit_NoRejectionCounted verifies that under-limit
// requests do not increment the body-limit rejection counter.
func TestBodyLimit_UnderLimit_NoRejectionCounted(t *testing.T) {
	mc := metrics.NewInMemoryCollector()
	handler := BodyLimit(1024, mc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("small"))
	req.ContentLength = 5
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	snap := mc.Snapshot()
	assert.Empty(t, snap.BodyLimitRejections,
		"under-limit requests must not increment the body-limit rejection counter")
}

// TestBodyLimit_CellFromContext ensures the production code uses
// ctxkeys.CellIDFrom for the cell label when a cell ID is present in ctx.
func TestBodyLimit_CellFromContext(t *testing.T) {
	mc := metrics.NewInMemoryCollector()
	handler := BodyLimit(10, mc)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run")
	}))

	body := bytes.Repeat([]byte("x"), 20)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", bytes.NewReader(body))
	req.ContentLength = 20

	// Simulate CellAttribution having written the cell into ctx.
	const cellID = "mycell"
	req = req.WithContext(ctxkeys.WithCellID(req.Context(), cellID))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	snap := mc.Snapshot()
	// Cell should come from context; route falls back to UnmatchedRoute.
	key := metrics.BodyLimitRejectionKey{
		Cell:  cellID,
		Route: UnmatchedRoute,
	}
	assert.Equal(t, int64(1), snap.BodyLimitRejections[key],
		"body-limit rejection cell label must come from ctxkeys.CellIDFrom")
}
