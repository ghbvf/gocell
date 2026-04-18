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
// Trust model: by default the tracer continues upstream traces from valid
// inbound headers (trusted-upstream assumption). Use WithTracingOptions to
// configure WithPublicEndpointFn for public-facing endpoints that should
// create new root traces instead of inheriting from untrusted callers.
//
// ref: go-zero — observability wired by default when configured
// ref: otelchi — chi middleware for OpenTelemetry trace propagation
// ref: otelhttp — public endpoint option (WithPublicEndpoint) starts new root with link
func WithTracer(t tracing.Tracer) Option {
	return func(r *Router) {
		r.tracer = t
	}
}

// WithTracingOptions passes additional TracingOption values to the Tracing
// middleware. Use this to configure trust-boundary behavior, e.g.:
//
//	WithTracingOptions(middleware.WithPublicEndpointFn(func(r *http.Request) bool {
//	    return isPublicPath(r.URL.Path)
//	}))
func WithTracingOptions(opts ...middleware.TracingOption) Option {
	return func(r *Router) {
		r.tracingOpts = append(r.tracingOpts, opts...)
	}
}

// WithRequestIDOptions passes additional RequestIDOption values to the
// RequestID middleware for trust-boundary configuration, e.g.:
//
//	WithRequestIDOptions(middleware.WithReqIDPublicEndpointFn(func(r *http.Request) bool {
//	    return isPublicPath(r.URL.Path)
//	}))
func WithRequestIDOptions(opts ...middleware.RequestIDOption) Option {
	return func(r *Router) {
		r.requestIDOpts = append(r.requestIDOpts, opts...)
	}
}

// WithPublicEndpoints declares paths that are public-facing. This is the
// recommended single-point configuration for trust boundary policy:
//   - Auth: these paths bypass JWT verification
//   - Tracing: these paths create new trace roots (ignore upstream traceparent)
//   - RequestID: these paths reject client-supplied X-Request-Id headers
//
// For asymmetric policies (e.g., auth bypass without trace root), use the
// individual WithTracingOptions / WithRequestIDOptions / WithAuthMiddleware
// options instead.
//
// Note: bootstrap.WithPublicEndpoints wraps this option with auth-provider
// discovery logic. Use this router-level option for standalone router usage
// outside bootstrap (e.g., in tests or custom server setups).
//
// ref: go-zero rest/server.go — single-point route group auth config
// ref: otelhttp config.go — WithPublicEndpointFn per-request detection
func WithPublicEndpoints(paths []string) Option {
	return func(r *Router) {
		r.publicEndpoints = paths
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
// A nil cb is rejected by NewE so the circuit breaker is never silently absent
// when the caller intended to enable it.
//
// The Allower interface is defined in runtime/http/middleware.
// Use adapters/circuitbreaker.New() for a sony/gobreaker implementation.
//
// ref: go-zero — circuit breaker as default middleware when configured
// ref: go-kit/kit circuitbreaker — middleware wrapping pattern
// ref: kubernetes/kubernetes apiserver — option fail-fast at startup
func WithCircuitBreaker(cb middleware.Allower) Option {
	return func(r *Router) {
		if cb == nil || middleware.IsTypedNilAllower(cb) {
			r.circuitBreakerNil = true
			return
		}
		r.circuitBreaker = cb
	}
}

// WithAuthMiddleware enables authentication in the default middleware chain
// with an explicitly injected verifier. Complementary to WithPublicEndpoints:
// this option is the primary path for tests and advanced scenarios that must
// inject a specific (e.g. mock) IntentTokenVerifier; WithPublicEndpoints is the
// primary path for production cells that expose a discovered verifier.
//
// When provided, the auth middleware is placed after CircuitBreaker and before
// BodyLimit, so DoS protection (RL/CB) runs before expensive JWT verification.
// Infra endpoints (/healthz, /readyz, /metrics) registered on outerMux are not
// affected — they bypass business-route middleware entirely.
//
// publicEndpoints specifies paths that bypass authentication (path-only match).
// For method-aware bypass, compose via router.WithPublicEndpoints which wires
// a WithPublicEndpointMatcher AuthOption that supersedes this list.
//
// ref: go-kratos/kratos — auth middleware at service level with selector-based bypass
// ref: go-zero — per-route WithJwt() opt-in auth
func WithAuthMiddleware(verifier auth.IntentTokenVerifier, publicEndpoints []string) Option {
	if verifier == nil {
		panic("router: WithAuthMiddleware requires a non-nil IntentTokenVerifier")
	}
	return func(r *Router) {
		r.authVerifier = verifier
		r.authPublicEndpoints = publicEndpoints
	}
}

// WithAuthMetrics sets the AuthMetrics instance used by AuthMiddleware when wired
// via WithAuthMiddleware. When provided, JWT verification outcomes are recorded
// against the shared metrics backend.
func WithAuthMetrics(m *auth.AuthMetrics) Option {
	return func(r *Router) { r.authMetrics = m }
}

// WithPasswordResetExemptEndpoints declares the routes that remain reachable
// when an authenticated token carries password_reset_required=true. Each entry
// must be in "METHOD /path" format; path templates may use {xxx} single-segment
// wildcards (e.g. "POST /api/v1/access/users/{id}/password").
//
// The list is compiled into an auth.WithPasswordResetExemptMatcher option at
// Run() time. If this option is not used, the password-reset gate is
// fail-closed: every authenticated request returns 403 until the user's new
// token no longer carries the claim.
//
// Accepting the list here (rather than hard-coding it in runtime/auth) keeps
// runtime/ free of cell-specific route knowledge — access-core owns the
// identity endpoints and the composition root passes their paths in explicitly.
func WithPasswordResetExemptEndpoints(endpoints []string) Option {
	return func(r *Router) { r.passwordResetExemptEndpoints = endpoints }
}

// WithPasswordResetChangeEndpointHint sets the string emitted in the
// details.change_password_endpoint field of the 403
// ERR_AUTH_PASSWORD_RESET_REQUIRED response body. Purely a client-navigation
// hint — empty value (default) omits the details map entirely.
//
// Declaring the hint here (rather than hard-coding it in runtime/auth) keeps
// runtime/ free of business-level path literals — the composition root that
// knows which endpoint finishes the reset flow is the only place that names
// it.
func WithPasswordResetChangeEndpointHint(hint string) Option {
	return func(r *Router) { r.passwordResetChangeEndpointHint = hint }
}

// WithSecurityHeadersOptions passes additional SecurityHeadersOption values to
// the SecurityHeaders middleware. Use this to configure HSTS directives, e.g.:
//
//	WithSecurityHeadersOptions(
//	    middleware.WithHSTSIncludeSubDomains(),
//	    middleware.WithHSTSPreload(),
//	)
//
// ref: unrolled/secure — configurable HSTS directives via struct fields
func WithSecurityHeadersOptions(opts ...middleware.SecurityHeadersOption) Option {
	return func(r *Router) {
		r.securityHeadersOpts = append(r.securityHeadersOpts, opts...)
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
	outerMux          *chi.Mux
	mux               *chi.Mux
	healthHandler     *health.Handler
	metricsCollector  metrics.Collector
	metricsHandler    http.Handler
	tracer            tracing.Tracer
	tracingOpts       []middleware.TracingOption
	requestIDOpts     []middleware.RequestIDOption
	rateLimiter       middleware.RateLimiter
	circuitBreaker    middleware.Allower
	circuitBreakerNil bool // set by WithCircuitBreaker(nil) to enable fail-fast in NewE
	authVerifier      auth.IntentTokenVerifier
	// authPublicEndpoints is the legacy path-only list accepted only by direct
	// callers of WithAuthMiddleware. WithPublicEndpoints no longer backfills this
	// field — the method-aware matcher (authPublicMatcher) is the sole source of
	// truth, preventing silent reactivation of path-only bypass if the matcher
	// is removed by future refactors.
	authPublicEndpoints             []string
	authPublicMatcher               func(*http.Request) bool // compiled from publicEndpoints via WithPublicEndpointMatcher
	authMetrics                     *auth.AuthMetrics
	publicEndpoints                 []string
	passwordResetExemptEndpoints    []string                          // raw "METHOD /path" entries; compiled in NewE
	passwordResetExemptMatcher      func(method, urlPath string) bool // compiled matcher fed to AuthMiddleware
	passwordResetChangeEndpointHint string                            // optional details.change_password_endpoint value for 403 body
	securityHeadersOpts             []middleware.SecurityHeadersOption
	bodyLimit                       int64
	trustedProxies                  []string
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

	if err := r.applyPublicEndpoints(); err != nil {
		return nil, err
	}

	if err := r.applyPasswordResetExempts(); err != nil {
		return nil, err
	}

	// Fail-fast: nil circuit breaker means the operator called
	// WithCircuitBreaker(nil) which would silently skip CB installation.
	if r.circuitBreakerNil {
		return nil, fmt.Errorf("router: circuit breaker must not be nil")
	}

	realIPMW, err := r.buildRealIPMiddleware()
	if err != nil {
		return nil, err
	}

	r.buildOuterMux(realIPMW)
	r.buildBusinessMux()

	// Mount business mux on outerMux. Paths not matched by infra routes
	// (/healthz, /readyz, /metrics) fall through to business routes.
	r.outerMux.Mount("/", r.mux)

	return r, nil
}

// buildRealIPMiddleware constructs the RealIP middleware, validating trusted
// proxy configuration eagerly so NewE returns an error on startup rather than
// panicking at request time.
//
// ref: gin-gonic/gin — SetTrustedProxies validates eagerly
func (r *Router) buildRealIPMiddleware() (func(http.Handler) http.Handler, error) {
	if len(r.trustedProxies) > 0 {
		checker, err := middleware.ValidateTrustedProxies(r.trustedProxies)
		if err != nil {
			return nil, fmt.Errorf("router: invalid trusted proxy configuration: %w", err)
		}
		return middleware.RealIPFromChecker(checker), nil
	}
	return middleware.RealIP(nil), nil
}

// buildOuterMux wires shared observability middleware and infra endpoints onto
// r.outerMux. All requests pass through this chain before reaching the
// business mux.
//
// Chain: RequestID → RealIP → Recorder → [Tracing] → AccessLog → [Metrics]
//
//	→ Recovery → SecurityHeaders → [infra routes] → mount(mux)
func (r *Router) buildOuterMux(realIPMW func(http.Handler) http.Handler) {
	r.outerMux.Use(
		middleware.RequestIDWithOptions(r.requestIDOpts...),
		realIPMW,
		middleware.Recorder,
	)
	if r.tracer != nil {
		r.outerMux.Use(middleware.Tracing(r.tracer, r.tracingOpts...))
	}
	r.outerMux.Use(middleware.AccessLog)
	if r.metricsCollector != nil {
		r.outerMux.Use(middleware.Metrics(r.metricsCollector))
	}
	r.outerMux.Use(
		middleware.Recovery,
		middleware.SecurityHeadersWithOptions(r.securityHeadersOpts...),
	)

	// Infrastructure endpoints bypass RateLimit and CircuitBreaker.
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
}

// buildBusinessMux wires rate limiter, circuit breaker, auth, and body-limit
// middleware onto r.mux. Cells register their routes on this mux.
//
// Chain: [RateLimit] → [CircuitBreaker] → [Auth] → BodyLimit → handler.
// Recovery + SecurityHeaders are already applied by outerMux.
func (r *Router) buildBusinessMux() {
	if r.rateLimiter != nil {
		r.mux.Use(middleware.RateLimit(r.rateLimiter))
	}
	if r.circuitBreaker != nil {
		r.mux.Use(middleware.CircuitBreaker(r.circuitBreaker))
	}
	if r.authVerifier != nil {
		r.mux.Use(auth.AuthMiddleware(r.authVerifier, r.authPublicEndpoints, r.buildAuthOpts()...))
	}
	r.mux.Use(middleware.BodyLimit(r.bodyLimit))
}

// buildAuthOpts constructs the AuthOption slice for the auth middleware.
// Prefers the compiled method-aware matcher (set by applyPublicEndpoints) over
// the legacy []string path so that direct WithAuthMiddleware callers remain
// unaffected.
func (r *Router) buildAuthOpts() []auth.AuthOption {
	var opts []auth.AuthOption
	if r.authMetrics != nil {
		opts = append(opts, auth.WithMetrics(r.authMetrics))
	}
	// Prefer the compiled method-aware matcher (set by applyPublicEndpoints) over
	// the legacy []string path. Direct callers of WithAuthMiddleware that do NOT
	// call WithPublicEndpoints continue to use the []string path (no regression).
	if r.authPublicMatcher != nil {
		opts = append(opts, auth.WithPublicEndpointMatcher(r.authPublicMatcher))
	}
	if r.passwordResetExemptMatcher != nil {
		opts = append(opts, auth.WithPasswordResetExemptMatcher(r.passwordResetExemptMatcher))
	}
	if r.passwordResetChangeEndpointHint != "" {
		opts = append(opts, auth.WithPasswordResetChangeEndpointHint(r.passwordResetChangeEndpointHint))
	}
	return opts
}

// applyPasswordResetExempts compiles the "METHOD /path" entries supplied via
// WithPasswordResetExemptEndpoints into a matcher consumed by the auth
// middleware. Returns an error if any entry is malformed.
func (r *Router) applyPasswordResetExempts() error {
	if len(r.passwordResetExemptEndpoints) == 0 {
		return nil
	}
	matcher, err := auth.CompilePasswordResetExempts(r.passwordResetExemptEndpoints)
	if err != nil {
		return fmt.Errorf("router: %w", err)
	}
	r.passwordResetExemptMatcher = matcher
	return nil
}

// applyPublicEndpoints derives trust-boundary policy from WithPublicEndpoints:
// compiles the "METHOD /path" entries into a per-request predicate and
// auto-wires tracing, request_id, and auth. Returns an error if any entry is
// malformed (fail-fast; the caller NewE propagates it to Bootstrap.Run).
//
// Semantic note: auth / tracing / request-id share the same isPublic predicate
// because their trust-boundary criteria are currently identical ("is this a
// public-facing edge?"). If these three semantics diverge in the future (e.g.
// a public endpoint that still requires auth), split into independent
// predicates. See backlog T8 PUBLIC-ENDPOINT-STRUCT-MIGRATE-01.
//
// ref: go-zero rest/server.go — single-point route group auth config
// ref: otelhttp config.go — WithPublicEndpointFn per-request detection
func (r *Router) applyPublicEndpoints() error {
	if len(r.publicEndpoints) == 0 {
		return nil
	}

	isPublic, err := middleware.CompilePublicEndpoints(r.publicEndpoints)
	if err != nil {
		return fmt.Errorf("router: %w", err)
	}

	r.tracingOpts = append(r.tracingOpts, middleware.WithPublicEndpointFn(isPublic))
	r.requestIDOpts = append(r.requestIDOpts, middleware.WithReqIDPublicEndpointFn(isPublic))

	// Populate the method-aware matcher for the auth middleware.
	// authPublicEndpoints is NOT backfilled from publicEndpoints — the matcher is
	// the sole source of truth. Passing nil to auth.AuthMiddleware is safe because
	// F-B's panic guard ensures no space-containing legacy entries reach that path,
	// and the matcher supersedes the []string parameter when both are present.
	r.authPublicMatcher = isPublic
	return nil
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
