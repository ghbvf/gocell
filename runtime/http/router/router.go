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
	"github.com/ghbvf/gocell/runtime/auth"
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
// Last call wins: if multiple WithHealthHandler options are applied, only the
// final one takes effect. Bootstrap relies on this to apply the framework-managed
// handler after user options.
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
// propagated through context. Inbound W3C `traceparent` headers are extracted
// before span creation, with B3 used only as a fallback. The tracing
// middleware is placed after Recorder and before AccessLog so trace IDs appear
// in access logs.
//
// Trust model: the current implementation unconditionally continues upstream
// traces from valid inbound headers. This assumes a trusted-upstream
// deployment (service-to-service behind a gateway). For public-facing
// endpoints exposed directly to untrusted clients, consider adding a
// trust-boundary middleware or gateway-level header sanitization to prevent
// external callers from injecting arbitrary trace identities.
//
// ref: go-zero — observability wired by default when configured
// ref: otelchi — chi middleware for OpenTelemetry trace propagation
// ref: otelhttp — public endpoint option (WithPublicEndpoint) starts new root with link
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

// WithAuthMiddleware enables authentication in the default middleware chain.
// When provided, the auth middleware is placed after CircuitBreaker and before
// BodyLimit, so DoS protection (RL/CB) runs before expensive JWT verification.
// Infra endpoints (/healthz, /readyz, /metrics) registered on outerMux are not
// affected — they bypass business-route middleware entirely.
//
// publicEndpoints specifies paths that bypass authentication. If nil,
// auth.DefaultPublicEndpoints is used. Callers should include their login and
// token refresh endpoints.
//
// ref: go-kratos/kratos — auth middleware at service level with selector-based bypass
// ref: go-zero — per-route WithJwt() opt-in auth
func WithAuthMiddleware(verifier auth.TokenVerifier, publicEndpoints []string) Option {
	if verifier == nil {
		panic("router: WithAuthMiddleware requires a non-nil TokenVerifier")
	}
	return func(r *Router) {
		r.authVerifier = verifier
		r.authPublicEndpoints = publicEndpoints
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
//
// Internally it uses two chi.Mux instances:
//   - outerMux: shared observability + Recovery + SecurityHeaders + infra endpoints
//   - mux: business routes with optional RL/CB + BodyLimit
//
// outerMux delegates unmatched paths to mux via Mount("/", mux). This ensures
// infra endpoints (/healthz, /readyz, /metrics) bypass RL/CB while business
// routes get the full protection chain.
type Router struct {
	outerMux            *chi.Mux
	mux                 *chi.Mux
	healthHandler       *health.Handler
	metricsCollector    metrics.Collector
	metricsHandler      http.Handler
	tracer              tracing.Tracer
	rateLimiter         middleware.RateLimiter
	circuitBreaker      middleware.CircuitBreakerPolicy
	authVerifier        auth.TokenVerifier
	authPublicEndpoints []string
	bodyLimit           int64
	trustedProxies      []string
}

// New creates a Router with default middleware and optional configuration.
// It panics if the configuration is invalid (e.g. bad trusted proxy entries).
// Use NewE for an error-returning variant suitable for managed startup
// sequences like Bootstrap.Run where rollback must be possible.
//
// The request chain is split across two chi.Mux instances:
//
//	outerMux: RequestID → RealIP → Recorder → [Tracing] → AccessLog → [Metrics] → Recovery → SecurityHeaders
//	  ├── infra routes: /healthz, /readyz, /metrics (bypass RL/CB/Auth)
//	  └── Mount("/", mux)
//	       mux: [RateLimit] → [CircuitBreaker] → [Auth] → BodyLimit → business routes
//
// Infrastructure endpoints are registered on outerMux and get shared
// observability + Recovery + SecurityHeaders but bypass RateLimit,
// CircuitBreaker, and Auth. This prevents overload/auth protection from
// short-circuiting health probes and metric scrapes.
//
// ref: go-zero rest/engine.go — management endpoints on separate handler chain
// ref: Kratos transport/http — middleware split between server and business
func New(opts ...Option) *Router {
	r, err := NewE(opts...)
	if err != nil {
		panic(err)
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
		outerMux:  chi.NewRouter(),
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

	// --- Phase 1: outerMux — shared observability + infra ---
	// All requests pass through: RequestID → RealIP → Recorder → [Tracing]
	// → AccessLog → [Metrics] → Recovery → SecurityHeaders.
	// Metrics is placed before RL/CB so 429/503 short-circuit responses are
	// counted. Recovery + SecurityHeaders apply to all paths.
	r.outerMux.Use(
		middleware.RequestID,
		realIPMW,
		middleware.Recorder,
	)
	if r.tracer != nil {
		r.outerMux.Use(middleware.Tracing(r.tracer))
	}
	r.outerMux.Use(middleware.AccessLog)
	if r.metricsCollector != nil {
		r.outerMux.Use(middleware.Metrics(r.metricsCollector))
	}
	r.outerMux.Use(
		middleware.Recovery,
		middleware.SecurityHeaders,
	)

	// Infrastructure endpoints: registered on outerMux before business mount.
	// They get shared observability + Recovery + SecurityHeaders but NOT
	// RateLimit or CircuitBreaker, so probes and scrapes work during overload.
	//
	// ref: go-zero rest/engine.go — management endpoints on separate chain
	if r.healthHandler != nil {
		r.outerMux.Get("/healthz", r.healthHandler.LivezHandler())
		r.outerMux.Get("/readyz", r.healthHandler.ReadyzHandler())
	}
	// /metrics is only registered when explicitly provided via WithMetricsHandler.
	// WithMetricsCollector enables the metrics middleware (recording) but does NOT
	// expose a /metrics endpoint — adopting the Prometheus/Kratos convention of
	// separating "collect" from "serve".
	if r.metricsHandler != nil {
		r.outerMux.Handle("/metrics", r.metricsHandler)
	}

	// --- Phase 2: mux — business routes with RL/CB/Auth ---
	// Cells register routes on mux via Handle/Route/Mount/Group/With.
	// Business chain: [RateLimit] → [CircuitBreaker] → [Auth] → BodyLimit → handler.
	// Recovery + SecurityHeaders already applied by outerMux.
	if r.rateLimiter != nil {
		r.mux.Use(middleware.RateLimit(r.rateLimiter))
	}
	if r.circuitBreaker != nil {
		r.mux.Use(middleware.CircuitBreaker(r.circuitBreaker))
	}
	if r.authVerifier != nil {
		r.mux.Use(auth.AuthMiddleware(r.authVerifier, r.authPublicEndpoints))
	}
	r.mux.Use(middleware.BodyLimit(r.bodyLimit))

	// Mount business mux on outerMux. Paths not matched by infra routes
	// (/healthz, /readyz, /metrics) fall through to business routes.
	r.outerMux.Mount("/", r.mux)

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

// ServeHTTP delegates to the outer mux (shared observability + infra routes +
// business routes via mount).
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.outerMux.ServeHTTP(w, req)
}

// Handler returns the outer http.Handler (entry point for the full chain).
func (r *Router) Handler() http.Handler {
	return r.outerMux
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
