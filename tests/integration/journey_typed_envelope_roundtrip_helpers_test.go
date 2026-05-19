//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http/httptest"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// wireError is the top-level JSON wrapper emitted by httputil write helpers.
type wireError struct {
	Error wireErrorBody `json:"error"`
}

// wireErrorBody mirrors the v1 error envelope
// contracts/shared/errors/error-response-v1.schema.json.
type wireErrorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Details []json.RawMessage `json:"details"`
}

// callWriteError invokes httputil.WriteError with err and returns the recorder.
func callWriteError(err error) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	httputil.WriteError(context.Background(), w, err)
	return w
}

// mustDecodeWireError asserts the response is JSON and decodes the envelope.
func mustDecodeWireError(
	t interface {
		Helper()
		Fatalf(string, ...any)
	},
	w *httptest.ResponseRecorder,
) wireError {
	t.Helper()
	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Fatalf("expected Content-Type application/json, got %q", contentType)
	}
	var out wireError
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("failed to decode wire error body: %v", err)
	}
	return out
}

// assertHTTPStatus asserts that the recorder has the expected HTTP status.
func assertHTTPStatus(
	t interface {
		Helper()
		Errorf(string, ...any)
	},
	w *httptest.ResponseRecorder,
	want int,
) {
	t.Helper()
	if w.Code != want {
		t.Errorf("HTTP status: want %d, got %d", want, w.Code)
	}
}

// callWriteErrorWithStatus invokes httputil.WriteErrorWithStatus directly.
func callWriteErrorWithStatus(status int, err *errcode.Error) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	httputil.WriteErrorWithStatus(context.Background(), w, status, err)
	return w
}

// callWriteNilResponseInternal invokes httputil.WriteNilResponseInternal.
func callWriteNilResponseInternal() *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	httputil.WriteNilResponseInternal(context.Background(), w)
	return w
}

// callWriteEncodeFaultInternal invokes httputil.WriteEncodeFaultInternal.
func callWriteEncodeFaultInternal() *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	httputil.WriteEncodeFaultInternal(context.Background(), w)
	return w
}
