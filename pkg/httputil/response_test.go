package httputil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/ctxcancel"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestWriteJSON(t *testing.T) {
	rec := httptest.NewRecorder()

	WriteJSON(rec, http.StatusCreated, map[string]string{"ok": "true"})

	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.JSONEq(t, `{"ok":"true"}`, rec.Body.String())
}

func TestWritePublic_5xxMasksCodeKeepsFrameworkMessageAndLogsOriginal(t *testing.T) {
	handler, restore := installCaptureHandler()
	defer restore()

	rec := httptest.NewRecorder()
	ctx := ctxkeys.WithRequestID(context.Background(), "req-public")

	WritePublic(ctx, rec, errcode.KindUnavailable, "ERR_CIRCUIT_OPEN", "service unavailable")

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	errObj := decodeErrorBody(t, rec)
	assert.Equal(t, string(errcode.ErrServiceUnavailable), errObj["code"])
	assert.Equal(t, "service unavailable", errObj["message"])
	assert.Equal(t, []any{}, errObj["details"])

	errRec := findRecord(handler, slog.LevelError)
	require.NotNil(t, errRec)
	assertStringAttr(t, *errRec, "code", "ERR_CIRCUIT_OPEN")
	assertStringAttr(t, *errRec, "public_code", string(errcode.ErrServiceUnavailable))
	assertStringAttr(t, *errRec, "status", "503")
	assertStringAttr(t, *errRec, "request_id", "req-public")
	assertAttrAbsent(t, *errRec, "message")
}

func TestWriteError_ClientErrorShowsMessageDetailsAndSamplesWarn(t *testing.T) {
	handler, restore := installCaptureHandler()
	defer restore()

	ctx := withClientErrorLogSamplingEvery(context.Background(), t.Name(), 1)
	ctx = ctxkeys.WithRequestID(ctx, "req-4xx")
	ctx = ctxkeys.WithTraceID(ctx, "trace-4xx")
	ctx = ctxkeys.WithSpanID(ctx, "span-4xx")
	err := errcode.New(
		errcode.KindInvalid,
		errcode.ErrValidationFailed,
		"invalid cursor",
		errcode.WithInternal("cursor token failed signature check"),
		errcode.WithDetails(slog.String("reason", "signature")),
	)

	rec := httptest.NewRecorder()
	WriteError(ctx, rec, err)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	errObj := decodeErrorBody(t, rec)
	assert.Equal(t, string(errcode.ErrValidationFailed), errObj["code"])
	assert.Equal(t, "invalid cursor", errObj["message"])
	assert.Equal(t, []any{map[string]any{"key": "reason", "value": "signature"}}, errObj["details"])
	assert.Equal(t, "req-4xx", errObj["request_id"])

	warnRec := findRecord(handler, slog.LevelWarn)
	require.NotNil(t, warnRec)
	assertStringAttr(t, *warnRec, "code", string(errcode.ErrValidationFailed))
	assertStringAttr(t, *warnRec, "status", "400")
	assertStringAttr(t, *warnRec, "internal", "cursor token failed signature check")
	assertStringAttr(t, *warnRec, "request_id", "req-4xx")
	assertStringAttr(t, *warnRec, "trace_id", "trace-4xx")
	assertStringAttr(t, *warnRec, "span_id", "span-4xx")
	assertAttrAbsent(t, *warnRec, "message")
}

func TestWriteError_ClientErrorMissingContextUsesFallbackSampler(t *testing.T) {
	handler, restore := installCaptureHandler()
	defer restore()

	for range 200 {
		rec := httptest.NewRecorder()
		WriteError(context.Background(), rec, errcode.New(
			errcode.KindInvalid,
			errcode.ErrValidationFailed,
			"invalid input",
		))
		assert.Equal(t, http.StatusBadRequest, rec.Code)
	}

	assert.Equal(t, 2, countRecords(handler, slog.LevelWarn))
}

func TestWithClientErrorLogSampling_RouteKeyedCounters(t *testing.T) {
	sampler := newClientErrorLogSampler(2)
	ctxA := sampler.withContext(context.Background(), t.Name()+"/a")
	ctxB := sampler.withContext(context.Background(), t.Name()+"/b")

	assert.False(t, shouldLogClientError(ctxA))
	assert.True(t, shouldLogClientError(ctxA))
	assert.False(t, shouldLogClientError(ctxB))
	assert.True(t, shouldLogClientError(ctxB))
}

func TestWithClientErrorLogSampling_PreservesExistingConfig(t *testing.T) {
	ctx := WithClientErrorLogSamplingEvery(context.Background(), t.Name(), 1)
	ctx = WithClientErrorLogSampling(ctx, t.Name())

	assert.True(t, shouldLogClientError(ctx))
	assert.True(t, shouldLogClientError(ctx))
}

func TestWithClientErrorLogSampling_EveryOneLogsAll(t *testing.T) {
	ctx := withClientErrorLogSamplingEvery(context.Background(), t.Name(), 1)

	assert.True(t, shouldLogClientError(ctx))
	assert.True(t, shouldLogClientError(ctx))

	ctxZero := withClientErrorLogSamplingEvery(context.Background(), t.Name()+"/zero", 0)
	assert.True(t, shouldLogClientError(ctxZero))
	assert.True(t, shouldLogClientError(ctxZero))
}

func TestWriteError_5xxMasksMessageCodeDetailsAndLogsDiagnostics(t *testing.T) {
	handler, restore := installCaptureHandler()
	defer restore()

	cause := errors.New("postgres pool exhausted")
	err := errcode.Wrap(
		errcode.KindInternal,
		errcode.ErrConfigRepoQuery,
		"config query failed for tenant admin@example.com",
		cause,
		errcode.WithInternal("select config_entries failed"),
		errcode.WithDetails(slog.String("tenant", "admin@example.com")),
	)
	ctx := ctxkeys.WithRequestID(context.Background(), "req-5xx")

	rec := httptest.NewRecorder()
	WriteError(ctx, rec, err)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	errObj := decodeErrorBody(t, rec)
	assert.Equal(t, string(errcode.ErrInternal), errObj["code"])
	assert.Equal(t, "internal server error", errObj["message"])
	assert.Equal(t, []any{}, errObj["details"])
	assert.Equal(t, "req-5xx", errObj["request_id"])

	errRec := findRecord(handler, slog.LevelError)
	require.NotNil(t, errRec)
	assertStringAttr(t, *errRec, "code", string(errcode.ErrConfigRepoQuery))
	assertStringAttr(t, *errRec, "public_code", string(errcode.ErrInternal))
	assertStringAttr(t, *errRec, "status", "500")
	assertStringAttr(t, *errRec, "internal", "select config_entries failed")
	assertStringAttr(t, *errRec, "cause", cause.Error())
	assertStringAttr(t, *errRec, "request_id", "req-5xx")
	// Details must be logged server-side even for 5xx (framework strips them
	// from the wire response but preserves them in slog for diagnostics).
	assertStringAttr(t, *errRec, "tenant", "admin@example.com")
	assertAttrAbsent(t, *errRec, "message")
}

// TestWriteError_MaxBytesError_Returns413 verifies the WriteError fast path
// for *http.MaxBytesError surfaced from generated handlers' io.ReadAll on a
// MaxBytesReader-wrapped Body. Without this branch the error falls through
// to the unhandled-error path and the client gets a misleading 500.
// ref: net/http MaxBytesError godoc.
func TestWriteError_MaxBytesError_Returns413(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(context.Background(), rec, &http.MaxBytesError{Limit: 1024})

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	errObj := decodeErrorBody(t, rec)
	assert.Equal(t, string(errcode.ErrBodyTooLarge), errObj["code"])
	assert.Equal(t, "request body too large", errObj["message"])
}

// TestWriteError_WrappedMaxBytesError_Returns413 verifies the same mapping
// holds when the error is wrapped (e.g. fmt.Errorf("decode: %w", err) from
// a handler) — errors.As must traverse the chain.
func TestWriteError_WrappedMaxBytesError_Returns413(t *testing.T) {
	rec := httptest.NewRecorder()
	wrapped := fmt.Errorf("read body: %w", &http.MaxBytesError{Limit: 2048})
	WriteError(context.Background(), rec, wrapped)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	errObj := decodeErrorBody(t, rec)
	assert.Equal(t, string(errcode.ErrBodyTooLarge), errObj["code"])
}

func TestWriteError_PlainErrorMasksResponseAndLogsUnhandled(t *testing.T) {
	handler, restore := installCaptureHandler()
	defer restore()

	rec := httptest.NewRecorder()
	WriteError(context.Background(), rec, errors.New("nil pointer at auth handler"))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	errObj := decodeErrorBody(t, rec)
	assert.Equal(t, string(errcode.ErrInternal), errObj["code"])
	assert.Equal(t, "internal server error", errObj["message"])
	assert.Equal(t, []any{}, errObj["details"])

	assert.NotNil(t, findRecord(handler, slog.LevelError), "plain errors must be logged")
}

func TestWriteError_ClientClosedSetsCancelReasonAndWarns(t *testing.T) {
	handler, restore := installCaptureHandler()
	defer restore()

	ctx := WithCancelReasonSlot(context.Background())
	ctx = withClientErrorLogSamplingEvery(ctx, t.Name(), 1)
	ctx = ctxkeys.WithRequestID(ctx, "req-499")
	err := ctxcancel.Wrap(context.Canceled, "Insert", "key=foo")
	require.NotNil(t, err)

	rec := httptest.NewRecorder()
	WriteError(ctx, rec, err)

	assert.Equal(t, StatusClientClosedRequest, rec.Code)
	assert.Equal(t, ctxcancel.ReasonCanceled, CancelReason(ctx))
	errObj := decodeErrorBody(t, rec)
	assert.Equal(t, string(errcode.ErrClientCanceled), errObj["code"])
	assert.Equal(t, "request canceled", errObj["message"])

	warnRec := findRecord(handler, slog.LevelWarn)
	require.NotNil(t, warnRec)
	assertStringAttr(t, *warnRec, "code", string(errcode.ErrClientCanceled))
	assertStringAttr(t, *warnRec, "status", "499")
	assertStringAttr(t, *warnRec, "cancel_reason", ctxcancel.ReasonCanceled)
	assertStringAttr(t, *warnRec, "request_id", "req-499")
	assert.Nil(t, findRecord(handler, slog.LevelError), "499 must not emit 5xx error logs")
}

func TestWriteError_DeadlineExceededMasksAsGatewayTimeoutAndLogsReason(t *testing.T) {
	handler, restore := installCaptureHandler()
	defer restore()

	ctx := ctxkeys.WithRequestID(context.Background(), "req-504")
	err := ctxcancel.Wrap(context.DeadlineExceeded, "Query", "id=x")
	require.NotNil(t, err)

	rec := httptest.NewRecorder()
	WriteError(ctx, rec, err)

	assert.Equal(t, http.StatusGatewayTimeout, rec.Code)
	errObj := decodeErrorBody(t, rec)
	assert.Equal(t, string(errcode.ErrServerTimeout), errObj["code"])
	assert.Equal(t, "gateway timeout", errObj["message"])
	assert.Equal(t, []any{}, errObj["details"])

	errRec := findRecord(handler, slog.LevelError)
	require.NotNil(t, errRec)
	assertStringAttr(t, *errRec, "code", string(errcode.ErrServerTimeout))
	assertStringAttr(t, *errRec, "public_code", string(errcode.ErrServerTimeout))
	assertStringAttr(t, *errRec, "status", "504")
	assertStringAttr(t, *errRec, "cancel_reason", ctxcancel.ReasonDeadlineExceeded)
	assertStringAttr(t, *errRec, "request_id", "req-504")
}

func TestWriteJSON_EncodeFail(t *testing.T) {
	handler, restore := installCaptureHandler()
	defer restore()

	WriteJSON(erroringResponseWriter{}, http.StatusOK, map[string]any{"ch": make(chan int)})

	assert.NotNil(t, findRecord(handler, slog.LevelError))
}

func TestWriteError_EncodeFail(t *testing.T) {
	handler, restore := installCaptureHandler()
	defer restore()

	WriteError(context.Background(), erroringResponseWriter{}, errcode.New(
		errcode.KindNotFound,
		errcode.ErrCellNotFound,
		"cell not found",
	))

	assert.NotNil(t, findRecord(handler, slog.LevelError))
}

type captureHandler struct {
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool {
	return true
}

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler {
	return h
}

func (h *captureHandler) WithGroup(string) slog.Handler {
	return h
}

func installCaptureHandler() (*captureHandler, func()) {
	handler := &captureHandler{}
	orig := slog.Default()
	slog.SetDefault(slog.New(handler))
	return handler, func() { slog.SetDefault(orig) }
}

func decodeErrorBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "response must contain error object")
	return errObj
}

func findRecord(h *captureHandler, level slog.Level) *slog.Record {
	for i := range h.records {
		if h.records[i].Level == level {
			return &h.records[i]
		}
	}
	return nil
}

func countRecords(h *captureHandler, level slog.Level) int {
	count := 0
	for i := range h.records {
		if h.records[i].Level == level {
			count++
		}
	}
	return count
}

func attrValue(r slog.Record, key string) (string, bool) {
	var result string
	var found bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key != key {
			return true
		}
		found = true
		switch a.Value.Kind() {
		case slog.KindString:
			result = a.Value.String()
		case slog.KindInt64:
			result = a.Value.String()
		case slog.KindAny:
			if err, ok := a.Value.Any().(error); ok {
				result = err.Error()
				return false
			}
			result = a.Value.String()
		default:
			result = a.Value.String()
		}
		return false
	})
	return result, found
}

func assertStringAttr(t *testing.T, rec slog.Record, key, want string) {
	t.Helper()
	got, ok := attrValue(rec, key)
	require.True(t, ok, "log record must contain %q attr", key)
	assert.Equal(t, want, got)
}

func assertAttrAbsent(t *testing.T, rec slog.Record, key string) {
	t.Helper()
	_, ok := attrValue(rec, key)
	assert.False(t, ok, "log record must not contain %q attr", key)
}

type erroringResponseWriter struct{}

func (erroringResponseWriter) Header() http.Header {
	return make(http.Header)
}

func (erroringResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}

func (erroringResponseWriter) WriteHeader(int) {}
