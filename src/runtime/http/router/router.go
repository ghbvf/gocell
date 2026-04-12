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
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/http/middleware"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
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

// WithTracer enables distributed tracing middleware using the given Tracer.
// When provided, each request gets a trace span with trace_id and span_id
// propagated through context. The tracing middleware is placed after Recorder
// and before AccessLog so trace IDs appear in access logs.
//
// ref: go-zero — observability wired by default when configured
// ref: otelchi — chi middleware for OpenTelemetry trace propagation
func WithTracer(t tracing.Tracer) Option {
	return func(r *Router) {
		r.tracer = t
	}
}

// WithRateLimiter enables per-IP rate limiting in the default middleware chain.
// When provided, the rate limiter is placed after AccessLog (so rejected
// requests are logged) and before Metrics (so rejections are counted).
// Requests that exceed the rate are rejected with 429 Too Many Requests.
//
// The RateLimiter interface is defined in runtime/http/middleware. Use
// adapters/ratelimit.New() for a token-bucket implementation.
//
// ref: go-zero — rate limiting as default middleware when configured
func WithRateLimiter(rl middleware.RateLimiter) Option {
	return func(r *Router) {
		r.rateLimiter = rl
	}
}

// WithCircuitBreaker enables a circuit breaker in the default middleware chain.
// When provided, the circuit breaker is placed after the rate limiter and
// before Metrics, protecting downstream handlers from cascade failures.
// When the circuit opens, requests are rejected with 503 Service Unavailable.
//
// The CircuitBreakerPolicy interface is defined in runtime/http/middleware.
// Use adapters/circuitbreaker.New() for a sony/gobreaker implementation.
//
// ref: go-zero — circuit breaker as default middleware when configured
// ref: go-kit/kit circuitbreaker — middleware wrapping pattern
func WithCircuitBreaker(cb middleware.CircuitBreakerPolicy) Option {
	return func(r *Router) {
		r.circuitBreaker = cb
	}
}

// WithTrustedProxies configures the set of trusted proxy IPs/CIDRs for
// X-Forwarded-For header processing. Supports both exact IPs ("192.168.1.1")
// and CIDR notation ("10.0.0.0/8"). When nil (default), no proxy is trusted
// and RemoteAddr is always used.
//
// ref: gin-gonic/gin — SetTrustedProxies([]string) with CIDR support
func WithTrustedProxies(proxies []string) Option {
	return func(r *Router) {
		r.trustedProxies = proxies
	}
}

// Router wraps chi.Mux and implements kernel/cell.RouteMux.
type Router struct {
	mux              *chi.Mux
	healthHandler    *health.Handler
	metricsCollector metrics.Collector
	metricsHandler   http.Handler
	tracer           tracing.Tracer
	rateLimiter      middleware.RateLimiter
	circuitBreaker   middleware.CircuitBreakerPolicy
	bodyLimit        int64
	trustedProxies   []string
}

// New creates a Router with default middleware and optional configuration.
// It panics if the configuration is invalid (e.g. bad trusted proxy entries).
// Use NewE for an error-returning variant suitable for managed startup
// sequences like Bootstrap.Run where rollback must be possible.
//
// Default middleware chain (applied in order):
//
//	RequestID → RealIP → Recorder → [Tracing] → AccessLog → [RateLimit] → [CircuitBreaker] → [Metrics] → Recovery → SecurityHeaders → BodyLimit
//
// Infrastructure endpoints (/healthz, /readyz, /metrics) are registered after
// the default middleware chain, so they are subject to the same observability
// pipeline (tracing, access logging, metrics). This is intentional — probe
// traffic is observable by default. To exclude probes from tracing, callers
// can mount them on a separate chi.Mux without the default chain.
func New(opts ...Option) *Router {
	r, err := NewE(opts...)
	if err != nil {
		panic(err.Error())
	}
	return r
}

// NewE creates a Router with default middleware and optional configuration.
// Unlike New, it returns an error instead of panicking on invalid
// configuration, making it suitable for Bootstrap.Run and other managed
// startup sequences where rollback of already-started components is required.
//
// ref: gin-gonic/gin — SetTrustedProxies returns error at config time
// ref: uber-go/fx — startup failures return error, trigger rollback
func NewE(opts ...Option) (*Router, error) {
	r := &Router{
		mux:       chi.NewRouter(),
		bodyLimit: middleware.DefaultBodyLimit,
	}
	for _, o := range opts {
		o(r)
	}

	// Fail-fast: validate and construct the proxy checker once. The validated
	// checker is passed to RealIPFromChecker so proxies are only parsed once.
	//
	// ref: gin-gonic/gin — SetTrustedProxies validates eagerly
	var realIPMW func(http.Handler) http.Handler
	if len(r.trustedProxies) > 0 {
		checker, err := middleware.ValidateTrustedProxies(r.trustedProxies)
		if err != nil {
			return nil, fmt.Errorf("router: invalid trusted proxy configuration: %w", err)
		}
		realIPMW = middleware.RealIPFromChecker(checker)
	} else {
		realIPMW = middleware.RealIP(nil)
	}

	// Default middleware chain — Recorder before AccessLog/Metrics,
	// Recovery after them so panic-recovered 500s are observable.
	r.mux.Use(
		middleware.RequestID,
		realIPMW,
		middleware.Recorder,
	)

	// Tracing (if configured) — after Recorder so it reuses RecorderState,
	// before AccessLog so trace_id is available in log output.
	if r.tracer != nil {
		r.mux.Use(middleware.Tracing(r.tracer))
	}

	r.mux.Use(middleware.AccessLog)

	// Rate limiter (if configured) — after AccessLog so rejected requests
	// are logged, after RealIP so the limiter sees the real client IP.
	if r.rateLimiter != nil {
		r.mux.Use(middleware.RateLimit(r.rateLimiter))
	}

	// Circuit breaker (if configured) — after RateLimit (rate-limited
	// requests don't count toward breaker failures), before Recovery.
	if r.circuitBreaker != nil {
		r.mux.Use(middleware.CircuitBreaker(r.circuitBreaker))
	}

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

	return r, nil
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
