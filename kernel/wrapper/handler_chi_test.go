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
	t.Parallel()
	tr := &spyTracer{}
	spec := wrapper.ContractSpec{
		ID: "http.user.get.v1", Kind: "http", Transport: "http",
		Method: "GET", Path: "/api/v1/users/{id}",
	}
	h := wrapper.HTTPHandler(spec, okHandler(200), wrapper.WithTracer(tr))

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
	t.Parallel()
	tr := &spyTracer{}
	var flushable bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		if err := rc.Flush(); err == nil {
			flushable = true
		}
		w.WriteHeader(200)
	})
	h := wrapper.HTTPHandler(loginSpec(), inner, wrapper.WithTracer(tr))
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if !flushable {
		t.Error("Flush() failed — statusRecorder.Unwrap() did not expose inner ResponseWriter")
	}
}

// TestWithConsumerSpanNameFormatter_Override exercises the event-side span
// name Option.
func TestWithConsumerSpanNameFormatter_Override(t *testing.T) {
	t.Parallel()
	tr := &spyTracer{}
	inner := func(ctx context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}
	w := wrapper.WrapConsumer(eventSpec(), inner,
		wrapper.WithTracer(tr),
		wrapper.WithConsumerSpanNameFormatter(func(s wrapper.ContractSpec) string {
			return "custom:" + s.ID
		}),
	)
	_ = w(context.Background(), outbox.Entry{})

	if got := tr.only(t).name; got != "custom:event.session.revoked.v1" {
		t.Errorf("want custom name, got %q", got)
	}
}

// TestWrapConsumer_InvalidDisposition_MarksErrorWithoutModifyingResult —
// consumer returns zero-value HandleResult (invalid), wrapper still passes
// it through (ConsumerBase will later downgrade to Requeue); wrapper just
// marks span status=Error so ops can see the misbehaviour.
func TestWrapConsumer_InvalidDisposition_MarksErrorWithoutModifyingResult(t *testing.T) {
	t.Parallel()
	tr := &spyTracer{}
	inner := func(ctx context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{} // Disposition == 0 (invalid)
	}
	w := wrapper.WrapConsumer(eventSpec(), inner, wrapper.WithTracer(tr))
	res := w(context.Background(), outbox.Entry{})

	if res.Disposition != 0 {
		t.Errorf("wrapper must not modify invalid disposition; got %v", res.Disposition)
	}
	if tr.only(t).status != wrapper.StatusError {
		t.Error("invalid disposition should mark span as Error")
	}
}

// TestHTTPHandler_WithTracer_NilUsesDefault — passing nil tracer keeps the
// default NoopTracer in place without panicking.
func TestHTTPHandler_WithTracer_NilUsesDefault(t *testing.T) {
	t.Parallel()
	h := wrapper.HTTPHandler(loginSpec(), okHandler(200), wrapper.WithTracer(nil))
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("inner handler didn't run; got %d", rec.Code)
	}
}

// TestHTTPHandler_WithFilter_NilKeepsDefault — nil filter option is a no-op.
func TestHTTPHandler_WithFilter_NilKeepsDefault(t *testing.T) {
	t.Parallel()
	tr := &spyTracer{}
	h := wrapper.HTTPHandler(loginSpec(), okHandler(200),
		wrapper.WithTracer(tr), wrapper.WithFilter(nil))
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if len(tr.spans) != 1 {
		t.Errorf("want 1 span (default), got %d", len(tr.spans))
	}
}

// TestHTTPHandler_ExtraAttrs_NilReturned_NoPanic — the extra-attrs callback
// may return nil to skip emission; wrapper must tolerate it.
func TestHTTPHandler_ExtraAttrs_NilReturned_NoPanic(t *testing.T) {
	t.Parallel()
	tr := &spyTracer{}
	h := wrapper.HTTPHandler(loginSpec(), okHandler(200),
		wrapper.WithTracer(tr),
		wrapper.WithExtraAttrs(func(_ *http.Request) []wrapper.Attr { return nil }),
	)
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	_ = tr.only(t)
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
