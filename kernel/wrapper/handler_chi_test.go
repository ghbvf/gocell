package wrapper_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// TestHTTPHandler_MultiplePathsWithSameTemplate_CollapseSpanName — two
// different actual URLs matching the same template must produce the same
// span name (route-collapse semantics via the spec.Path template).
func TestHTTPHandler_MultiplePathsWithSameTemplate_CollapseSpanName(t *testing.T) {
	tr := &spyTracer{}

	spec := wrapper.ContractSpec{
		ID: "http.user.get.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/users/{id}",
	}
	h := wrapper.HTTPHandler(tr, spec, okHandler(200))

	for _, url := range []string{"/api/v1/users/abc", "/api/v1/users/42"} {
		req := httptest.NewRequest("GET", url, nil)
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.spans) != 2 {
		t.Fatalf("want 2 spans, got %d", len(tr.spans))
	}
	for _, s := range tr.spans {
		if s.name != "GET /api/v1/users/{id}" {
			t.Errorf("span name drift: got %q", s.name)
		}
	}
}

// TestHTTPHandler_Unwrap_PreservesFlusher verifies that statusRecorder
// exposes the inner ResponseWriter via Unwrap so stdlib capability
// discovery (http.NewResponseController) still works.
func TestHTTPHandler_Unwrap_PreservesFlusher(t *testing.T) {
	tr := &spyTracer{}

	var flushable bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		if err := rc.Flush(); err == nil {
			flushable = true
		}
		w.WriteHeader(200)
	})
	h := wrapper.HTTPHandler(tr, loginSpec(), inner)
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !flushable {
		t.Error("Flush() failed — statusRecorder.Unwrap() did not expose inner ResponseWriter")
	}
}

// TestWrapConsumer_InvalidDisposition_MarksErrorWithoutModifyingResult —
// consumer returns zero-value HandleResult (invalid), wrapper still passes
// it through (ConsumerBase will later downgrade to Requeue); wrapper just
// marks span status=Error so ops can see the misbehaviour.
func TestWrapConsumer_InvalidDisposition_MarksErrorWithoutModifyingResult(t *testing.T) {
	tr := &spyTracer{}

	inner := func(ctx context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{} // Disposition == 0 (invalid)
	}
	w := wrapper.WrapConsumer(tr, eventSpec(), inner)
	res := w(context.Background(), outbox.Entry{})

	if res.Disposition != 0 {
		t.Errorf("wrapper must not modify invalid disposition; got %v", res.Disposition)
	}
	if tr.only(t).status != wrapper.StatusError {
		t.Error("invalid disposition should mark span as Error")
	}
}

// TestHTTPHandler_WithFilter_NilKeepsDefault — nil filter option is a no-op.
func TestHTTPHandler_WithFilter_NilKeepsDefault(t *testing.T) {
	tr := &spyTracer{}

	h := wrapper.HTTPHandler(tr, loginSpec(), okHandler(200), wrapper.WithFilter(nil))
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if len(tr.spans) != 1 {
		t.Errorf("want 1 span (default), got %d", len(tr.spans))
	}
}

// TestContractSpec_CommandAndProjection_AreValid — command/projection kinds
// pass Validate without requiring HTTP or event fields.
func TestContractSpec_CommandAndProjection_AreValid(t *testing.T) {
	t.Parallel()
	cases := []string{"command", "projection"}
	for _, k := range cases {
		spec := wrapper.ContractSpec{ID: "x", Kind: k, Transport: "internal"}
		if err := spec.Validate(); err != nil {
			t.Errorf("kind %q should validate: %v", k, err)
		}
	}
}

// TestContractSpec_UnknownKind_Rejected.
func TestContractSpec_UnknownKind_Rejected(t *testing.T) {
	t.Parallel()
	spec := wrapper.ContractSpec{ID: "x", Kind: "grpc", Transport: "h2"}
	if err := spec.Validate(); err == nil {
		t.Error("unknown kind must be rejected")
	}
}

// TestDefaultProbeFilter verifies that DefaultProbeFilter matches only the
// canonical probe paths and ignores others (including query-string variants).
func TestDefaultProbeFilter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path     string
		wantSkip bool
	}{
		{"/healthz", true},
		{"/readyz", true},
		{"/livez", true},
		{"/healthz?verbose=true", true}, // URL.Path strips query string; /healthz is still matched
		{"/api/v1/users", false},
		{"/healthz/extra", false},
		{"/", false},
		{"", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("GET", "http://example.com"+tc.path, nil)
		got := wrapper.DefaultProbeFilter(req)
		if got != tc.wantSkip {
			t.Errorf("DefaultProbeFilter(%q): want %v, got %v", tc.path, tc.wantSkip, got)
		}
	}
}

// TestDefaultProbeFilter_WithHTTPHandler verifies that attaching
// DefaultProbeFilter to HTTPHandler skips span creation for probe paths.
func TestDefaultProbeFilter_WithHTTPHandler(t *testing.T) {
	tr := &spyTracer{}

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	spec := wrapper.ContractSpec{
		ID:        "http.health.v1",
		Kind:      "http",
		Transport: "http",
		Method:    "GET",
		Path:      "/healthz",
	}
	h := wrapper.HTTPHandler(tr, spec, inner, wrapper.WithFilter(wrapper.DefaultProbeFilter))

	req := httptest.NewRequest("GET", "/healthz", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !called {
		t.Fatal("inner handler not called")
	}
	if len(tr.spans) != 0 {
		t.Errorf("expected 0 spans for probe path, got %d", len(tr.spans))
	}
}
