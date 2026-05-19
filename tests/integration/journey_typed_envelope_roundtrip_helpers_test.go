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
//
// Coverage boundary: exercises the pkg/httputil.WriteError → writeErrcodeError
// path including errcode.Kind dispatch and 5xx details-strip. Does NOT cover
// Span error redaction (kernel/wrapper.WrapConsumer + middleware/Recovery →
// span.RecordError via pkg/redaction.RedactError); that channel is owned by
// independent observability unit tests per ADR
// docs/architecture/202604242030-adr-kernel-wrapper-contract-observability.md §8.
// Also does NOT drive the envelope through a generated accesscore login handler;
// the pkg/httputil seam is the final single source of the typed envelope
// invariant — generated handlers call visit → typed struct → MarshalJSON, all
// of which pass through pkg/httputil. Contract-level end-to-end coverage is
// tracked in backlog JOURNEY-TYPED-ENVELOPE-ROUNDTRIP-CONTRACT-LEVEL-01.
//
// Context lifetime: ctx is context.Background() with no deadline.
// httptest.ResponseRecorder is purely in-process with no I/O, so a deadline is
// unnecessary. If this helper is upgraded to drive a real HTTP server, the
// caller must wrap ctx with a timeout before invoking.
func callWriteError(err error) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	httputil.WriteError(context.Background(), w, err)
	return w
}

// mustDecodeWireError asserts the response is JSON and decodes the envelope.
//
// Coverage boundary: validates the v1 error wire shape
// (contracts/shared/errors/error-response-v1.schema.json) at the
// pkg/httputil layer. Does not re-validate HTTP transport framing or TLS.
//
// Context lifetime: stateless decoder; no ctx dependency.
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
//
// Coverage boundary: checks status code written by pkg/httputil helpers only.
// Does not validate routing-layer or middleware-layer status overrides.
//
// Context lifetime: stateless; no ctx dependency.
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
//
// Coverage boundary: exercises the explicit-status override path in
// pkg/httputil. Does not cover Span error redaction (see callWriteError
// godoc for the redaction boundary note and ADR reference).
//
// Context lifetime: ctx is context.Background() with no deadline;
// httptest.ResponseRecorder is purely in-process.
func callWriteErrorWithStatus(status int, err *errcode.Error) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	httputil.WriteErrorWithStatus(context.Background(), w, status, err)
	return w
}

// callWriteNilResponseInternal invokes httputil.WriteNilResponseInternal.
//
// Coverage boundary: covers the framework 5xx fallback path when a generated
// handler returns a nil typed response (visit not called). Does not cover the
// visit encode-failure path (use callWriteEncodeFaultInternal for that).
//
// Context lifetime: ctx is context.Background() with no deadline;
// httptest.ResponseRecorder is purely in-process.
func callWriteNilResponseInternal() *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	httputil.WriteNilResponseInternal(context.Background(), w)
	return w
}

// callWriteEncodeFaultInternal invokes httputil.WriteEncodeFaultInternal.
//
// Coverage boundary: covers the framework 5xx fallback path when a generated
// handler encounters a visit encode failure (e.g. json.Marshal error on typed
// response). Does not cover the nil-response path (use
// callWriteNilResponseInternal for that).
//
// Context lifetime: ctx is context.Background() with no deadline;
// httptest.ResponseRecorder is purely in-process.
func callWriteEncodeFaultInternal() *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	httputil.WriteEncodeFaultInternal(context.Background(), w)
	return w
}
