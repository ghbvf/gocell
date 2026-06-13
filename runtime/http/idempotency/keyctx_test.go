package idempotency

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
)

// TestKeyFromContext_AbsentByDefault — a bare context carries no Idempotency-Key.
func TestKeyFromContext_AbsentByDefault(t *testing.T) {
	if _, ok := KeyFromContext(context.Background()); ok {
		t.Fatal("KeyFromContext should return ok=false on a bare context")
	}
}

// TestWithKey_RoundTrip — WithKey/KeyFromContext are a symmetric accessor pair.
func TestWithKey_RoundTrip(t *testing.T) {
	ctx := WithKey(context.Background(), "abc-123")
	got, ok := KeyFromContext(ctx)
	if !ok || got != "abc-123" {
		t.Fatalf("KeyFromContext = (%q, %v), want (abc-123, true)", got, ok)
	}
}

// TestMiddleware_InjectsKeyIntoDownstreamContext — the HTTP idempotency middleware
// exposes the validated Idempotency-Key to the downstream handler (the HTTP-side
// half of the #1610 bridge), so a producer can map it to a command_id.
func TestMiddleware_InjectsKeyIntoDownstreamContext(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	var seen string
	var seenOK bool
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, seenOK = KeyFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	mw(downstream).ServeHTTP(httptest.NewRecorder(),
		requestWithUserCtx("POST", "/", "idem-xyz", "tenant1", "user1"))

	if !seenOK || seen != "idem-xyz" {
		t.Fatalf("downstream KeyFromContext = (%q, %v), want (idem-xyz, true)", seen, seenOK)
	}
}

// TestMiddleware_NoKeyNoInjection — without an Idempotency-Key header the
// middleware passes through and the downstream handler sees no key.
func TestMiddleware_NoKeyNoInjection(t *testing.T) {
	clk := clockmock.New(time.Now())
	ms := NewMemStore(clk)
	mw := Middleware(clk, ms)

	var seenOK bool
	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, seenOK = KeyFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	mw(downstream).ServeHTTP(httptest.NewRecorder(),
		requestWithUserCtx("POST", "/", "", "tenant1", "user1"))

	if seenOK {
		t.Fatal("downstream must not see an Idempotency-Key when none was sent")
	}
}
