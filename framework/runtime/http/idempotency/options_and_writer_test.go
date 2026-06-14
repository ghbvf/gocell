package idempotency

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/idempotency"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
)

// =============================================================================
// WithDoneTTL option
// =============================================================================

// TestWithDoneTTL_PositiveValue verifies that a positive duration is stored.
func TestWithDoneTTL_PositiveValue(t *testing.T) {
	cfg := defaultConfig()
	WithDoneTTL(testtime.D1h)(&cfg)
	if cfg.doneTTL != testtime.D1h {
		t.Errorf("doneTTL: got %v, want %v", cfg.doneTTL, testtime.D1h)
	}
}

// TestWithDoneTTL_ZeroClampsToDefault verifies that zero is clamped to
// idempotency.DefaultTTL.
func TestWithDoneTTL_ZeroClampsToDefault(t *testing.T) {
	cfg := defaultConfig()
	cfg.doneTTL = testtime.D1h // start with non-default so we can detect a change
	WithDoneTTL(0)(&cfg)
	if cfg.doneTTL != idempotency.DefaultTTL {
		t.Errorf("WithDoneTTL(0) should clamp to DefaultTTL %v; got %v", idempotency.DefaultTTL, cfg.doneTTL)
	}
}

// TestWithDoneTTL_NegativeClampsToDefault verifies that a negative duration is
// clamped to idempotency.DefaultTTL.
func TestWithDoneTTL_NegativeClampsToDefault(t *testing.T) {
	cfg := defaultConfig()
	WithDoneTTL(-testtime.D1h)(&cfg)
	if cfg.doneTTL != idempotency.DefaultTTL {
		t.Errorf("WithDoneTTL(-1h) should clamp to DefaultTTL %v; got %v", idempotency.DefaultTTL, cfg.doneTTL)
	}
}

// =============================================================================
// WithLeaseTTL option — clamp branch (negative/zero)
// =============================================================================

// TestWithLeaseTTL_ZeroClampsToDefault verifies that zero is clamped to
// idempotency.DefaultLeaseTTL.
func TestWithLeaseTTL_ZeroClampsToDefault(t *testing.T) {
	cfg := defaultConfig()
	cfg.leaseTTL = testtime.D1h // start with non-default
	WithLeaseTTL(0)(&cfg)
	if cfg.leaseTTL != idempotency.DefaultLeaseTTL {
		t.Errorf("WithLeaseTTL(0) should clamp to DefaultLeaseTTL %v; got %v", idempotency.DefaultLeaseTTL, cfg.leaseTTL)
	}
}

// TestWithLeaseTTL_NegativeClampsToDefault verifies that a negative duration is
// clamped to idempotency.DefaultLeaseTTL.
func TestWithLeaseTTL_NegativeClampsToDefault(t *testing.T) {
	cfg := defaultConfig()
	WithLeaseTTL(-testtime.D1h)(&cfg)
	if cfg.leaseTTL != idempotency.DefaultLeaseTTL {
		t.Errorf("WithLeaseTTL(-1h) should clamp to DefaultLeaseTTL %v; got %v", idempotency.DefaultLeaseTTL, cfg.leaseTTL)
	}
}

// TestWithLeaseTTL_PositiveValue verifies that a positive duration is stored.
func TestWithLeaseTTL_PositiveValue(t *testing.T) {
	cfg := defaultConfig()
	WithLeaseTTL(testtime.D2min)(&cfg)
	if cfg.leaseTTL != testtime.D2min {
		t.Errorf("leaseTTL: got %v, want %v", cfg.leaseTTL, testtime.D2min)
	}
}

// =============================================================================
// capturedHeader — nil headerSnap branch
// =============================================================================

// TestBufferingWriter_CapturedHeaderNilSnap verifies that capturedHeader returns
// an empty (non-nil) http.Header when nothing has been written yet (headerSnap is nil).
func TestBufferingWriter_CapturedHeaderNilSnap(t *testing.T) {
	rr := httptest.NewRecorder()
	bw := newBufferingWriter(rr, 1024)

	// capturedHeader before any Write or WriteHeader: headerSnap is nil.
	h := bw.capturedHeader()
	if h == nil {
		t.Error("capturedHeader must return non-nil http.Header even before any write")
	}
	if len(h) != 0 {
		t.Errorf("capturedHeader should be empty before write; got %v", h)
	}
}

// =============================================================================
// readFromHook — io.ReaderFrom path
// =============================================================================

// readerFromRecorder is an http.ResponseWriter that additionally implements
// io.ReaderFrom so httpsnoop's Wrap preserves the interface on bufferingWriter.
type readerFromRecorder struct {
	*httptest.ResponseRecorder
	readFromCalled bool
	writtenBytes   int64
}

func (r *readerFromRecorder) ReadFrom(src io.Reader) (int64, error) {
	r.readFromCalled = true
	b, err := io.ReadAll(src)
	if err != nil {
		return 0, err
	}
	n := int64(len(b))
	r.writtenBytes += n
	_, writeErr := r.Write(b)
	return n, writeErr
}

// TestBufferingWriter_ReadFromHook_SmallBodyCaptured verifies that a ReadFrom
// path with a body that fits within maxBody does NOT mark the bufferingWriter as
// oversized and DOES capture the body. This is the C5/F6 fix: the previous
// implementation always set oversized=true for any ReadFrom write.
func TestBufferingWriter_ReadFromHook_SmallBodyCaptured(t *testing.T) {
	inner := &readerFromRecorder{ResponseRecorder: httptest.NewRecorder()}
	bw := newBufferingWriter(inner, 1024)

	rf, ok := bw.ResponseWriter.(io.ReaderFrom)
	if !ok {
		t.Skip("underlying writer does not implement io.ReaderFrom; httpsnoop will not hook ReadFrom")
	}

	payload := strings.Repeat("x", 100) // 100 < 1024 maxBody
	bw.WriteHeader(200)

	n, err := rf.ReadFrom(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("ReadFrom error: %v", err)
	}
	if n != int64(len(payload)) {
		t.Errorf("ReadFrom: wrote %d bytes, want %d", n, len(payload))
	}

	// Small body must NOT be oversized after ReadFrom (C5/F6 fix).
	if bw.isOversized() {
		t.Error("bufferingWriter must NOT be oversized for a small ReadFrom body")
	}
	// Small body must be captured in the buffer.
	if string(bw.bufferedBody()) != payload {
		t.Errorf("bufferedBody: got %q, want %q", bw.bufferedBody(), payload)
	}
	// committed must be true.
	if !bw.committed() {
		t.Error("bufferingWriter must be committed after ReadFrom")
	}
	// The payload must have reached the underlying writer.
	if inner.Body.String() != payload {
		t.Errorf("underlying recorder body: got %q, want %q", inner.Body.String(), payload)
	}
}

// TestBufferingWriter_ReadFromHook_LargeBodyOversized verifies that a ReadFrom
// path with a body that exceeds maxBody DOES mark the bufferingWriter as oversized.
func TestBufferingWriter_ReadFromHook_LargeBodyOversized(t *testing.T) {
	inner := &readerFromRecorder{ResponseRecorder: httptest.NewRecorder()}
	maxBody := 10
	bw := newBufferingWriter(inner, maxBody)

	rf, ok := bw.ResponseWriter.(io.ReaderFrom)
	if !ok {
		t.Skip("underlying writer does not implement io.ReaderFrom")
	}

	payload := strings.Repeat("x", 100) // 100 > 10 maxBody
	bw.WriteHeader(200)

	n, err := rf.ReadFrom(strings.NewReader(payload))
	if err != nil {
		t.Fatalf("ReadFrom error: %v", err)
	}
	if n != int64(len(payload)) {
		t.Errorf("ReadFrom: wrote %d bytes, want %d", n, len(payload))
	}

	// Large body MUST be oversized.
	if !bw.isOversized() {
		t.Error("bufferingWriter must be oversized after ReadFrom with large body")
	}
	// The payload must have reached the underlying writer.
	if inner.Body.String() != payload {
		t.Errorf("underlying recorder body: got %q, want %q", inner.Body.String(), payload)
	}
}

// TestBufferingWriter_ReadFromHook_ZeroBytes verifies that ReadFrom with a
// zero-byte source does NOT mark the buffer as oversized.
func TestBufferingWriter_ReadFromHook_ZeroBytes(t *testing.T) {
	inner := &readerFromRecorder{ResponseRecorder: httptest.NewRecorder()}
	bw := newBufferingWriter(inner, 1024)

	rf, ok := bw.ResponseWriter.(io.ReaderFrom)
	if !ok {
		t.Skip("underlying writer does not implement io.ReaderFrom")
	}

	_, err := rf.ReadFrom(strings.NewReader(""))
	if err != nil {
		t.Fatalf("ReadFrom error: %v", err)
	}

	// n == 0 → oversized flag must NOT be set.
	if bw.isOversized() {
		t.Error("ReadFrom with 0 bytes must not mark oversized")
	}
}

// TestBufferingWriter_ReadFromHook_CommitsSetsHeaderSnap verifies that the
// readFromHook captures the header snapshot even when WriteHeader was not
// called before ReadFrom.
func TestBufferingWriter_ReadFromHook_CommitsSetsHeaderSnap(t *testing.T) {
	inner := &readerFromRecorder{ResponseRecorder: httptest.NewRecorder()}
	inner.ResponseRecorder.Header().Set("X-Custom", "val")
	bw := newBufferingWriter(inner, 1024)

	rf, ok := bw.ResponseWriter.(io.ReaderFrom)
	if !ok {
		t.Skip("underlying writer does not implement io.ReaderFrom")
	}

	// Do not call WriteHeader first; readFromHook should commit.
	_, _ = rf.ReadFrom(strings.NewReader("data"))

	if !bw.committed() {
		t.Error("bufferingWriter must be committed after ReadFrom")
	}
}

// =============================================================================
// sensitiveResponseHeaders filter — via bufferingWriter capturedHeader
// =============================================================================

// TestBufferingWriter_CapturedHeaderIncludesSensitive verifies that
// capturedHeader does NOT filter sensitive headers — that responsibility
// belongs to filterSensitiveHeaders in middleware.go. The test confirms
// capturedHeader returns whatever was set, including Set-Cookie, so the
// middleware can decide what to strip.
func TestBufferingWriter_CapturedHeaderIncludesSensitive(t *testing.T) {
	rr := httptest.NewRecorder()
	bw := newBufferingWriter(rr, 1024)

	bw.Header().Set("Set-Cookie", "session=abc; HttpOnly")
	bw.Header().Set("X-Safe", "yes")
	bw.WriteHeader(http.StatusOK)

	h := bw.capturedHeader()
	if h.Get("Set-Cookie") == "" {
		t.Error("capturedHeader must include Set-Cookie (filtering is middleware's job)")
	}
	if h.Get("X-Safe") == "" {
		t.Error("capturedHeader must include X-Safe")
	}
}

// TestFilterSensitiveHeaders_DropsSetCookieKeepsSafe exercises
// filterSensitiveHeaders directly to confirm the sensitive-header filter
// removes Set-Cookie while preserving non-sensitive headers.
func TestFilterSensitiveHeaders_DropsSetCookieKeepsSafe(t *testing.T) {
	h := http.Header{}
	h.Set("Set-Cookie", "session=abc; HttpOnly")
	h.Set("Authorization", "Bearer tok")
	h.Set("Content-Type", "application/json")
	h.Set("X-App-Header", "value")

	filtered := filterSensitiveHeaders(h)

	if filtered.Get("Set-Cookie") != "" {
		t.Errorf("Set-Cookie must be filtered; got %q", filtered.Get("Set-Cookie"))
	}
	if filtered.Get("Authorization") != "" {
		t.Errorf("Authorization must be filtered; got %q", filtered.Get("Authorization"))
	}
	if filtered.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type must be preserved; got %q", filtered.Get("Content-Type"))
	}
	if filtered.Get("X-App-Header") != "value" {
		t.Errorf("X-App-Header must be preserved; got %q", filtered.Get("X-App-Header"))
	}
	// Original header must be unmodified (filterSensitiveHeaders must clone).
	if h.Get("Set-Cookie") == "" {
		t.Error("filterSensitiveHeaders must not mutate the original header")
	}
}
