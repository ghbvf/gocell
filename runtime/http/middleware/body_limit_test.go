package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

func TestBodyLimit_UnderLimit(t *testing.T) {
	handler := BodyLimit(1024, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	handler := BodyLimit(10, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	mc := metrics.NewInMemoryCollector()
	handler := BodyLimit(10, mc, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	// Streaming overruns (MaxBytesReader path) must NOT increment the
	// body-limit rejection counter — only the Content-Length fast-path does.
	// The 413 from streaming is captured separately by http_requests_total via RecordRequest.
	snap := mc.Snapshot()
	assert.Empty(t, snap.BodyLimitRejections,
		"streaming MaxBytesReader overrun must not increment the body-limit rejection counter")
}

func TestBodyLimit_DefaultLimit(t *testing.T) {
	handler := BodyLimit(0, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	handler := BodyLimit(10, mc, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run when body limit rejects")
	}))

	body := bytes.Repeat([]byte("x"), 20)
	req := httptest.NewRequest(http.MethodPost, "/upload", bytes.NewReader(body))
	req.ContentLength = 20
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	snap := mc.Snapshot()
	// No ctxkeys.CellID in ctx → falls back to metrics.RuntimeCellSentinel.
	// No RouteResolver in ctx → RouteFor falls back to UnmatchedRoute.
	key := metrics.BodyLimitRejectionKey{
		Cell:  metrics.RuntimeCellSentinel,
		Route: UnmatchedRoute,
	}
	assert.Equal(t, int64(1), snap.BodyLimitRejections[key],
		"body-limit rejection counter must be incremented exactly once on Content-Length fast-path reject")
}

// TestBodyLimit_NilCollector_NoRejectionCounted verifies that passing nil
// collector does not panic and no counter is incremented.
func TestBodyLimit_NilCollector_NoRejectionCounted(t *testing.T) {
	handler := BodyLimit(10, nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	handler := BodyLimit(1024, mc, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// TestBodyLimit_UnmatchedRoutesDoNotExpandCardinality verifies that N requests
// arriving on distinct arbitrary paths — with no RouteResolver context — all
// fold into the single {Cell:"_runtime", Route:"unmatched"} bucket.  This
// guards against the cardinality-explosion attack vector: an adversary sending
// oversized bodies on random paths must not be able to inflate label space.
func TestBodyLimit_UnmatchedRoutesDoNotExpandCardinality(t *testing.T) {
	mc := metrics.NewInMemoryCollector()
	handler := BodyLimit(10, mc, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run when body limit rejects")
	}))

	body := bytes.Repeat([]byte("x"), 20)
	paths := []string{"/foo", "/bar/baz", "/api/v99/evil", "/etc/passwd", "/a/b/c/d/e"}
	for _, p := range paths {
		req := httptest.NewRequest(http.MethodPost, p, bytes.NewReader(body))
		req.ContentLength = 20
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	}

	snap := mc.Snapshot()
	// All rejections must collapse into a single key — no per-path cardinality growth.
	require.Len(t, snap.BodyLimitRejections, 1,
		"distinct arbitrary paths must not expand body-limit rejection label cardinality")
	key := metrics.BodyLimitRejectionKey{
		Cell:  metrics.RuntimeCellSentinel,
		Route: UnmatchedRoute,
	}
	assert.Equal(t, int64(len(paths)), snap.BodyLimitRejections[key],
		"all unmatched-route rejections must fold into the _runtime/unmatched bucket")
}

// TestBodyLimit_StreamingOverrun_Via_HttputilWriteError proves that when a
// handler uses httputil.WriteError to propagate *http.MaxBytesError, the
// framework path produces a 413 response with the canonical ERR_BODY_TOO_LARGE
// code. This validates the claim in the BodyLimit godoc that "framework-
// generated handlers produce a 413 captured in http_requests_total{status=413}"
// via RecordRequest — the 413 originates from WriteError, not from BodyLimit.
func TestBodyLimit_StreamingOverrun_Via_HttputilWriteError(t *testing.T) {
	// A handler that reads the body and propagates any error through WriteError,
	// mimicking what framework-generated handlers do.
	frameworkLikeHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			httputil.WriteError(context.Background(), w, err)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	handler := BodyLimit(10, nil, nil)(frameworkLikeHandler)

	// Send a body larger than the limit, without a matching Content-Length
	// (so the fast-path does not trigger — only MaxBytesReader kicks in).
	body := bytes.Repeat([]byte("x"), 20)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", bytes.NewReader(body))
	req.ContentLength = -1 // unknown → skip fast-path
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// The framework path via httputil.WriteError maps MaxBytesError → 413.
	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code,
		"streaming overrun with httputil.WriteError propagation must produce 413")

	var respBody map[string]any
	err := json.NewDecoder(rec.Body).Decode(&respBody)
	require.NoError(t, err)
	errObj, ok := respBody["error"].(map[string]any)
	require.True(t, ok, "response must have 'error' object")
	assert.Equal(t, "ERR_BODY_TOO_LARGE", errObj["code"],
		"error code must be ERR_BODY_TOO_LARGE for MaxBytesError via WriteError")
}

// TestBodyLimit_CellFromContext ensures the production code uses
// ctxkeys.CellIDFrom for the cell label when a cell ID is present in ctx.
func TestBodyLimit_CellFromContext(t *testing.T) {
	mc := metrics.NewInMemoryCollector()
	const cellID = "mycell"
	handler := BodyLimit(10, mc, map[string]struct{}{cellID: {}})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not run")
	}))

	body := bytes.Repeat([]byte("x"), 20)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", bytes.NewReader(body))
	req.ContentLength = 20

	// Simulate CellAttribution having written the cell into ctx.
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
