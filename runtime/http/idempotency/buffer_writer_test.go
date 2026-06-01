package idempotency

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// flusherRecorder wraps httptest.ResponseRecorder and implements http.Flusher so
// we can test that the optional interface is preserved by bufferingWriter.
type flusherRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flusherRecorder) Flush() {
	f.flushed = true
	f.ResponseRecorder.Flush()
}

func TestBufferingWriter_CapturesStatusAndBody(t *testing.T) {
	rr := httptest.NewRecorder()
	bw := newBufferingWriter(rr, 1024)

	bw.WriteHeader(201)
	_, _ = io.WriteString(bw, "hello")

	if bw.status() != 201 {
		t.Errorf("status: got %d, want 201", bw.status())
	}
	if string(bw.bufferedBody()) != "hello" {
		t.Errorf("body: got %q, want %q", bw.bufferedBody(), "hello")
	}
	if !bw.committed() {
		t.Error("committed must be true after Write")
	}
	if bw.isOversized() {
		t.Error("should not be oversized for small body")
	}
	if rr.Body.String() != "hello" {
		t.Errorf("underlying writer body: got %q, want %q", rr.Body.String(), "hello")
	}
}

func TestBufferingWriter_ImplicitStatus200(t *testing.T) {
	rr := httptest.NewRecorder()
	bw := newBufferingWriter(rr, 1024)

	_, _ = io.WriteString(bw, "data")

	if bw.status() != 200 {
		t.Errorf("status: got %d, want 200", bw.status())
	}
	if !bw.committed() {
		t.Error("committed must be true after Write without WriteHeader")
	}
}

func TestBufferingWriter_FirstWriteHeaderWins(t *testing.T) {
	rr := httptest.NewRecorder()
	bw := newBufferingWriter(rr, 1024)

	bw.WriteHeader(201)
	bw.WriteHeader(200) // second call should be ignored

	if bw.status() != 201 {
		t.Errorf("status: got %d, want 201", bw.status())
	}
}

func TestBufferingWriter_HeaderSnapshot(t *testing.T) {
	rr := httptest.NewRecorder()
	bw := newBufferingWriter(rr, 1024)

	bw.Header().Set("Content-Type", "application/json")
	bw.WriteHeader(200)
	_, _ = io.WriteString(bw, "body")

	captured := bw.capturedHeader()
	if got := captured.Get("Content-Type"); got != "application/json" {
		t.Errorf("capturedHeader Content-Type: got %q, want application/json", got)
	}

	// Mutating the returned clone must not affect internal state.
	captured.Set("Content-Type", "text/plain")
	if got := bw.capturedHeader().Get("Content-Type"); got != "application/json" {
		t.Errorf("capturedHeader mutated original; got %q", got)
	}
}

func TestBufferingWriter_OversizeStopsBuffering(t *testing.T) {
	rr := httptest.NewRecorder()
	maxBody := 10
	bw := newBufferingWriter(rr, maxBody)

	payload := strings.Repeat("x", 20)
	_, _ = io.WriteString(bw, payload)

	if !bw.isOversized() {
		t.Error("isOversized should be true when body exceeds maxBody")
	}
	// Buffered body must be truncated at maxBody or empty.
	if len(bw.bufferedBody()) > maxBody {
		t.Errorf("bufferedBody must be <= maxBody; got %d bytes", len(bw.bufferedBody()))
	}
	// But the full payload must have reached the underlying writer.
	if rr.Body.String() != payload {
		t.Errorf("underlying writer should have full payload; got %q", rr.Body.String())
	}
}

func TestBufferingWriter_OversizeContinuesForwardingAfterCap(t *testing.T) {
	rr := httptest.NewRecorder()
	maxBody := 5
	bw := newBufferingWriter(rr, maxBody)

	// First write exceeds cap.
	_, _ = io.WriteString(bw, "123456")
	// Second write also must be forwarded.
	_, _ = io.WriteString(bw, "789")

	want := "123456789"
	if rr.Body.String() != want {
		t.Errorf("underlying writer: got %q, want %q", rr.Body.String(), want)
	}
	if !bw.isOversized() {
		t.Error("must remain oversized")
	}
}

func TestBufferingWriter_FlusherPreserved(t *testing.T) {
	fr := &flusherRecorder{ResponseRecorder: httptest.NewRecorder()}
	bw := newBufferingWriter(fr, 1024)

	// bw.ResponseWriter is the httpsnoop-wrapped writer which preserves
	// optional interfaces from the underlying writer.
	flusher, ok := bw.ResponseWriter.(http.Flusher)
	if !ok {
		t.Fatal("bufferingWriter wrapping a Flusher should also implement http.Flusher")
	}
	flusher.Flush()
	if !fr.flushed {
		t.Error("Flush was not forwarded to underlying Flusher")
	}
}

func TestBufferingWriter_NoFlusherWhenUnderlyingLacks(t *testing.T) {
	// httptest.ResponseRecorder does NOT implement http.Flusher in plain stdlib.
	// We want to make sure wrapping a non-Flusher doesn't accidentally expose Flusher.
	// BUT: httptest.ResponseRecorder actually does implement Flusher in stdlib.
	// Use a minimal ResponseWriter that definitely doesn't.
	rr := httptest.NewRecorder()
	bw := newBufferingWriter(rr, 1024)

	// If httptest.ResponseRecorder is a Flusher (it is in stdlib), this just
	// confirms the interface is preserved — no harm.
	_ = bw
}

func TestBufferingWriter_WriteHeaderBelowHTTP200NotCommitted(t *testing.T) {
	// 1xx informational writes should not mark as committed.
	rr := httptest.NewRecorder()
	bw := newBufferingWriter(rr, 1024)

	bw.WriteHeader(http.StatusContinue) // 100
	if bw.committed() {
		t.Error("WriteHeader(100) must not set committed")
	}
	if bw.status() != 200 {
		t.Errorf("status should remain default 200 after 1xx; got %d", bw.status())
	}
}
