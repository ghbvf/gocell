// Package middleware provides chi-compatible HTTP middleware for the GoCell framework.
//
// ref: go-kratos/kratos middleware/middleware.go — Middleware func(Handler) Handler chain pattern
// Adopted: standard func(http.Handler) http.Handler signature for chi compatibility.
package middleware

import (
	"net/http"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/idutil"
)

const headerRequestID = "X-Request-Id"

// RequestID reads the request ID from the X-Request-Id header, or generates a
// new UUID v4 if absent. The ID is stored in the request context via
// ctxkeys.RequestID and bridged to ctxkeys.CorrelationID for cross-service
// tracing correlation. The ID is echoed back in the response header.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := requestIDFromHeader(r.Header.Get(headerRequestID))
		if err != nil {
			httputil.WriteDomainError(r.Context(), w, err)
			return
		}
		serveWithRequestID(w, r, next, id)
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
			id, err := requestIDForRequest(r, cfg)
			if err != nil {
				httputil.WriteDomainError(r.Context(), w, err)
				return
			}
			serveWithRequestID(w, r, next, id)
		})
	}
}

func requestIDForRequest(r *http.Request, cfg requestIDConfig) (string, error) {
	if cfg.publicEndpointFn != nil && cfg.publicEndpointFn(r) {
		return newRequestID()
	}
	return requestIDFromHeader(r.Header.Get(headerRequestID))
}

func requestIDFromHeader(id string) (string, error) {
	if id == "" || len(id) > idutil.MaxHTTPIDLen || !idutil.IsSafeID(id) {
		return newRequestID()
	}
	return id, nil
}

func newRequestID() (string, error) {
	id, err := idutil.NewUUID()
	if err != nil {
		return "", errcode.Wrap(errcode.ErrInternal, "request id: generate uuid", err)
	}
	return id, nil
}

func serveWithRequestID(w http.ResponseWriter, r *http.Request, next http.Handler, id string) {
	w.Header().Set(headerRequestID, id)
	ctx := ctxkeys.WithRequestID(r.Context(), id)
	ctx = ctxkeys.WithCorrelationID(ctx, id)
	next.ServeHTTP(w, r.WithContext(ctx))
}
