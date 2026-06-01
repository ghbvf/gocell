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

// testHandler returns a fixed status+body and sets a header.
func testHandler(status int, body string) http.Handler { //nolint:unparam // parameter kept for test readability
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
}

// — 409 when lease is in progress —

func TestMiddleware_409WhenLeaseInProgress(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	ctx := context.Background()
	// Pre-seed a lease directly via MemStore to simulate in-flight request.
	_, _, _, err := ms.Claim(ctx, "tenant1", "user-b:in-flight", 5*time.Minute)
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
	if rr.Header().Get("Retry-After") == "" {
		t.Error("Retry-After header must be set on 409")
	}
}

// — store error → 500 fail-closed —

// failingStore always returns an error from Claim.
type failingStore struct{}

func (failingStore) Claim(_ context.Context, _, _ string, _ time.Duration) (idempotency.ClaimState, *RecordedResponse, Receipt, error) {
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
