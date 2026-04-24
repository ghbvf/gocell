package wrapper_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/kernel/wrapper"
)

// ── Test harness ──────────────────────────────────────────────────────────

// spySpan records attributes / errors / status / end calls for assertions.
// Thread-safe because HTTPHandler may be exercised from parallel requests.
type spySpan struct {
	mu      sync.Mutex
	name    string
	attrs   []wrapper.Attr
	errs    []error
	status  wrapper.StatusCode
	stDesc  string
	ended   bool
	started bool
}

func (s *spySpan) SetAttributes(attrs ...wrapper.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attrs = append(s.attrs, attrs...)
}

func (s *spySpan) RecordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, err)
}

func (s *spySpan) SetStatus(code wrapper.StatusCode, desc string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = code
	s.stDesc = desc
}

func (s *spySpan) SetName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.name = name
}

func (s *spySpan) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
}

func (s *spySpan) attrMap() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]any, len(s.attrs))
	for _, a := range s.attrs {
		out[a.Key] = a.Value
	}
	return out
}

type spyTracer struct {
	mu    sync.Mutex
	spans []*spySpan
}

func (t *spyTracer) Start(ctx context.Context, name string, attrs ...wrapper.Attr) (context.Context, wrapper.Span) {
	s := &spySpan{name: name, started: true}
	if len(attrs) > 0 {
		s.attrs = append(s.attrs, attrs...)
	}
	t.mu.Lock()
	t.spans = append(t.spans, s)
	t.mu.Unlock()
	return ctx, s
}

func (t *spyTracer) only(tb testing.TB) *spySpan {
	tb.Helper()
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.spans) != 1 {
		tb.Fatalf("expected exactly 1 span, got %d", len(t.spans))
	}
	return t.spans[0]
}

// ── Fixture helpers ───────────────────────────────────────────────────────

func loginSpec() wrapper.ContractSpec {
	return wrapper.ContractSpec{
		ID:        "http.auth.login.v1",
		Kind:      "http",
		Transport: "http",
		Method:    "POST",
		Path:      "/api/v1/auth/login",
	}
}

func okHandler(status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	})
}

// setSpyTracer installs tr as the package-level tracer and registers cleanup.
func setSpyTracer(t *testing.T, tr *spyTracer) {
	t.Helper()
	wrapper.SetTracer(tr)
	t.Cleanup(wrapper.ResetTracerForTest)
}

// ── Tests ─────────────────────────────────────────────────────────────────

func TestHTTPHandler_EmitsSpanWithContractAttrs(t *testing.T) {
	tr := &spyTracer{}
	setSpyTracer(t, tr)

	spec := loginSpec()
	h := wrapper.HTTPHandler(spec, okHandler(http.StatusOK))

	req := httptest.NewRequest(spec.Method, spec.Path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	span := tr.only(t)
	attrs := span.attrMap()
	wantAttrs := map[string]any{
		"gocell.contract.id":        spec.ID,
		"gocell.contract.kind":      spec.Kind,
		"gocell.contract.transport": spec.Transport,
		"http.method":               spec.Method,
		"http.route":                spec.Path,
		"http.status_code":          int64(http.StatusOK),
	}
	for k, want := range wantAttrs {
		if got, ok := attrs[k]; !ok || got != want {
			t.Errorf("attr %q: want %v, got %v (present=%v)", k, want, got, ok)
		}
	}
	if span.name != "POST /api/v1/auth/login" {
		t.Errorf("span name: want %q, got %q", "POST /api/v1/auth/login", span.name)
	}
	if !span.ended {
		t.Error("span not Ended()")
	}
	if span.status != wrapper.StatusOK {
		t.Errorf("status: want OK, got %v", span.status)
	}
}

func TestHTTPHandler_MarksErrorOn5xx(t *testing.T) {
	tr := &spyTracer{}
	setSpyTracer(t, tr)

	h := wrapper.HTTPHandler(loginSpec(), okHandler(http.StatusInternalServerError))
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	span := tr.only(t)
	if span.status != wrapper.StatusError {
		t.Errorf("want StatusError on 5xx, got %v", span.status)
	}
	if got := span.attrMap()["http.status_code"]; got != int64(500) {
		t.Errorf("http.status_code: got %v", got)
	}
}

func TestHTTPHandler_DoesNotMarkError_On4xx(t *testing.T) {
	tr := &spyTracer{}
	setSpyTracer(t, tr)

	h := wrapper.HTTPHandler(loginSpec(), okHandler(http.StatusBadRequest))
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	span := tr.only(t)
	// 4xx reflects client error; per otelhttp semantic conventions the server
	// span status stays Unset (here represented by StatusOK default).
	if span.status == wrapper.StatusError {
		t.Error("4xx response should not mark span as error")
	}
}

func TestHTTPHandler_CapturesDefault200WhenHandlerWritesNoStatus(t *testing.T) {
	tr := &spyTracer{}
	setSpyTracer(t, tr)

	// Handler never calls WriteHeader; stdlib defaults to 200 on first Write.
	h := wrapper.HTTPHandler(loginSpec(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hi"))
	}))
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := tr.only(t).attrMap()["http.status_code"]; got != int64(200) {
		t.Errorf("expected default status 200, got %v", got)
	}
}

func TestHTTPHandler_CapturesImplicit200_OnEmptyResponse(t *testing.T) {
	tr := &spyTracer{}
	setSpyTracer(t, tr)

	// Handler never writes body nor header; stdlib still sends 200 OK.
	h := wrapper.HTTPHandler(loginSpec(), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := tr.only(t).attrMap()["http.status_code"]; got != int64(200) {
		t.Errorf("expected implicit 200 on empty response, got %v", got)
	}
}

func TestHTTPHandler_PanicsOnInvalidSpec(t *testing.T) {
	t.Parallel()
	cases := []wrapper.ContractSpec{
		{},                      // all empty
		{ID: "a", Kind: "http"}, // missing transport/method/path
		{ID: "a", Kind: "http", Transport: "http"},                 // missing method/path
		{ID: "a", Kind: "http", Transport: "http", Method: "POST"}, // missing path
	}
	for _, spec := range cases {
		func(s wrapper.ContractSpec) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("expected panic for %+v", s)
				}
			}()
			_ = wrapper.HTTPHandler(s, okHandler(200))
		}(spec)
	}
}

func TestHTTPHandler_PanicsOnNilHandler(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil handler")
		}
	}()
	_ = wrapper.HTTPHandler(loginSpec(), nil)
}

func TestHTTPHandler_Filter_SkipsTracing(t *testing.T) {
	tr := &spyTracer{}
	setSpyTracer(t, tr)

	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(200) })
	h := wrapper.HTTPHandler(loginSpec(), inner,
		wrapper.WithFilter(func(r *http.Request) bool { return true }),
	)
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("inner handler not called when filtered")
	}
	if len(tr.spans) != 0 {
		t.Errorf("expected 0 spans when filter returns true, got %d", len(tr.spans))
	}
}

func TestHTTPHandler_PutsContractIDInContext(t *testing.T) {
	tr := &spyTracer{}
	setSpyTracer(t, tr)

	var seen string
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = wrapper.ContractIDFromContext(r.Context())
		w.WriteHeader(200)
	})
	h := wrapper.HTTPHandler(loginSpec(), inner)
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	if seen != "http.auth.login.v1" {
		t.Errorf("ContractID not propagated; got %q", seen)
	}
}

func TestHTTPHandler_NoopTracer_DoesNotPanic(t *testing.T) {
	wrapper.SetTracer(wrapper.NoopTracer{})
	t.Cleanup(wrapper.ResetTracerForTest)

	h := wrapper.HTTPHandler(loginSpec(), okHandler(200))
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("inner handler didn't run; code=%d", rec.Code)
	}
}

func TestHTTPHandler_RecordsErrorAndStatus_OnHandlerPanic(t *testing.T) {
	tr := &spyTracer{}
	setSpyTracer(t, tr)

	boom := errors.New("boom")
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(boom)
	})
	h := wrapper.HTTPHandler(loginSpec(), inner)
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
	rec := httptest.NewRecorder()

	defer func() {
		// wrapper RE-panics after recording, so outer stack still sees it —
		// middleware chains (Recovery) own the recovery behaviour.
		_ = recover()
		span := tr.only(t)
		if !span.ended {
			t.Error("span not ended on panic")
		}
		if span.status != wrapper.StatusError {
			t.Errorf("want StatusError on panic, got %v", span.status)
		}
		if len(span.errs) == 0 {
			t.Error("RecordError not called on panic")
		}
	}()
	h.ServeHTTP(rec, req)
}

// TestHTTPHandler_PanicsIfTracerNotSet verifies that a request with no tracer
// set results in a descriptive panic.
func TestHTTPHandler_PanicsIfTracerNotSet(t *testing.T) {
	// Ensure tracer is in unset state (reset after test).
	wrapper.ResetTracerForTest()
	t.Cleanup(wrapper.ResetTracerForTest)

	h := wrapper.HTTPHandler(loginSpec(), okHandler(200))
	req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when tracer not set")
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), req)
}

// TestHTTPHandler_ConcurrentRequests_UniqueSpans — 50 parallel requests must
// each produce their own span without data races (run under `go test -race`).
func TestHTTPHandler_ConcurrentRequests_UniqueSpans(t *testing.T) {
	tr := &spyTracer{}
	setSpyTracer(t, tr)

	h := wrapper.HTTPHandler(loginSpec(), okHandler(200))
	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	wg.Wait()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.spans) != N {
		t.Fatalf("expected %d spans, got %d", N, len(tr.spans))
	}
}

// TestStatusRecorder_ConcurrentRequests_NoRace verifies that sync.Once
// protects statusRecorder's status/wroteHeader fields when 100 goroutines
// each make independent requests (run with -race). Each request uses its
// own statusRecorder instance, so this validates the protection under
// concurrent construction+usage, not concurrent access to the same instance.
func TestStatusRecorder_ConcurrentRequests_NoRace(t *testing.T) {
	tr := &spyTracer{}
	setSpyTracer(t, tr)

	var wg sync.WaitGroup
	const N = 100
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			status := 200 + (i % 10)
			h := wrapper.HTTPHandler(loginSpec(), okHandler(status))
			req := httptest.NewRequest("POST", "/api/v1/auth/login", nil)
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
	}
	wg.Wait()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	if len(tr.spans) != N {
		t.Fatalf("expected %d spans, got %d", N, len(tr.spans))
	}
}
