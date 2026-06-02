package idempotency

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/runtime/auth"
)

// testHandler returns a fixed status+body and sets Content-Type + X-Test headers.
func testHandler(status int, body string) http.Handler { //nolint:unparam // parameter kept for test readability
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("X-Test", "yes")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

// userCtx builds an http.Request with a user Principal embedded in context.
func requestWithUserCtx(method, target, idemKey, tenantID, subject string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	if idemKey != "" {
		r.Header.Set("Idempotency-Key", idemKey)
	}
	ctx := auth.WithPrincipal(r.Context(), &auth.Principal{
		Kind:     auth.PrincipalUser,
		Subject:  subject,
		TenantID: tenantID,
	})
	return r.WithContext(ctx)
}

// — no header passthrough —

func TestMiddleware_NoIdemKeyPassthrough(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	handler := mw(testHandler(200, "ok"))
	r := requestWithUserCtx("POST", "/", "", "tenant1", "user1")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)

	if rr.Code != 200 {
		t.Errorf("code: got %d, want 200", rr.Code)
	}
}

// — GET passthrough —

func TestMiddleware_GETPassthrough(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	handler := mw(testHandler(200, "data"))
	r := requestWithUserCtx("GET", "/", "some-key", "tenant1", "user1")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)

	if rr.Code != 200 {
		t.Errorf("code: got %d, want 200", rr.Code)
	}
	// No idempotency tracking for GET.
	// Second call should also hit handler (not replay).
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, r)
	if rr2.Code != 200 {
		t.Errorf("second GET code: got %d, want 200", rr2.Code)
	}
}

// — service principal passthrough —

func TestMiddleware_ServicePrincipalPassthrough(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	handler := mw(testHandler(200, "service-resp"))
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("Idempotency-Key", "some-key")
	ctx := auth.WithPrincipal(r.Context(), &auth.Principal{
		Kind:         auth.PrincipalService,
		CallerCellID: "mycell",
	})
	r = r.WithContext(ctx)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)

	if rr.Code != 200 {
		t.Errorf("code: got %d, want 200", rr.Code)
	}
}

// — anonymous principal passthrough —

func TestMiddleware_AnonymousPrincipalPassthrough(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	handler := mw(testHandler(200, "anon-resp"))
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("Idempotency-Key", "some-key")
	ctx := auth.WithPrincipal(r.Context(), &auth.Principal{Kind: auth.PrincipalAnonymous})
	r = r.WithContext(ctx)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)

	if rr.Code != 200 {
		t.Errorf("code: got %d, want 200", rr.Code)
	}
}

// — no principal passthrough —

func TestMiddleware_NoPrincipalPassthrough(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	handler := mw(testHandler(200, "no-auth"))
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("Idempotency-Key", "some-key")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)

	if rr.Code != 200 {
		t.Errorf("code: got %d, want 200", rr.Code)
	}
}

// — first POST is recorded —

func TestMiddleware_FirstPOSTRecorded(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":"1"}`)
	})

	handler := mw(inner)
	r := requestWithUserCtx("POST", "/resources", "idem-abc", "tenant1", "user-a")

	// First call.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)
	if rr.Code != 201 {
		t.Errorf("first call code: got %d, want 201", rr.Code)
	}
	if callCount != 1 {
		t.Errorf("handler call count: got %d, want 1", callCount)
	}

	// Second call — must replay without running handler.
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, r)

	if rr2.Code != 201 {
		t.Errorf("replay code: got %d, want 201", rr2.Code)
	}
	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("missing Idempotency-Replayed header; got %q", rr2.Header().Get("Idempotency-Replayed"))
	}
	if callCount != 1 {
		t.Errorf("handler must not be called again; got call count %d", callCount)
	}
	if rr2.Body.String() != `{"id":"1"}` {
		t.Errorf("replayed body: got %q, want %q", rr2.Body.String(), `{"id":"1"}`)
	}
	// Non-sensitive headers must be replayed.
	if rr2.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type not replayed; got %q", rr2.Header().Get("Content-Type"))
	}
}

// — 409 when lease is in progress —

func TestMiddleware_409WhenLeaseInProgress(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	ctx := context.Background()
	// Pre-seed a lease directly via MemStore to simulate in-flight request.
	// Key composed by Middleware: subject + "\x00" + method + "\x00" + path + "\x00" + idemKey.
	_, _, _, err := ms.Claim(ctx, "tenant1", "user-b\x00POST\x00/\x00in-flight", "", idempotency.DefaultLeaseTTL)
	if err != nil {
		t.Fatalf("pre-seed claim: %v", err)
	}

	handler := mw(testHandler(200, "should-not-run"))
	r := requestWithUserCtx("POST", "/", "in-flight", "tenant1", "user-b")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)

	if rr.Code != 409 {
		t.Errorf("code: got %d, want 409", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "ERR_IDEMPOTENCY_IN_PROGRESS") {
		t.Errorf("expected ERR_IDEMPOTENCY_IN_PROGRESS in body; got %q", body)
	}
	retryAfter := rr.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Error("Retry-After header must be set on 409")
	}
	// Retry-After is now a small fixed hint (retryAfterHintSeconds = 5s), not the
	// full lease TTL. Clients should retry soon; the hint avoids a 5-min wait.
	wantRetryAfter := "5"
	if retryAfter != wantRetryAfter {
		t.Errorf("Retry-After: got %q, want %q (small fixed hint, not lease TTL)", retryAfter, wantRetryAfter)
	}
}

// — store error → 500 fail-closed —

// failingStore always returns an error from Claim.
type failingStore struct{}

func (failingStore) Claim(_ context.Context, _, _, _ string, _ time.Duration) (idempotency.ClaimState, *RecordedResponse, Receipt, error) {
	return idempotency.ClaimAcquired, nil, nil, errors.New("store unavailable")
}

func TestMiddleware_StoreError_500FailClosed(t *testing.T) {
	clk := clockmock.New(time.Now())
	mw := Middleware(clk, failingStore{})

	handler := mw(testHandler(200, "should-not-run"))
	r := requestWithUserCtx("POST", "/", "some-key", "tenant1", "user-c")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)

	if rr.Code != 500 {
		t.Errorf("code: got %d, want 500", rr.Code)
	}
}

// — oversize body not recorded —

func TestMiddleware_OversizeBodyNotRecorded(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	maxBody := 10
	mw := Middleware(clk, ms, WithMaxBodyBytes(maxBody))

	bigBody := strings.Repeat("x", 100)
	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(200)
		_, _ = io.WriteString(w, bigBody)
	})

	handler := mw(inner)
	r := requestWithUserCtx("POST", "/big", "oversize-key", "tenant1", "user-d")

	// First call.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)
	if rr.Code != 200 {
		t.Errorf("first call code: got %d, want 200", rr.Code)
	}
	// Check that the full body was still forwarded to the client.
	if rr.Body.String() != bigBody {
		t.Errorf("first call body: got len=%d, want len=%d", len(rr.Body.String()), len(bigBody))
	}

	// Second call should re-run the handler (not replay), because body was not recorded.
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, r)
	if callCount != 2 {
		t.Errorf("second call must re-run handler; got call count %d", callCount)
	}
	if rr2.Header().Get("Idempotency-Replayed") == "true" {
		t.Error("oversize response must not be replayed")
	}
}

// — PUT is also guarded —

func TestMiddleware_PUTIsGuarded(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "put-resp")
	})

	handler := mw(inner)
	r := requestWithUserCtx("PUT", "/resource/1", "put-key", "tenant1", "user-e")

	// First call.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)
	if rr.Code != 200 || callCount != 1 {
		t.Errorf("first PUT: code=%d calls=%d", rr.Code, callCount)
	}

	// Second call — replay.
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, r)
	if callCount != 1 {
		t.Errorf("PUT replay: handler should not be called again; got %d", callCount)
	}
	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Error("Idempotency-Replayed not set on PUT replay")
	}
}

// — no-tenant sentinel namespace —

func TestMiddleware_EmptyTenantUsesNoTenantSentinel(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "ok")
	})

	handler := mw(inner)
	// Subject is set but TenantID is empty.
	r := requestWithUserCtx("POST", "/", "key-notenant", "", "user-f")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)
	if rr.Code != 200 {
		t.Errorf("first call: %d", rr.Code)
	}

	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, r)
	if callCount != 1 {
		t.Errorf("should replay; callCount=%d", callCount)
	}
	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Error("Idempotency-Replayed not set")
	}
}

// — 4xx response is NOT recorded (handler re-runs on retry) —

func TestMiddleware_4xxResponseNotRecorded(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(422)
		_, _ = io.WriteString(w, `{"error":"validation failed"}`)
	})

	handler := mw(inner)
	r := requestWithUserCtx("POST", "/items", "key-4xx", "t1", "user-g")

	// First call: handler returns 422, should NOT be recorded.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)
	if rr.Code != 422 {
		t.Errorf("first call code: got %d, want 422", rr.Code)
	}

	// Second call: lease was released, handler must be called again.
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, r)
	if callCount != 2 {
		t.Errorf("4xx response must not be recorded; callCount=%d, want 2", callCount)
	}
	if rr2.Header().Get("Idempotency-Replayed") == "true" {
		t.Error("Idempotency-Replayed must not be set for non-recorded 4xx response")
	}
}

// — PATCH and DELETE are guarded (table-driven) —

func TestMiddleware_PATCHAndDELETEAreGuarded(t *testing.T) {
	cases := []struct {
		method string
	}{
		{"PATCH"},
		{"DELETE"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.method, func(t *testing.T) {
			clk := clockmock.New(time.Now())
			ms := NewMemStore(clk)
			mw := Middleware(clk, ms)

			callCount := 0
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				callCount++
				w.WriteHeader(200)
				_, _ = io.WriteString(w, "resp")
			})

			handler := mw(inner)
			r := requestWithUserCtx(tc.method, "/resource/1", "key-"+tc.method, "t1", "user-h")

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, r)
			if rr.Code != 200 || callCount != 1 {
				t.Errorf("first %s: code=%d calls=%d", tc.method, rr.Code, callCount)
			}

			rr2 := httptest.NewRecorder()
			handler.ServeHTTP(rr2, r)
			if callCount != 1 {
				t.Errorf("%s replay: handler called again (callCount=%d)", tc.method, callCount)
			}
			if rr2.Header().Get("Idempotency-Replayed") != "true" {
				t.Errorf("%s replay: Idempotency-Replayed not set", tc.method)
			}
		})
	}
}

// — PrincipalUser with empty Subject → passthrough (no idempotency) —

func TestMiddleware_EmptySubjectPassthrough(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(200)
	})

	handler := mw(inner)
	// PrincipalUser with empty Subject.
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("Idempotency-Key", "some-key")
	ctx := auth.WithPrincipal(r.Context(), &auth.Principal{
		Kind:     auth.PrincipalUser,
		Subject:  "", // empty — must bypass idempotency
		TenantID: "t1",
	})
	r = r.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)
	if rr.Code != 200 {
		t.Errorf("code: got %d, want 200", rr.Code)
	}
	// Second call must also hit handler (no replay).
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, r)
	if callCount != 2 {
		t.Errorf("empty subject must bypass idempotency; callCount=%d, want 2", callCount)
	}
}

// — sensitive headers (Set-Cookie) are NOT replayed —

func TestMiddleware_SensitiveHeadersNotReplayed(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Set-Cookie", "session=abc123; Path=/; HttpOnly")
		w.Header().Set("X-Test", "safe-header")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":"2"}`)
	})

	handler := mw(inner)
	r := requestWithUserCtx("POST", "/items", "key-cookie-test", "t1", "user-i")

	// First call.
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)
	if rr.Code != 201 {
		t.Errorf("first call code: got %d, want 201", rr.Code)
	}

	// Second call — replay.
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, r)

	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("expected replay on second call")
	}
	// Set-Cookie MUST NOT be replayed (security: avoid session fixation).
	if rr2.Header().Get("Set-Cookie") != "" {
		t.Errorf("Set-Cookie must not be replayed; got %q", rr2.Header().Get("Set-Cookie"))
	}
	// Non-sensitive headers MUST be replayed.
	if rr2.Header().Get("X-Test") != "safe-header" {
		t.Errorf("X-Test not replayed; got %q", rr2.Header().Get("X-Test"))
	}
	if rr2.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type not replayed; got %q", rr2.Header().Get("Content-Type"))
	}
}

// — 3xx response IS recorded and replayed —

func TestMiddleware_3xxRecordedAndReplayed(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Location", "/new-location")
		w.WriteHeader(303)
	})

	handler := mw(inner)
	r := requestWithUserCtx("POST", "/resource", "key-3xx", "t1", "user-j")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)
	if rr.Code != 303 {
		t.Errorf("first call: got %d, want 303", rr.Code)
	}

	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, r)
	if rr2.Code != 303 {
		t.Errorf("replayed code: got %d, want 303", rr2.Code)
	}
	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Error("Idempotency-Replayed not set on 3xx replay")
	}
	if callCount != 1 {
		t.Errorf("3xx must be replayed without re-running handler; callCount=%d", callCount)
	}
}

// — over-cap Idempotency-Key → 400, handler not called —

func TestMiddleware_OverCapKeyReturns400(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	callCount := 0
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(200)
	}))

	// Key exceeding maxIdempotencyKeyLen (256 bytes).
	overCapKey := strings.Repeat("x", maxIdempotencyKeyLen+1)
	r := requestWithUserCtx("POST", "/", overCapKey, "t1", "user-k")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)

	if rr.Code != 400 {
		t.Errorf("over-cap key: got %d, want 400", rr.Code)
	}
	if callCount != 0 {
		t.Errorf("handler must not be called for over-cap key; got callCount=%d", callCount)
	}
	if !strings.Contains(rr.Body.String(), "ERR_VALIDATION_FAILED") {
		t.Errorf("expected ERR_VALIDATION_FAILED in body; got %q", rr.Body.String())
	}
}

// — WithMaxBodyBytes(0) clamped to default, recording still works —

func TestMiddleware_WithMaxBodyBytes0ClampsToDefault(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	// WithMaxBodyBytes(0) must be clamped to defaultMaxBodyBytes, not disable recording.
	mw := Middleware(clk, ms, WithMaxBodyBytes(0))

	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":"clamped"}`)
	})

	handler := mw(inner)
	r := requestWithUserCtx("POST", "/", "key-clamped", "t1", "user-l")

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)
	if rr.Code != 201 {
		t.Errorf("first call: %d", rr.Code)
	}

	// Second call must replay (recording was not disabled by MaxBodyBytes=0).
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, r)
	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Error("WithMaxBodyBytes(0) must be clamped to default; recording disabled means replay never works")
	}
	if callCount != 1 {
		t.Errorf("handler must not be called twice; callCount=%d", callCount)
	}
}

// testLeaseTTL2m is the custom lease TTL used in TestMiddleware_RetryAfterReflectsLeaseTTL.
// Extracted to a package-level const per TEST-TIME-LITERAL-01 archtest rule.
const testLeaseTTL2m = 2 * time.Minute

// — Retry-After reflects configured lease TTL —

func TestMiddleware_RetryAfterReflectsLeaseTTL(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms, WithLeaseTTL(testLeaseTTL2m))

	ctx := context.Background()
	// Pre-seed a lease with the custom TTL to simulate in-flight request.
	// Key composed by Middleware: subject + "\x00" + method + "\x00" + path + "\x00" + idemKey.
	_, _, _, err := ms.Claim(ctx, "tenant1", "user-m\x00POST\x00/\x00retry-after-key", "", testLeaseTTL2m)
	if err != nil {
		t.Fatalf("pre-seed claim: %v", err)
	}

	handler := mw(testHandler(200, "should-not-run"))
	r := requestWithUserCtx("POST", "/", "retry-after-key", "tenant1", "user-m")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, r)

	if rr.Code != 409 {
		t.Errorf("code: got %d, want 409", rr.Code)
	}
	// Retry-After is now a small fixed hint (retryAfterHintSeconds = 5s) regardless
	// of the configured lease TTL. This avoids long waits for clients.
	want := "5"
	got := rr.Header().Get("Retry-After")
	if got != want {
		t.Errorf("Retry-After: got %q, want %q (small fixed hint, not leaseTTL)", got, want)
	}
}

// — handler panic releases lease —

func TestMiddleware_HandlerPanicReleasesLease(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	firstCall := true
	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if firstCall {
			firstCall = false
			panic("test panic from handler")
		}
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "recovered")
	})

	handler := mw(inner)
	r := requestWithUserCtx("POST", "/", "key-panic", "t1", "user-n")

	// First call: handler panics. The panic propagates; Recovery middleware
	// is NOT installed here, so we catch it manually to keep the test self-contained.
	func() {
		defer func() { recover() }() //nolint:errcheck // intentional: we just need to absorb the panic
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, r)
	}()

	// After the panic, the lease must have been released by the defer in
	// recordOrRelease, so the same key is re-claimable.
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, r)
	if rr2.Code != 200 {
		t.Errorf("post-panic re-claim: code=%d, want 200", rr2.Code)
	}
	if callCount != 2 {
		t.Errorf("after panic, lease must be released so handler re-runs; callCount=%d, want 2", callCount)
	}
}

// ---------------------------------------------------------------------------
// WithExemptMatcher tests (C2)
// ---------------------------------------------------------------------------

// TestMiddleware_ExemptMatcher_ExemptPathNotRecorded verifies that when the
// exempt matcher returns true for a path, the middleware passes through
// without claiming or recording — the handler is called every time even with
// the same Idempotency-Key.
func TestMiddleware_ExemptMatcher_ExemptPathNotRecorded(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)

	exemptPath := "/api/v1/users/abc/password"
	exemptMatcher := func(r *http.Request) bool {
		return r.URL.Path == exemptPath
	}
	mw := Middleware(clk, ms, WithExemptMatcher(exemptMatcher))

	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusOK)
	})

	handler := mw(inner)

	// First request to exempt path.
	r1 := requestWithUserCtx("POST", exemptPath, "idem-key-123", "tenant1", "user-a")
	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, r1)
	if rr1.Code != 200 {
		t.Errorf("first exempt call: code=%d, want 200", rr1.Code)
	}
	if callCount != 1 {
		t.Errorf("first exempt call: handler call count=%d, want 1", callCount)
	}

	// Second request to exempt path with the same Idempotency-Key:
	// handler must be called again (not replayed).
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, r1)
	if rr2.Code != 200 {
		t.Errorf("second exempt call: code=%d, want 200", rr2.Code)
	}
	if callCount != 2 {
		t.Errorf("exempt path must not be recorded; handler must run every time; callCount=%d, want 2", callCount)
	}
	if rr2.Header().Get("Idempotency-Replayed") != "" {
		t.Errorf("Idempotency-Replayed must not be set for exempt paths")
	}
}

// TestMiddleware_ExemptMatcher_NonExemptPathStillRecorded verifies that the
// exempt matcher only bypasses the declared path — non-exempt sibling paths
// still go through the full idempotency flow.
func TestMiddleware_ExemptMatcher_NonExemptPathStillRecorded(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)

	exemptPath := "/api/v1/users/abc/password"
	exemptMatcher := func(r *http.Request) bool {
		return r.URL.Path == exemptPath
	}
	mw := Middleware(clk, ms, WithExemptMatcher(exemptMatcher))

	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusCreated)
	})

	handler := mw(inner)

	// First request to a non-exempt path.
	normalPath := "/api/v1/orders"
	r := requestWithUserCtx("POST", normalPath, "idem-key-order-1", "tenant1", "user-a")

	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, r)
	if rr1.Code != 201 {
		t.Errorf("first non-exempt call: code=%d, want 201", rr1.Code)
	}
	if callCount != 1 {
		t.Errorf("first non-exempt call: handler count=%d, want 1", callCount)
	}

	// Second request with same Idempotency-Key: should be replayed (not invoke handler).
	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, r)
	if rr2.Code != 201 {
		t.Errorf("replayed non-exempt call: code=%d, want 201", rr2.Code)
	}
	if callCount != 1 {
		t.Errorf("non-exempt path must be replayed on second call; handler count=%d, want 1", callCount)
	}
	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("Idempotency-Replayed must be set to true for replayed non-exempt paths; got %q",
			rr2.Header().Get("Idempotency-Replayed"))
	}
}

// TestMiddleware_ExemptMatcher_NilMatcher_AllRoutesTracked verifies that a nil
// exempt matcher (the zero value) is a noop — all qualifying routes continue
// to be tracked as normal.
func TestMiddleware_ExemptMatcher_NilMatcher_AllRoutesTracked(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms, WithExemptMatcher(nil))

	callCount := 0
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusCreated)
	})

	handler := mw(inner)
	r := requestWithUserCtx("POST", "/api/v1/orders", "idem-nil-matcher", "t1", "u1")

	rr1 := httptest.NewRecorder()
	handler.ServeHTTP(rr1, r)
	if rr1.Code != 201 {
		t.Errorf("first call: code=%d, want 201", rr1.Code)
	}

	rr2 := httptest.NewRecorder()
	handler.ServeHTTP(rr2, r)
	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Errorf("nil exempt matcher must not affect recording; Idempotency-Replayed=%q, want true",
			rr2.Header().Get("Idempotency-Replayed"))
	}
	if callCount != 1 {
		t.Errorf("nil exempt matcher must not affect replay; callCount=%d, want 1", callCount)
	}
}

// ---------------------------------------------------------------------------
// WithMetrics / MetricsObserver tests
// ---------------------------------------------------------------------------

// recordingObserver is a simple in-test MetricsObserver that collects every
// RequestState emitted on the hot path. It is intentionally minimal: no
// synchronization (tests are single-goroutine), no deduplication.
type recordingObserver struct{ states []RequestState }

func (r *recordingObserver) ObserveRequest(_ context.Context, s RequestState) {
	r.states = append(r.states, s)
}

// TestMiddleware_Metrics_Acquired verifies that a fresh POST with a recorded
// 2xx response emits exactly [StateAcquired].
func TestMiddleware_Metrics_Acquired(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	obs := &recordingObserver{}
	mw := Middleware(clk, ms, WithMetrics(obs))

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		_, _ = io.WriteString(w, `{"id":"1"}`)
	})

	r := requestWithUserCtx("POST", "/resources", "key-acquired", "t1", "user-1")
	rr := httptest.NewRecorder()
	mw(inner).ServeHTTP(rr, r)

	if rr.Code != 201 {
		t.Fatalf("code: got %d, want 201", rr.Code)
	}
	if len(obs.states) != 1 || obs.states[0] != StateAcquired {
		t.Errorf("states: got %v, want [StateAcquired]", obs.states)
	}
}

// TestMiddleware_Metrics_Replayed verifies that the second identical POST emits
// [StateReplayed].
func TestMiddleware_Metrics_Replayed(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	obs := &recordingObserver{}
	mw := Middleware(clk, ms, WithMetrics(obs))

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "ok")
	})

	r := requestWithUserCtx("POST", "/resources", "key-replay", "t1", "user-2")

	// First call — acquired.
	rr1 := httptest.NewRecorder()
	mw(inner).ServeHTTP(rr1, r)
	obs.states = obs.states[:0] // reset after first call

	// Second call — must be replayed.
	rr2 := httptest.NewRecorder()
	mw(inner).ServeHTTP(rr2, r)

	if rr2.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("expected replay on second call")
	}
	if len(obs.states) != 1 || obs.states[0] != StateReplayed {
		t.Errorf("states: got %v, want [StateReplayed]", obs.states)
	}
}

// TestMiddleware_Metrics_Busy verifies that a ClaimBusy (in-flight lease)
// emits [StateBusy].
func TestMiddleware_Metrics_Busy(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	obs := &recordingObserver{}
	mw := Middleware(clk, ms, WithMetrics(obs))

	ctx := context.Background()
	// Pre-seed a lease to simulate in-flight request.
	_, _, _, err := ms.Claim(ctx, "t1", "user-busy\x00POST\x00/\x00key-busy", "", idempotency.DefaultLeaseTTL)
	if err != nil {
		t.Fatalf("pre-seed claim: %v", err)
	}

	r := requestWithUserCtx("POST", "/", "key-busy", "t1", "user-busy")
	rr := httptest.NewRecorder()
	mw(testHandler(200, "nope")).ServeHTTP(rr, r)

	if rr.Code != 409 {
		t.Fatalf("code: got %d, want 409", rr.Code)
	}
	if len(obs.states) != 1 || obs.states[0] != StateBusy {
		t.Errorf("states: got %v, want [StateBusy]", obs.states)
	}
}

// TestMiddleware_Metrics_StoreError verifies that a Store.Claim error (not a
// fingerprint mismatch) emits [StateStoreError].
func TestMiddleware_Metrics_StoreError(t *testing.T) {
	clk := clockmock.New(time.Now())
	obs := &recordingObserver{}
	mw := Middleware(clk, failingStore{}, WithMetrics(obs))

	r := requestWithUserCtx("POST", "/", "key-store-err", "t1", "user-3")
	rr := httptest.NewRecorder()
	mw(testHandler(200, "nope")).ServeHTTP(rr, r)

	if rr.Code != 500 {
		t.Fatalf("code: got %d, want 500", rr.Code)
	}
	if len(obs.states) != 1 || obs.states[0] != StateStoreError {
		t.Errorf("states: got %v, want [StateStoreError]", obs.states)
	}
}

// TestMiddleware_Metrics_Oversize verifies that an oversized response emits
// both StateAcquired (on initial claim) and StateOversize (on body overflow),
// in that order.
func TestMiddleware_Metrics_Oversize(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	obs := &recordingObserver{}
	mw := Middleware(clk, ms, WithMaxBodyBytes(5), WithMetrics(obs))

	bigBody := strings.Repeat("x", 100)
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, bigBody)
	})

	r := requestWithUserCtx("POST", "/big", "key-oversize", "t1", "user-4")
	rr := httptest.NewRecorder()
	mw(inner).ServeHTTP(rr, r)

	if rr.Code != 200 {
		t.Fatalf("code: got %d, want 200", rr.Code)
	}
	if len(obs.states) != 2 {
		t.Fatalf("states count: got %d, want 2; states=%v", len(obs.states), obs.states)
	}
	if obs.states[0] != StateAcquired {
		t.Errorf("states[0]: got %v, want StateAcquired", obs.states[0])
	}
	if obs.states[1] != StateOversize {
		t.Errorf("states[1]: got %v, want StateOversize", obs.states[1])
	}
}

// fingerprintMismatchStore is a store that returns ErrFingerprintMismatch on
// the first Claim call after a seed, simulating key reuse with a different body.
type fingerprintMismatchStore struct{}

func (fingerprintMismatchStore) Claim(
	_ context.Context, _, _, _ string, _ time.Duration,
) (idempotency.ClaimState, *RecordedResponse, Receipt, error) {
	return 0, nil, nil, ErrFingerprintMismatch
}

// TestMiddleware_Metrics_KeyReused verifies that a fingerprint mismatch
// (same Idempotency-Key, different body) emits [StateKeyReused].
func TestMiddleware_Metrics_KeyReused(t *testing.T) {
	clk := clockmock.New(time.Now())
	obs := &recordingObserver{}
	mw := Middleware(clk, fingerprintMismatchStore{}, WithMetrics(obs))

	r := requestWithUserCtx("POST", "/", "key-reused", "t1", "user-5")
	rr := httptest.NewRecorder()
	mw(testHandler(200, "nope")).ServeHTTP(rr, r)

	if rr.Code != 409 {
		t.Fatalf("code: got %d, want 409", rr.Code)
	}
	if len(obs.states) != 1 || obs.states[0] != StateKeyReused {
		t.Errorf("states: got %v, want [StateKeyReused]", obs.states)
	}
}

// TestMiddleware_Metrics_NilObserver_NoopAndNoPanic verifies that with no
// observer wired (WithMetrics not called), requests succeed and nothing panics.
func TestMiddleware_Metrics_NilObserver_NoopAndNoPanic(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	// No WithMetrics option — nil observer must be safe.
	mw := Middleware(clk, ms)

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "ok")
	})

	r := requestWithUserCtx("POST", "/", "key-noop", "t1", "user-6")
	rr := httptest.NewRecorder()
	// Must not panic.
	mw(inner).ServeHTTP(rr, r)

	if rr.Code != 200 {
		t.Errorf("code: got %d, want 200", rr.Code)
	}
}

// TestMiddleware_Metrics_WithNilArg_NoopAndNoPanic verifies that explicitly
// passing nil to WithMetrics is a safe noop — no panic, no state recorded.
func TestMiddleware_Metrics_WithNilArg_NoopAndNoPanic(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	// WithMetrics(nil) must be a safe noop (typed-nil check).
	mw := Middleware(clk, ms, WithMetrics(nil))

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
	})

	r := requestWithUserCtx("POST", "/", "key-nil-obs", "t1", "user-7")
	rr := httptest.NewRecorder()
	mw(inner).ServeHTTP(rr, r)

	if rr.Code != 201 {
		t.Errorf("code: got %d, want 201", rr.Code)
	}
}

// TestMiddleware_ExemptMatcher_ShortCircuitsBeforeBodyRead verifies that the
// exempt check runs before the body is read — the request body is available
// to the handler intact (not consumed by the middleware body fingerprinting).
func TestMiddleware_ExemptMatcher_ShortCircuitsBeforeBodyRead(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)

	exemptMatcher := func(r *http.Request) bool { return true }
	mw := Middleware(clk, ms, WithExemptMatcher(exemptMatcher))

	bodyReceived := ""
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodyReceived = string(b)
		w.WriteHeader(http.StatusOK)
	})

	handler := mw(inner)

	req := httptest.NewRequest("POST", "/api/v1/any", strings.NewReader("hello-body"))
	req.Header.Set("Idempotency-Key", "some-key")
	ctx := auth.WithPrincipal(req.Context(), &auth.Principal{
		Kind:    auth.PrincipalUser,
		Subject: "user-1",
	})
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if bodyReceived != "hello-body" {
		t.Errorf("body must be intact for exempt routes (not consumed by middleware); got %q", bodyReceived)
	}
}
