// Package router provides a chi-based HTTP router that implements
// kernel/cell.RouteMux with default middleware and automatic health/metrics
// endpoint registration.
//
// ref: go-chi/chi/v5 — Mux pattern (Group, Mount, Route, Use)
// Adopted: chi.NewRouter as the underlying multiplexer.
// Deviated: wrapped behind kernel/cell.RouteMux interface so Cells remain
// decoupled from any specific router library.
package router

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/http/middleware"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// Compile-time check: Router implements cell.RouteMux.
var _ kcell.RouteMux = (*Router)(nil)

// Option configures a Router.
type Option func(*Router)

// WithHealthHandler registers /healthz and /readyz using the given health.Handler.
func WithHealthHandler(h *health.Handler) Option {
	return func(r *Router) {
		r.healthHandler = h
	}
}

// WithMetricsCollector registers /metrics and adds the metrics middleware.
func WithMetricsCollector(c *metrics.InMemoryCollector) Option {
	return func(r *Router) {
		r.metricsCollector = c
	}
}

// WithBodyLimit overrides the default request body size limit.
func WithBodyLimit(maxBytes int64) Option {
	return func(r *Router) {
		r.bodyLimit = maxBytes
	}
}

// Router wraps chi.Mux and implements kernel/cell.RouteMux.
type Router struct {
	mux              *chi.Mux
	healthHandler    *health.Handler
	metricsCollector *metrics.InMemoryCollector
	bodyLimit        int64
}

// New creates a Router with default middleware and optional configuration.
//
// Default middleware chain (applied in order):
//
//	RequestID -> RealIP -> Recovery -> AccessLog -> SecurityHeaders -> BodyLimit
func New(opts ...Option) *Router {
	r := &Router{
		mux:       chi.NewRouter(),
		bodyLimit: middleware.DefaultBodyLimit,
	}
	for _, o := range opts {
		o(r)
	}

	// Default middleware chain.
	r.mux.Use(
		middleware.RequestID,
		middleware.RealIP(nil),
		middleware.Recovery,
		middleware.AccessLog,
		middleware.SecurityHeaders,
		middleware.BodyLimit(r.bodyLimit),
	)

	// Add metrics middleware if collector provided.
	if r.metricsCollector != nil {
		r.mux.Use(metrics.Middleware(r.metricsCollector))
	}

	// Auto-register infrastructure endpoints.
	if r.healthHandler != nil {
		r.mux.Get("/healthz", r.healthHandler.LivezHandler())
		r.mux.Get("/readyz", r.healthHandler.ReadyzHandler())
	}
	if r.metricsCollector != nil {
		r.mux.Handle("/metrics", r.metricsCollector.Handler())
	}

	return r
}

// Handle registers a handler for the given pattern, implementing cell.RouteMux.
func (r *Router) Handle(pattern string, handler http.Handler) {
	r.mux.Handle(pattern, handler)
}

// Group creates a sub-scope with a shared prefix, implementing cell.RouteMux.
func (r *Router) Group(fn func(kcell.RouteMux)) {
	r.mux.Group(func(cr chi.Router) {
		sub := &chiRouterAdapter{cr}
		fn(sub)
	})
}

// Route mounts a sub-router under the given pattern.
func (r *Router) Route(pattern string, fn func(kcell.RouteMux)) {
	r.mux.Route(pattern, func(cr chi.Router) {
		sub := &chiRouterAdapter{cr}
		fn(sub)
	})
}

// Mount attaches an http.Handler under the given prefix.
func (r *Router) Mount(prefix string, handler http.Handler) {
	r.mux.Mount(prefix, handler)
}

// Use appends middleware to the router's chain.
func (r *Router) Use(mw ...func(http.Handler) http.Handler) {
	r.mux.Use(mw...)
}

// ServeHTTP delegates to the underlying chi.Mux.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

// Handler returns the underlying http.Handler.
func (r *Router) Handler() http.Handler {
	return r.mux
}

// chiRouterAdapter wraps chi.Router to implement cell.RouteMux.
type chiRouterAdapter struct {
	cr chi.Router
}

func (a *chiRouterAdapter) Handle(pattern string, handler http.Handler) {
	a.cr.Handle(pattern, handler)
}

func (a *chiRouterAdapter) Route(pattern string, fn func(kcell.RouteMux)) {
	a.cr.Route(pattern, func(cr chi.Router) {
		sub := &chiRouterAdapter{cr}
		fn(sub)
	})
}

func (a *chiRouterAdapter) Mount(pattern string, handler http.Handler) {
	a.cr.Mount(pattern, handler)
}

func (a *chiRouterAdapter) Group(fn func(kcell.RouteMux)) {
	a.cr.Group(func(cr chi.Router) {
		sub := &chiRouterAdapter{cr}
		fn(sub)
	})
}
