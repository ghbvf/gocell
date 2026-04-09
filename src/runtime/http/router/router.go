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

// WithMetricsCollector adds the metrics middleware using the given Collector.
// To also serve a /metrics endpoint, use WithMetricsHandler.
func WithMetricsCollector(c metrics.Collector) Option {
	return func(r *Router) {
		r.metricsCollector = c
	}
}

// WithMetricsHandler registers an http.Handler at /metrics (e.g. promhttp handler).
func WithMetricsHandler(h http.Handler) Option {
	return func(r *Router) {
		r.metricsHandler = h
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
	metricsCollector metrics.Collector
	metricsHandler   http.Handler
	bodyLimit        int64
}

// New creates a Router with default middleware and optional configuration.
//
// Default middleware chain (applied in order):
//
//	RequestID → RealIP → Recorder → AccessLog → [Metrics] → Recovery → SecurityHeaders → BodyLimit
//
// Recorder creates the shared RecorderState at the chain head. AccessLog and
// Metrics sit outside Recovery so their post-ServeHTTP code always executes —
// even when Recovery catches a panic and writes a 500 response. This ensures
// panic requests are visible in both logs and metrics.
func New(opts ...Option) *Router {
	r := &Router{
		mux:       chi.NewRouter(),
		bodyLimit: middleware.DefaultBodyLimit,
	}
	for _, o := range opts {
		o(r)
	}

	// Default middleware chain — Recorder before AccessLog/Metrics,
	// Recovery after them so panic-recovered 500s are observable.
	r.mux.Use(
		middleware.RequestID,
		middleware.RealIP(nil),
		middleware.Recorder,
		middleware.AccessLog,
	)

	// Metrics (if configured) — must be before Recovery so panic
	// requests are recorded as status 500.
	if r.metricsCollector != nil {
		r.mux.Use(middleware.Metrics(r.metricsCollector))
	}

	r.mux.Use(
		middleware.Recovery,
		middleware.SecurityHeaders,
		middleware.BodyLimit(r.bodyLimit),
	)

	// Auto-register infrastructure endpoints.
	if r.healthHandler != nil {
		r.mux.Get("/healthz", r.healthHandler.LivezHandler())
		r.mux.Get("/readyz", r.healthHandler.ReadyzHandler())
	}
	// Auto-register /metrics: explicit handler takes precedence, otherwise
	// check if the collector itself can serve metrics (e.g. InMemoryCollector,
	// Prometheus Collector). This preserves backward compatibility — callers
	// that only pass WithMetricsCollector still get /metrics automatically.
	switch {
	case r.metricsHandler != nil:
		r.mux.Handle("/metrics", r.metricsHandler)
	case r.metricsCollector != nil:
		type handlerProvider interface{ Handler() http.Handler }
		if hp, ok := r.metricsCollector.(handlerProvider); ok {
			r.mux.Handle("/metrics", hp.Handler())
		}
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

// With returns a new RouteMux that applies the given middleware to routes
// registered through it, without modifying the receiver. Safe to call
// after routes are registered (unlike chi.Mux.Use which panics).
func (r *Router) With(mw ...func(http.Handler) http.Handler) kcell.RouteMux {
	return &chiRouterAdapter{r.mux.With(mw...)}
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

func (a *chiRouterAdapter) With(mw ...func(http.Handler) http.Handler) kcell.RouteMux {
	return &chiRouterAdapter{a.cr.With(mw...)}
}
