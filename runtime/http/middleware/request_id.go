// Package middleware provides chi-compatible HTTP middleware for the GoCell framework.
//
// ref: go-kratos/kratos middleware/middleware.go — Middleware func(Handler) Handler chain pattern
// Adopted: standard func(http.Handler) http.Handler signature for chi compatibility.
package middleware

import (
	"crypto/rand"
	"fmt"
	"net/http"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

const headerRequestID = "X-Request-Id"

// RequestID reads the request ID from the X-Request-Id header, or generates a
// new UUID v4 if absent. The ID is stored in the request context via
// ctxkeys.RequestID and bridged to ctxkeys.CorrelationID for cross-service
// tracing correlation. The ID is echoed back in the response header.
const maxRequestIDLen = 128

func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(headerRequestID)
		if id == "" || len(id) > maxRequestIDLen || !isSafeID(id) {
			id = newUUID()
		}
		w.Header().Set(headerRequestID, id)
		ctx := ctxkeys.WithRequestID(r.Context(), id)
		ctx = ctxkeys.WithCorrelationID(ctx, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDOption configures the RequestIDWithOptions middleware.
type RequestIDOption func(*requestIDConfig)

type requestIDConfig struct {
	publicEndpointFn func(*http.Request) bool
}

// WithReqIDPublicEndpointFn sets a per-request function that determines whether
// an endpoint is public-facing. For public endpoints, the client-supplied
// X-Request-Id header is ignored and a fresh UUID is always generated.
// This prevents untrusted callers from injecting arbitrary request IDs.
//
// ref: go-chi/chi — warns to "only use this middleware if you can trust the headers"
// ref: otelhttp — WithPublicEndpointFn pattern for per-request trust decisions
func WithReqIDPublicEndpointFn(fn func(*http.Request) bool) RequestIDOption {
	return func(c *requestIDConfig) { c.publicEndpointFn = fn }
}

// RequestIDWithOptions creates a RequestID middleware with configurable trust
// boundary options. The zero-value config preserves backward-compatible behavior
// (accepts client-supplied X-Request-Id when syntactically safe).
func RequestIDWithOptions(opts ...RequestIDOption) func(http.Handler) http.Handler {
	var cfg requestIDConfig
	for _, o := range opts {
		o(&cfg)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			isPublic := cfg.publicEndpointFn != nil && cfg.publicEndpointFn(r)

			var id string
			if isPublic {
				id = newUUID()
			} else {
				id = r.Header.Get(headerRequestID)
				if id == "" || len(id) > maxRequestIDLen || !isSafeID(id) {
					id = newUUID()
				}
			}

			w.Header().Set(headerRequestID, id)
			ctx := ctxkeys.WithRequestID(r.Context(), id)
			ctx = ctxkeys.WithCorrelationID(ctx, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// isSafeID reports whether s is non-empty and every byte is in the safe set
// for observability IDs: ASCII letters, digits, and the separators ._:/-
// This rejects control characters, whitespace, quotes, brackets and other
// characters that could confuse log parsers or structured output.
func isSafeID(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '_' || c == ':' || c == '/' || c == '-':
		default:
			return false
		}
	}
	return true
}

// newUUID generates a UUID v4 string.
func newUUID() string {
	var buf [16]byte
	_, _ = rand.Read(buf[:])
	buf[6] = (buf[6] & 0x0f) | 0x40 // version 4
	buf[8] = (buf[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		buf[0:4], buf[4:6], buf[6:8], buf[8:10], buf[10:16])
}
