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
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/http/middleware"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
)

// Compile-time checks.
var _ kcell.RouteMux = (*Router)(nil)
var _ kcell.AuthRouteDeclarer = (*Router)(nil)

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
// with an explicitly injected verifier. This is the primary path for tests
// and advanced scenarios that must inject a specific (e.g. mock)
// IntentTokenVerifier.
//
// When provided, the auth middleware is placed after CircuitBreaker and before
// BodyLimit, so DoS protection (RL/CB) runs before expensive JWT verification.
// Infra endpoints (/healthz, /readyz, /metrics) registered on outerMux are not
// affected — they bypass business-route middleware entirely.
//
// Public endpoints are declared via auth.Declare with Public:true inside Cell
// RegisterRoutes; FinalizeAuth compiles them into the router's auth predicates.
//
// ref: go-kratos/kratos — auth middleware at service level with selector-based bypass
// ref: go-zero — per-route WithJwt() opt-in auth
func WithAuthMiddleware(verifier auth.IntentTokenVerifier) Option {
	if verifier == nil {
		panic("router: WithAuthMiddleware requires a non-nil IntentTokenVerifier")
	}
	return func(r *Router) {
		r.authVerifier = verifier
	}
}

// WithAuthMetrics sets the AuthMetrics instance used by AuthMiddleware when wired
// via WithAuthMiddleware. When provided, JWT verification outcomes are recorded
// against the shared metrics backend.
func WithAuthMetrics(m *auth.AuthMetrics) Option {
	return func(r *Router) { r.authMetrics = m }
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

// WithInternalPathPrefixGuard registers a guard middleware that is applied to
// every request whose URL path has the given prefix. The canonical use-case is
// protecting all /internal/v1/* endpoints with a service-token or HMAC check.
//
// prefix must be non-empty, start with '/', and end with '/' (e.g.
// "/internal/v1/"). guard must be non-nil. NewE returns an error immediately
// (fail-fast) when either constraint is violated.
//
// The guard is injected into the business mux (r.mux) as a selective
// middleware: requests that match the prefix are wrapped; all others pass
// through unchanged. Infrastructure endpoints (/healthz, /readyz, /metrics)
// are registered on outerMux and are never reached by this guard.
//
// Actual token-validation logic lives in the guard function supplied by the
// caller — this option is purely a wiring point (injection, not policy).
//
// Chain order for business requests:
//
//	RateLimit → CircuitBreaker → Auth (JWT/delegated) → BodyLimit → InternalGuard → handler
//
// Installing this option automatically marks the prefix as "JWT-delegated" in
// AuthMiddleware: requests whose path starts with prefix bypass JWT verification
// entirely and proceed directly to InternalGuard, which becomes the sole
// authentication layer for that prefix. This makes machine-to-machine callers
// (service tokens, HMAC) work without a user JWT.
//
// ref: go-kratos/kratos middleware/selector — default-deny + Option injection
// for per-route middleware application.
func WithInternalPathPrefixGuard(prefix string, guard func(http.Handler) http.Handler) Option {
	return func(r *Router) {
		r.internalGuardPrefix = prefix
		r.internalGuard = guard
		// Auto-delegate JWT for the guard prefix: the guard is the sole auth layer
		// for requests under this prefix. AuthMiddleware will call next directly
		// without verifying any Bearer token, passing the request to InternalGuard.
		// This is set unconditionally here; validateInternalGuard() enforces that
		// prefix is valid before NewE returns.
		r.authDelegatedMatcher = func(req *http.Request) bool {
			return strings.HasPrefix(req.URL.Path, prefix)
		}
	}
}

// WithPolicyCoverageWhitelist sets route patterns exempt from policy coverage
// verification. Entries use "METHOD /path" for exact match or "/path/*" for
// prefix match. Routes on outerMux (healthz, readyz, metrics) are
// automatically excluded.
//
// ref: kubernetes/apiserver — structural injection guarantees authorizer
// presence; GoCell's startup-time verification achieves the same guarantee.
func WithPolicyCoverageWhitelist(patterns []string) Option {
	return func(r *Router) {
		r.policyCoverageWhitelist = append(r.policyCoverageWhitelist, patterns...)
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
	outerMux                   *chi.Mux
	mux                        *chi.Mux
	healthHandler              *health.Handler
	metricsCollector           metrics.Collector
	metricsHandler             http.Handler
	tracer                     tracing.Tracer
	tracingOpts                []middleware.TracingOption
	requestIDOpts              []middleware.RequestIDOption
	rateLimiter                middleware.RateLimiter
	circuitBreaker             middleware.Allower
	circuitBreakerNil          bool // set by WithCircuitBreaker(nil) to enable fail-fast in NewE
	authVerifier               auth.IntentTokenVerifier
	authPublicMatcher          func(*http.Request) bool // compiled by FinalizeAuth from auth.Declare Public metas
	authMetrics                *auth.AuthMetrics
	passwordResetExemptMatcher func(method, urlPath string) bool // compiled by FinalizeAuth from auth.Declare PasswordResetExempt metas
	securityHeadersOpts        []middleware.SecurityHeadersOption
	bodyLimit                  int64
	trustedProxies             []string

	// internalGuardPrefix and internalGuard implement the /internal/v1/* path-prefix
	// guard: any request whose URL path starts with internalGuardPrefix is wrapped
	// by internalGuard before reaching the business handler.
	// Both fields must be set together (validated in NewE).
	internalGuardPrefix string
	internalGuard       func(http.Handler) http.Handler

	// authDelegatedMatcher is a per-request predicate that marks paths where JWT
	// authentication is delegated to a downstream middleware (e.g. the internal
	// guard). When set, AuthMiddleware forwards these requests to next without any
	// JWT check. Auto-populated by WithInternalPathPrefixGuard.
	authDelegatedMatcher func(*http.Request) bool

	// F3 two-stage auth construction fields.
	// declaredAuthMetas accumulates cell.AuthRouteMeta entries forwarded by
	// auth.Declare during Cell RegisterRoutes. FinalizeAuth compiles them into
	// matchers and OR-merges with any legacy option-supplied matchers.
	declaredAuthMetas []kcell.AuthRouteMeta
	// authFinalized is set to true by FinalizeAuth. Any subsequent
	// DeclareAuthMeta call panics; a second FinalizeAuth call returns an error.
	authFinalized bool
	// derivedHint is populated by FinalizeAuth from the first declared
	// POST + PasswordResetExempt meta. Served at request time via
	// WithPasswordResetChangeEndpointHintFn.
	derivedHint string

	// policyCoverageWhitelist holds patterns exempt from policy coverage
	// verification. Populated by WithPolicyCoverageWhitelist.
	policyCoverageWhitelist []string
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

	// Fail-fast: nil circuit breaker means the operator called
	// WithCircuitBreaker(nil) which would silently skip CB installation.
	if r.circuitBreakerNil {
		return nil, fmt.Errorf("router: circuit breaker must not be nil")
	}

	// Fail-fast: validate internal guard configuration when the option was used.
	if err := r.validateInternalGuard(); err != nil {
		return nil, err
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
	// Always pre-append lazy public predicates so Tracing and RequestID
	// honour both the legacy WithPublicEndpoints path and any routes
	// declared later via auth.Declare / FinalizeAuth.
	lazyPublic := func(req *http.Request) bool {
		if r.authPublicMatcher == nil {
			return false
		}
		return r.authPublicMatcher(req)
	}
	// Prepend so user-supplied TracingOptions / RequestIDOptions can still
	// override (last-write-wins semantics for those slices).
	r.tracingOpts = append([]middleware.TracingOption{middleware.WithPublicEndpointFn(lazyPublic)}, r.tracingOpts...)
	r.requestIDOpts = append([]middleware.RequestIDOption{middleware.WithReqIDPublicEndpointFn(lazyPublic)}, r.requestIDOpts...)

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

// buildBusinessMux wires rate limiter, circuit breaker, auth, body-limit, and
// optional internal-path-prefix guard middleware onto r.mux. Cells register
// their routes on this mux.
//
// Chain: [RateLimit] → [CircuitBreaker] → [Auth] → BodyLimit → [InternalGuard] → handler.
// Recovery + SecurityHeaders are already applied by outerMux.
//
// The internal guard is applied as a selective inline middleware: only requests
// whose path starts with internalGuardPrefix are forwarded through the guard
// function; all others are handled directly. This keeps the guard out of the
// global Use() chain to avoid wrapping infrastructure or public endpoints.
func (r *Router) buildBusinessMux() {
	if r.rateLimiter != nil {
		r.mux.Use(middleware.RateLimit(r.rateLimiter))
	}
	if r.circuitBreaker != nil {
		r.mux.Use(middleware.CircuitBreaker(r.circuitBreaker))
	}
	if r.authVerifier != nil {
		r.mux.Use(auth.AuthMiddleware(r.authVerifier, r.buildAuthOpts()...))
	}
	r.mux.Use(middleware.BodyLimit(r.bodyLimit))
	if r.internalGuard != nil {
		prefix := r.internalGuardPrefix
		guard := r.internalGuard
		r.mux.Use(func(next http.Handler) http.Handler {
			guarded := guard(next)
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if strings.HasPrefix(req.URL.Path, prefix) {
					guarded.ServeHTTP(w, req)
					return
				}
				next.ServeHTTP(w, req)
			})
		})
	}
}

// buildAuthOpts constructs the AuthOption slice for the auth middleware.
// All matcher options use lazy closures that read Router fields at request
// time so that matchers finalized by FinalizeAuth (after AuthMiddleware is
// installed in buildBusinessMux) are honoured without reinstalling middleware.
//
// Public endpoints, password-reset exemptions, and delegated paths are all
// populated by FinalizeAuth after each Cell's RegisterRoutes completes.
func (r *Router) buildAuthOpts() []auth.AuthOption {
	opts := []auth.AuthOption{
		auth.WithPublicEndpointMatcher(func(req *http.Request) bool {
			if r.authPublicMatcher == nil {
				return false
			}
			return r.authPublicMatcher(req)
		}),
		auth.WithDelegatedMatcher(func(req *http.Request) bool {
			if r.authDelegatedMatcher == nil {
				return false
			}
			return r.authDelegatedMatcher(req)
		}),
		auth.WithPasswordResetExemptMatcher(func(method, urlPath string) bool {
			if r.passwordResetExemptMatcher == nil {
				return false
			}
			return r.passwordResetExemptMatcher(method, urlPath)
		}),
		auth.WithPasswordResetChangeEndpointHintFn(func() string {
			return r.derivedHint
		}),
	}
	if r.authMetrics != nil {
		opts = append(opts, auth.WithMetrics(r.authMetrics))
	}
	return opts
}

// validateInternalGuard checks that the internal path-prefix guard is either
// absent (both fields zero) or fully specified (valid prefix + non-nil guard).
// Returns an error on any misconfiguration so NewE can fail-fast at startup.
func (r *Router) validateInternalGuard() error {
	if r.internalGuardPrefix == "" && r.internalGuard == nil {
		return nil // option not used — nothing to validate
	}
	if r.internalGuardPrefix == "" {
		return fmt.Errorf("router: internal guard prefix must not be empty")
	}
	if !strings.HasPrefix(r.internalGuardPrefix, "/") {
		return fmt.Errorf("router: internal guard prefix %q must start with '/'", r.internalGuardPrefix)
	}
	if !strings.HasSuffix(r.internalGuardPrefix, "/") {
		return fmt.Errorf("router: internal guard prefix %q must end with '/'", r.internalGuardPrefix)
	}
	if r.internalGuard == nil {
		return fmt.Errorf("router: internal guard must not be nil when prefix %q is set", r.internalGuardPrefix)
	}
	return nil
}

// Handle registers a handler for the given pattern, implementing cell.RouteMux.
func (r *Router) Handle(pattern string, handler http.Handler) {
	r.mux.Handle(pattern, handler)
}

// Group creates a sub-scope with a shared prefix, implementing cell.RouteMux.
func (r *Router) Group(fn func(kcell.RouteMux)) {
	r.mux.Group(func(cr chi.Router) {
		sub := &chiRouterAdapter{cr: cr, declarer: r}
		fn(sub)
	})
}

// Route mounts a sub-router under the given pattern. The adapter carries
// both the chi sub-router and a reference to the Router-rooted declarer so
// that auth.Declare called on a nested sub-mux composes the mount prefix
// with the declared path and forwards AuthRouteMeta to the top-level Router.
func (r *Router) Route(pattern string, fn func(kcell.RouteMux)) {
	r.mux.Route(pattern, func(cr chi.Router) {
		sub := &chiRouterAdapter{cr: cr, prefix: pattern, declarer: r}
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
	return &chiRouterAdapter{cr: r.mux.With(mw...), declarer: r}
}

// ServeHTTP delegates to the outer mux (shared observability + infra routes +
// business routes via mount).
//
// Panics if auth route metadata has been declared but FinalizeAuth has not yet
// been called. This provides a loud, early failure rather than silently
// dropping auth declarations on the first request. Routers with no
// declarations (plain mux, tests, custom compositions) are unaffected.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if len(r.declaredAuthMetas) > 0 && !r.authFinalized {
		panic("router: FinalizeAuth must be called before ServeHTTP when auth route metadata has been declared")
	}
	r.outerMux.ServeHTTP(w, req)
}

// Handler returns the outer http.Handler (entry point for the full chain).
func (r *Router) Handler() http.Handler {
	return r.outerMux
}

// DeclareAuthMeta implements cell.AuthRouteDeclarer. It accumulates auth route
// metadata forwarded by auth.Declare during Cell RegisterRoutes. FinalizeAuth
// compiles the accumulated metas into matchers that the AuthMiddleware reads
// via lazy closures installed in buildAuthOpts.
//
// Panics if called after FinalizeAuth — all declarations must precede the
// compile step. The "FinalizeAuth must run before Listen" contract ensures
// there is a clear happens-before boundary; no mutex is needed.
func (r *Router) DeclareAuthMeta(m kcell.AuthRouteMeta) {
	if r.authFinalized {
		panic(fmt.Sprintf(
			"router: DeclareAuthMeta called after FinalizeAuth — route %s %s must be declared before FinalizeAuth",
			m.Method, m.Path))
	}
	r.declaredAuthMetas = append(r.declaredAuthMetas, m)
}

// FinalizeAuth compiles all accumulated AuthRouteMeta declarations into
// matchers and OR-merges them with any matchers already set by
// WithInternalPathPrefixGuard. Bootstrap calls this after all cells have
// completed RegisterRoutes but before Listen.
//
// Returns an error (not a panic) so Bootstrap can perform a clean rollback:
//   - "router: FinalizeAuth called twice"
//   - "router: duplicate auth declaration METHOD /path"
//
// It is safe to call FinalizeAuth with an empty declaredAuthMetas slice; the
// policy coverage check still runs to catch routes registered without any
// auth.Declare call.
func (r *Router) FinalizeAuth() error {
	if r.authFinalized {
		return fmt.Errorf("router: FinalizeAuth called twice")
	}
	r.authFinalized = true

	if len(r.declaredAuthMetas) > 0 {
		partitioned, err := partitionAuthMetas(r.declaredAuthMetas)
		if err != nil {
			return err
		}

		if err := r.mergePublicMatcher(partitioned.publicEntries); err != nil {
			return err
		}
		if err := r.mergeExemptMatcher(partitioned.exemptEntries); err != nil {
			return err
		}
		r.mergeDelegatedMatcher(partitioned.delegatedMatcher)
		r.deriveHint()

		// Warn when auth declarations exist but no verifier is installed: the
		// Public/Policy/PasswordResetExempt semantics compile successfully but
		// AuthMiddleware is absent so none of the declarations have any effect.
		if r.authVerifier == nil {
			slog.Warn("router: FinalizeAuth compiled route auth declarations but AuthMiddleware is not installed; Public/Policy/PasswordResetExempt declarations will have no effect",
				slog.Int("declared", len(r.declaredAuthMetas)))
		}
	}

	// Policy coverage verification: every business route registered on r.mux
	// must have a corresponding auth.Declare call (Public, Delegated, or with
	// Policy). Routes on outerMux (healthz, readyz, metrics) are excluded.
	routes := r.enumerateBusinessRoutes()
	if err := verifyPolicyCoverage(routes, r.declaredAuthMetas, r.policyCoverageWhitelist); err != nil {
		return fmt.Errorf("router: policy coverage: %w", err)
	}

	return nil
}

// enumerateBusinessRoutes walks the chi business mux and returns all registered
// (method, path) pairs. Infrastructure routes on outerMux (healthz, readyz,
// metrics) are excluded because they are not registered through Cell
// RegisterRoutes.
func (r *Router) enumerateBusinessRoutes() []routeKey {
	var routes []routeKey
	// chi.Walk never returns a non-nil error when the walker callback always
	// returns nil; the error return is part of the interface but unused here.
	_ = chi.Walk(r.mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, routeKey{Method: method, Path: route})
		return nil
	})
	return routes
}

// authMetaPartition holds the categorised results of partitionAuthMetas.
type authMetaPartition struct {
	publicEntries    []string
	exemptEntries    []string
	delegatedMatcher func(*http.Request) bool
}

// partitionAuthMetas deduplicates the metas and splits them into the three
// "METHOD /path" entry slices consumed by the compile helpers, plus a
// delegated per-request predicate (constructed inline because the exempt
// compiler already handles {xxx} wildcards; delegated paths are matched
// exactly for now).
func partitionAuthMetas(metas []kcell.AuthRouteMeta) (authMetaPartition, error) {
	seen := make(map[string]bool, len(metas))
	var p authMetaPartition

	for _, m := range metas {
		key := strings.ToUpper(m.Method) + "\x00" + m.Path
		if seen[key] {
			return authMetaPartition{}, fmt.Errorf("router: duplicate auth declaration %s %s", m.Method, m.Path)
		}
		seen[key] = true

		entry := m.Method + " " + m.Path
		if m.Public {
			p.publicEntries = append(p.publicEntries, entry)
		}
		if m.PasswordResetExempt {
			p.exemptEntries = append(p.exemptEntries, entry)
		}
		if m.Delegated {
			p.delegatedMatcher = buildDelegatedMatcher(p.delegatedMatcher, m.Method, m.Path)
		}
	}
	return p, nil
}

// buildDelegatedMatcher returns a new predicate that returns true when the
// request matches (method, path) OR the previous predicate matches.
func buildDelegatedMatcher(prev func(*http.Request) bool, method, path string) func(*http.Request) bool {
	return func(req *http.Request) bool {
		if prev != nil && prev(req) {
			return true
		}
		return strings.EqualFold(req.Method, method) && req.URL.Path == path
	}
}

// mergePublicMatcher OR-merges the compiled public entries with r.authPublicMatcher.
func (r *Router) mergePublicMatcher(entries []string) error {
	if len(entries) == 0 {
		return nil
	}
	compiled, err := middleware.CompilePublicEndpoints(entries)
	if err != nil {
		return fmt.Errorf("router: FinalizeAuth public entries: %w", err)
	}
	r.authPublicMatcher = orMergeRequest(r.authPublicMatcher, compiled)
	return nil
}

// mergeExemptMatcher OR-merges the compiled exempt entries with r.passwordResetExemptMatcher.
func (r *Router) mergeExemptMatcher(entries []string) error {
	if len(entries) == 0 {
		return nil
	}
	compiled, err := auth.CompilePasswordResetExempts(entries)
	if err != nil {
		return fmt.Errorf("router: FinalizeAuth exempt entries: %w", err)
	}
	r.passwordResetExemptMatcher = orMergeMethodPath(r.passwordResetExemptMatcher, compiled)
	return nil
}

// mergeDelegatedMatcher OR-merges the declared delegated matcher with r.authDelegatedMatcher.
func (r *Router) mergeDelegatedMatcher(declared func(*http.Request) bool) {
	if declared == nil {
		return
	}
	r.authDelegatedMatcher = orMergeRequest(r.authDelegatedMatcher, declared)
}

// deriveHint sets r.derivedHint to the first declared POST+PasswordResetExempt
// meta's METHOD+path (e.g. "POST /api/v1/access/users/{id}/password").
// The hint is served at request time via WithPasswordResetChangeEndpointHintFn.
// The "METHOD /path" format matches the wire contract documented in
// docs/operations/first-run-setup.md (change_password_endpoint field).
func (r *Router) deriveHint() {
	for _, m := range r.declaredAuthMetas {
		if m.Method == "POST" && m.PasswordResetExempt {
			r.derivedHint = m.Method + " " + m.Path
			return
		}
	}
}

// DeclaredAuthMetas returns a copy of all AuthRouteMeta entries accumulated by
// DeclareAuthMeta. Useful in tests to assert that Cell route declarations
// propagate the correct attributes (e.g. PasswordResetExempt) without
// exercising the full HTTP request path.
func (r *Router) DeclaredAuthMetas() []kcell.AuthRouteMeta {
	if len(r.declaredAuthMetas) == 0 {
		return nil
	}
	out := make([]kcell.AuthRouteMeta, len(r.declaredAuthMetas))
	copy(out, r.declaredAuthMetas)
	return out
}

// orMergeRequest returns a predicate that returns true when either a or b matches.
// When a is nil it returns b; when b is nil it returns a.
func orMergeRequest(a, b func(*http.Request) bool) func(*http.Request) bool {
	if a == nil {
		return b
	}
	return func(req *http.Request) bool {
		return a(req) || b(req)
	}
}

// orMergeMethodPath returns a predicate that returns true when either a or b matches.
// When a is nil it returns b; when b is nil it returns a.
func orMergeMethodPath(a, b func(method, urlPath string) bool) func(string, string) bool {
	if a == nil {
		return b
	}
	return func(method, urlPath string) bool {
		return a(method, urlPath) || b(method, urlPath)
	}
}

// chiRouterAdapter wraps chi.Router to implement cell.RouteMux. prefix is the
// mount prefix the adapter inherited from its parent Route; declarer points to
// the Router-rooted AuthRouteDeclarer so nested auth.Declare calls propagate
// metadata with the fully-composed path.
type chiRouterAdapter struct {
	cr       chi.Router
	prefix   string
	declarer kcell.AuthRouteDeclarer
}

// Compile-time check: chiRouterAdapter forwards AuthRouteMeta so Cells that
// declare routes under mux.Route("/api/v1", ...) reach the top-level Router.
var _ kcell.AuthRouteDeclarer = (*chiRouterAdapter)(nil)

func (a *chiRouterAdapter) Handle(pattern string, handler http.Handler) {
	a.cr.Handle(pattern, handler)
}

func (a *chiRouterAdapter) Route(pattern string, fn func(kcell.RouteMux)) {
	a.cr.Route(pattern, func(cr chi.Router) {
		sub := &chiRouterAdapter{
			cr:       cr,
			prefix:   joinPrefix(a.prefix, pattern),
			declarer: a.declarer,
		}
		fn(sub)
	})
}

func (a *chiRouterAdapter) Mount(pattern string, handler http.Handler) {
	a.cr.Mount(pattern, handler)
}

func (a *chiRouterAdapter) Group(fn func(kcell.RouteMux)) {
	a.cr.Group(func(cr chi.Router) {
		sub := &chiRouterAdapter{cr: cr, prefix: a.prefix, declarer: a.declarer}
		fn(sub)
	})
}

func (a *chiRouterAdapter) With(mw ...func(http.Handler) http.Handler) kcell.RouteMux {
	return &chiRouterAdapter{cr: a.cr.With(mw...), prefix: a.prefix, declarer: a.declarer}
}

// DeclareAuthMeta composes the adapter's mount prefix with the declared path
// before handing the metadata off to the Router. A prefix of "" means the
// adapter sits directly under the Router (Group without a Route wrapper) and
// no path rewrite is needed.
func (a *chiRouterAdapter) DeclareAuthMeta(m kcell.AuthRouteMeta) {
	if a.declarer == nil {
		return
	}
	if a.prefix != "" {
		m.Path = joinPrefix(a.prefix, m.Path)
	}
	a.declarer.DeclareAuthMeta(m)
}

// joinPrefix composes a parent mount prefix with a child pattern/path,
// normalising the result via path.Clean so nested chains like
// `/api/v1` + `/access` + `/sessions/{id}` collapse to `/api/v1/access/sessions/{id}`.
// Both inputs are expected to begin with '/'; callers guard that invariant.
func joinPrefix(parent, child string) string {
	if parent == "" {
		return child
	}
	return path.Clean(parent + child)
}
