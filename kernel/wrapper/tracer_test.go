package wrapper_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/kernel/wrapper"
)

// TestNoopTracer_Start_ReturnsNonNilSpan ensures the NoopTracer produces a
// usable Span even for a blank context and empty name — callers should never
// need to nil-check the returned span.
func TestNoopTracer_Start_ReturnsNonNilSpan(t *testing.T) {
	t.Parallel()
	var tr wrapper.Tracer = wrapper.NoopTracer{}
	ctx, span := tr.Start(context.Background(), "")
	if span == nil {
		t.Fatal("NoopTracer.Start returned nil span; contract forbids nil")
	}
	if ctx == nil {
		t.Fatal("NoopTracer.Start returned nil context")
	}
	span.SetAttributes(wrapper.Attr{Key: "k", Value: "v"})
	span.RecordError(errors.New("unused"))
	span.SetStatus(wrapper.StatusError, "desc")
	span.End()
}

// TestNoopTracer_Start_ZeroAllocations guards against regressions where the
// noop tracer itself starts allocating. Caller-side variadic slice allocation
// is out of scope — we test the minimal Start/End pair, and SetStatus /
// RecordError with no params.
// (Cannot use t.Parallel() — AllocsPerRun requires exclusive GC access.)
func TestNoopTracer_Start_ZeroAllocations(t *testing.T) {
	ctx := context.Background()
	tr := wrapper.NoopTracer{}
	allocs := testing.AllocsPerRun(100, func() {
		_, span := tr.Start(ctx, "op")
		span.SetStatus(wrapper.StatusOK, "")
		span.End()
	})
	if allocs > 0 {
		t.Fatalf("NoopTracer allocated on hot path: %v allocs/op", allocs)
	}
}

// TestAttr_Zero_IsUsable — Attr is a struct value; zero value must be safe
// to pass through SetAttributes without panics.
func TestAttr_Zero_IsUsable(t *testing.T) {
	t.Parallel()
	var tr wrapper.Tracer = wrapper.NoopTracer{}
	_, span := tr.Start(context.Background(), "op")
	span.SetAttributes(wrapper.Attr{})
	span.End()
}

// TestStatusCode_Constants_AreStable asserts the enum ordinals — callers and
// adapters rely on these as comparison constants.
func TestStatusCode_Constants_AreStable(t *testing.T) {
	t.Parallel()
	if wrapper.StatusOK == wrapper.StatusError {
		t.Fatal("StatusOK and StatusError must be distinct")
	}
	if wrapper.StatusOK != wrapper.StatusCode(0) {
		t.Fatalf("StatusOK must be ordinal 0, got %v", int(wrapper.StatusOK))
	}
}

// TestNoopSpan_DirectMethods — invoke each method through the NoopTracer
// dispatch path to register coverage for the noopSpan singleton methods.
func TestNoopSpan_DirectMethods(t *testing.T) {
	t.Parallel()
	_, span := wrapper.NoopTracer{}.Start(context.Background(), "op")
	// All four methods must be safe no-ops.
	span.SetAttributes(wrapper.Attr{Key: "k", Value: "v"})
	span.RecordError(errors.New("ignored"))
	span.SetStatus(wrapper.StatusError, "desc")
	span.End()
	// Idempotency: calling End + SetAttributes afterwards still must not panic.
	span.SetAttributes(wrapper.Attr{Key: "late", Value: "ok"})
	span.End()
}

// TestWrapperHTTPHandler_PanicsBeforeSetTracer verifies that requesting a
// handler serve without SetTracer panics with a descriptive message.
// Uses ResetTracerForTest (exported via export_test.go) to restore state.
func TestWrapperHTTPHandler_PanicsBeforeSetTracer(t *testing.T) {
	// Reset to unset state before and after.
	wrapper.ResetTracerForTest()
	t.Cleanup(wrapper.ResetTracerForTest)

	h := wrapper.HTTPHandler(wrapper.ContractSpec{
		ID:        "http.auth.login.v1",
		Kind:      "http",
		Transport: "http",
		Method:    "POST",
		Path:      "/api/v1/auth/login",
	}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when tracer is unset")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("expected string panic, got %T: %v", r, r)
		}
		if msg != "kernel/wrapper: tracer not set — runtime.bootstrap must call wrapper.SetTracer before serving" {
			t.Errorf("unexpected panic message: %q", msg)
		}
	}()
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
}

// TestSetTracer_NilPanics verifies SetTracer(nil) panics with a clear message.
func TestSetTracer_NilPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on SetTracer(nil)")
		}
	}()
	wrapper.SetTracer(nil) //nolint:staticcheck // intentional
}
