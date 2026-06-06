package httpcookie

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testTTL = 7 * 24 * time.Hour // mirrors accesscore.DefaultRefreshMaxAge

// serve runs h wrapped by Middleware(testTTL) against a request that
// optionally carries an inbound gocell_rt cookie, returning the recorder.
func serve(t *testing.T, inboundCookie string, h http.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/sessions/refresh", nil)
	if inboundCookie != "" {
		req.AddCookie(&http.Cookie{Name: CookieName, Value: inboundCookie})
	}
	rec := httptest.NewRecorder()
	Middleware(testTTL)(h).ServeHTTP(rec, req)
	return rec
}

// findCookie returns the parsed Set-Cookie matching CookieName, or nil.
func findCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	return nil
}

func TestMiddleware_SetOnSuccess(t *testing.T) {
	t.Parallel()
	rec := serve(t, "", func(w http.ResponseWriter, r *http.Request) {
		SetRefresh(r.Context(), "tok-abc")
		w.WriteHeader(http.StatusCreated)
	})

	got := findCookie(rec)
	if got == nil {
		t.Fatalf("expected gocell_rt cookie, got none; headers=%v", rec.Header())
	}
	if got.Value != "tok-abc" {
		t.Errorf("cookie value = %q, want tok-abc", got.Value)
	}
	if !got.HttpOnly {
		t.Error("cookie must be HttpOnly")
	}
	if !got.Secure {
		t.Error("cookie must be Secure")
	}
	if got.SameSite != http.SameSiteStrictMode {
		t.Errorf("SameSite = %v, want Strict", got.SameSite)
	}
	if got.Path != CookiePath {
		t.Errorf("Path = %q, want %q", got.Path, CookiePath)
	}
	if want := int(testTTL.Seconds()); got.MaxAge != want {
		t.Errorf("MaxAge = %d, want %d", got.MaxAge, want)
	}
	// Wire-string assertions per issue spec.
	raw := rec.Header().Get("Set-Cookie")
	for _, want := range []string{"HttpOnly", "Secure", "SameSite=Strict", "Max-Age=604800", "Path=/api/v1/access/sessions"} {
		if !strings.Contains(raw, want) {
			t.Errorf("Set-Cookie %q missing %q", raw, want)
		}
	}
}

func TestMiddleware_ClearOnSuccess(t *testing.T) {
	t.Parallel()
	rec := serve(t, "", func(w http.ResponseWriter, r *http.Request) {
		ClearRefresh(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})

	got := findCookie(rec)
	if got == nil {
		t.Fatalf("expected clearing gocell_rt cookie, got none; headers=%v", rec.Header())
	}
	if got.Value != "" {
		t.Errorf("clear cookie value = %q, want empty", got.Value)
	}
	// Go renders MaxAge<0 as wire "Max-Age=0"; readSetCookies parses it back to -1.
	if got.MaxAge != -1 {
		t.Errorf("parsed MaxAge = %d, want -1 (wire Max-Age=0)", got.MaxAge)
	}
	if raw := rec.Header().Get("Set-Cookie"); !strings.Contains(raw, "Max-Age=0") {
		t.Errorf("clear Set-Cookie %q missing Max-Age=0", raw)
	}
}

func TestMiddleware_NoCookieOn4xx(t *testing.T) {
	t.Parallel()
	rec := serve(t, "", func(w http.ResponseWriter, r *http.Request) {
		SetRefresh(r.Context(), "tok-should-not-appear")
		w.WriteHeader(http.StatusUnauthorized)
	})
	if got := findCookie(rec); got != nil {
		t.Errorf("4xx must not emit Set-Cookie, got %+v", got)
	}
}

func TestMiddleware_IncomingCookie(t *testing.T) {
	t.Parallel()
	var seen string
	rec := serve(t, "inbound-token", func(w http.ResponseWriter, r *http.Request) {
		seen = IncomingRefresh(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	_ = rec
	if seen != "inbound-token" {
		t.Errorf("IncomingRefresh = %q, want inbound-token", seen)
	}
}

func TestMiddleware_NoIncomingCookie(t *testing.T) {
	t.Parallel()
	seen := "sentinel"
	serve(t, "", func(w http.ResponseWriter, r *http.Request) {
		seen = IncomingRefresh(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	if seen != "" {
		t.Errorf("IncomingRefresh with no cookie = %q, want empty", seen)
	}
}

func TestMiddleware_SetViaImplicitWriteHeader(t *testing.T) {
	t.Parallel()
	rec := serve(t, "", func(w http.ResponseWriter, r *http.Request) {
		SetRefresh(r.Context(), "tok-implicit")
		_, _ = w.Write([]byte(`{"data":{}}`)) // implicit WriteHeader(200)
	})
	got := findCookie(rec)
	if got == nil || got.Value != "tok-implicit" {
		t.Fatalf("Write-path must emit Set-Cookie, got %+v", got)
	}
}

func TestDirectiveHelpers_NoMiddlewareNoOp(t *testing.T) {
	t.Parallel()
	// Without Middleware wrapping, the ctx carries no directive; helpers must be
	// safe no-ops (adapter unit tests rely on this).
	ctx := context.Background()
	SetRefresh(ctx, "x") // must not panic
	ClearRefresh(ctx)    // must not panic
	if v := IncomingRefresh(ctx); v != "" {
		t.Errorf("IncomingRefresh on bare ctx = %q, want empty", v)
	}
}
