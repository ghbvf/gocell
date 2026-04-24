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

// WithInternalMiddleware appends one or more middleware factories to the
// internal mux chain. The internal mux handles /internal/v1/* routes and is
// exposed via Router.InternalHandler() for the bootstrap's internal HTTP
// listener. Because internal routes live on a physically separate mux, no
// JWT AuthMiddleware runs there — callers must install service-token / mTLS
// middleware here as the sole authentication layer.
//
// Each middleware must be non-nil; NewE returns an error immediately if any
// entry is nil.
//
// Chain order for internal requests:
//
//	[RateLimit] → [CircuitBreaker] → [InternalMiddleware...] → BodyLimit → handler
//
// ref: go-kratos/kratos middleware — per-transport middleware chains; this
// option is the dual of AuthMiddleware for the control-plane listener.
func WithInternalMiddleware(mws ...func(http.Handler) http.Handler) Option {
	return func(r *Router) {
		r.internalMiddlewares = append(r.internalMiddlewares, mws...)
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
// Internally it uses three chi.Mux instances for physical public / internal
// listener isolation (PR-A14a):
//
//   - outerMux:    shared observability + Recovery + SecurityHeaders + infra
//     endpoints (/healthz, /readyz, /metrics); mounts publicMux at "/" for
//     /api/v1/* and other public business routes. Exposed via PublicHandler().
//   - publicMux:   public business routes with the full protection chain
//     (RL/CB/JWT/BodyLimit). JWT AuthMiddleware lives only here.
//   - internalMux: /internal/v1/* business routes. NO infra endpoints, NO
//     JWT middleware — callers inject service-token/mTLS via
//     WithInternalMiddleware. Exposed via InternalHandler().
//
// The physical split replaces the pre-PR-A14a "single mux + /internal/v1/*
// prefix guard + JWT delegated-matcher" design; the guard and delegated
// matcher are no longer needed because internal routes never reach the
// public-mux middleware chain.
type Router struct {
	outerMux                   *chi.Mux // wraps publicMux + infra routes; served on primary listener
	publicMux                  *chi.Mux // /api/v1/* + non-internal business routes
	internalMux                *chi.Mux // /internal/v1/* routes; served on internal listener
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

	// internalMiddlewares are applied to the internal mux chain in declaration
	// order. Populated by WithInternalMiddleware. Each entry must be non-nil
	// (validated in NewE).
	internalMiddlewares []func(http.Handler) http.Handler

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

// internalPathPrefix marks URL paths mounted on internalMux. Route/Handle/Mount
// on the top-level Router auto-dispatch to internalMux when the pattern starts
// with this prefix; FinalizeAuth cross-checks that paths on this prefix carry
// Delegated=true and vice versa.
const internalPathPrefix = "/internal/v1/"

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
		outerMux:    chi.NewRouter(),
		publicMux:   chi.NewRouter(),
		internalMux: chi.NewRouter(),
		bodyLimit:   middleware.DefaultBodyLimit,
	}
	for _, o := range opts {
		o(r)
	}

	// Fail-fast: nil circuit breaker means the operator called
	// WithCircuitBreaker(nil) which would silently skip CB installation.
	if r.circuitBreakerNil {
		return nil, fmt.Errorf("router: circuit breaker must not be nil")
	}

	// Fail-fast: nil entries in WithInternalMiddleware would silently skip
	// the authoritative auth layer for /internal/v1/*.
	for i, mw := range r.internalMiddlewares {
		if mw == nil {
			return nil, fmt.Errorf("router: WithInternalMiddleware entry %d must not be nil", i)
		}
	}

	realIPMW, err := r.buildRealIPMiddleware()
	if err != nil {
		return nil, err
	}

	r.buildOuterMux(realIPMW)
	r.buildPublicMux()
	r.buildInternalMux()

	// Physical-isolation edge guard: outerMux explicitly 404s any
	// /internal/v1/* request before it reaches publicMux's JWT middleware.
	// Without this, auth middleware would 401 unauthenticated requests to
	// /internal/v1/* on the primary listener — indistinguishable from a
	// valid-but-missing-token response on a real internal route, leaking
	// the internal prefix. The explicit 404 enforces PR-A14a's contract
	// "the primary listener does not know about /internal/v1/*".
	r.outerMux.Handle(internalPathPrefix+"*", http.HandlerFunc(primaryInternalPrefix404))

	// Mount public mux on outerMux. Paths not matched by infra routes
	// (/healthz, /readyz, /metrics) or the internal-prefix 404 fall through
	// to public business routes. internalMux is NOT mounted on outerMux —
	// it is served on a separate HTTP listener via Router.InternalHandler().
	r.outerMux.Mount("/", r.publicMux)

	return r, nil
}

// primaryInternalPrefix404 writes a 404 JSON body for /internal/v1/* requests
// that reach the primary listener. The response shape matches other router
// not-found paths so clients and gateway logs see a uniform error code.
func primaryInternalPrefix404(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":{"code":"ERR_NOT_FOUND","message":"not found"}}`))
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

// buildPublicMux wires the public-listener middleware chain onto r.publicMux.
// Cells' /api/v1/* routes land here via Router.Route/Handle/Mount.
//
// Chain: [RateLimit] → [CircuitBreaker] → [Auth] → BodyLimit → handler.
// Recovery + SecurityHeaders are already applied by outerMux.
func (r *Router) buildPublicMux() {
	if r.rateLimiter != nil {
		r.publicMux.Use(middleware.RateLimit(r.rateLimiter))
	}
	if r.circuitBreaker != nil {
		r.publicMux.Use(middleware.CircuitBreaker(r.circuitBreaker))
	}
	if r.authVerifier != nil {
		r.publicMux.Use(auth.AuthMiddleware(r.authVerifier, r.buildAuthOpts()...))
	}
	r.publicMux.Use(middleware.BodyLimit(r.bodyLimit))
}

// buildInternalMux wires the internal-listener middleware chain onto
// r.internalMux. Cells' /internal/v1/* routes land here via Router.Route/
// Handle/Mount dispatch. The chain intentionally omits JWT AuthMiddleware;
// callers inject service-token or mTLS middleware via WithInternalMiddleware.
//
// Chain: [RateLimit] → [CircuitBreaker] → [InternalMiddleware...] → BodyLimit → handler.
// Recovery + SecurityHeaders are NOT inherited (outerMux only wraps publicMux).
// The internal listener is exposed on a separate port, typically bound to an
// internal network segment, so the baseline security headers differ from
// public. Operators can still add middleware here explicitly.
func (r *Router) buildInternalMux() {
	if r.rateLimiter != nil {
		r.internalMux.Use(middleware.RateLimit(r.rateLimiter))
	}
	if r.circuitBreaker != nil {
		r.internalMux.Use(middleware.CircuitBreaker(r.circuitBreaker))
	}
	for _, mw := range r.internalMiddlewares {
		r.internalMux.Use(mw)
	}
	r.internalMux.Use(middleware.BodyLimit(r.bodyLimit))
}

// buildAuthOpts constructs the AuthOption slice for the public-mux auth
// middleware. All matcher options use lazy closures that read Router fields
// at request time so that matchers finalized by FinalizeAuth (after
// AuthMiddleware is installed in buildPublicMux) are honoured without
// reinstalling middleware.
//
// Public endpoints and password-reset exemptions are populated by FinalizeAuth
// after each Cell's RegisterRoutes completes. The internal mux has no JWT
// middleware, so no delegated-matcher option is needed (PR-A14a).
func (r *Router) buildAuthOpts() []auth.AuthOption {
	opts := []auth.AuthOption{
		auth.WithPublicEndpointMatcher(func(req *http.Request) bool {
			if r.authPublicMatcher == nil {
				return false
			}
			return r.authPublicMatcher(req)
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

// pickMux routes a registration call to the public or internal mux based on
// the pattern's path prefix. Patterns beginning with "/internal/v1/" (or the
// Go 1.22 ServeMux form "METHOD /internal/v1/...") land on the internal mux;
// everything else lands on the public mux. This is the single source of
// truth for the PR-A14a physical-isolation contract.
func (r *Router) pickMux(pattern string) *chi.Mux {
	if pathPartStartsWithInternalPrefix(pattern) {
		return r.internalMux
	}
	return r.publicMux
}

// pathPartStartsWithInternalPrefix reports whether the path component of
// pattern (i.e. the leading '/...' after any optional "METHOD " prefix)
// begins with internalPathPrefix. Handles both forms:
//
//   - chi-style pattern:   "/internal/v1/foo"
//   - Go 1.22 ServeMux:    "GET /internal/v1/foo"
func pathPartStartsWithInternalPrefix(pattern string) bool {
	pathPart := pattern
	if idx := strings.Index(pattern, " "); idx >= 0 {
		pathPart = pattern[idx+1:]
	}
	return strings.HasPrefix(pathPart, internalPathPrefix)
}

// Handle registers a handler for the given pattern, implementing cell.RouteMux.
// The underlying mux is chosen by pickMux: /internal/v1/* patterns route to
// internalMux; everything else routes to publicMux.
func (r *Router) Handle(pattern string, handler http.Handler) {
	r.pickMux(pattern).Handle(pattern, handler)
}

// Group creates a sub-scope with a shared prefix, implementing cell.RouteMux.
// Because Group has no pattern, the sub-scope inherits the public mux; use
// Route("/internal/v1/...") to scope onto the internal mux.
func (r *Router) Group(fn func(kcell.RouteMux)) {
	r.publicMux.Group(func(cr chi.Router) {
		sub := &chiRouterAdapter{cr: cr, declarer: r}
		fn(sub)
	})
}

// Route mounts a sub-router under the given pattern. The adapter carries
// both the chi sub-router and a reference to the Router-rooted declarer so
// that auth.Declare called on a nested sub-mux composes the mount prefix
// with the declared path and forwards AuthRouteMeta to the top-level Router.
// The underlying mux is chosen by pickMux; nested Route calls stay on the
// mux chosen at the top level.
func (r *Router) Route(pattern string, fn func(kcell.RouteMux)) {
	r.pickMux(pattern).Route(pattern, func(cr chi.Router) {
		sub := &chiRouterAdapter{cr: cr, prefix: pattern, declarer: r}
		fn(sub)
	})
}

// Mount attaches an http.Handler under the given prefix. Prefix-based
// dispatch matches Route/Handle semantics.
func (r *Router) Mount(prefix string, handler http.Handler) {
	r.pickMux(prefix).Mount(prefix, handler)
}

// With returns a new RouteMux that applies the given middleware to routes
// registered through it, without modifying the receiver. Safe to call
// after routes are registered (unlike chi.Mux.Use which panics). The
// returned adapter inherits the public mux; use Route("/internal/v1/...").With
// if middleware should scope onto the internal mux.
func (r *Router) With(mw ...func(http.Handler) http.Handler) kcell.RouteMux {
	return &chiRouterAdapter{cr: r.publicMux.With(mw...), declarer: r}
}

// ServeHTTP delegates to PublicHandler so the Router stays drop-in for code
// paths that historically used the single-mux entry point (tests, etc.). For
// production deployment, Bootstrap calls PublicHandler() and InternalHandler()
// separately to drive two http.Server listeners.
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

// PublicHandler returns the http.Handler for the public listener: shared
// observability + Recovery + SecurityHeaders + infra endpoints (/healthz,
// /readyz, /metrics) + /api/v1/* + non-internal business routes. The
// returned handler 404s any /internal/v1/* request because those routes are
// not mounted here.
//
// Bootstrap binds this handler to the primary HTTP listener; k8s probes and
// Prometheus scrapes hit this listener.
func (r *Router) PublicHandler() http.Handler {
	return r.outerMux
}

// InternalHandler returns the http.Handler for the internal listener: a
// dedicated chi.Mux that ONLY carries /internal/v1/* routes with caller-
// supplied WithInternalMiddleware as the sole auth layer. Non-internal
// paths (including /healthz, /metrics, /api/v1/*) 404 because they are not
// registered here — this is the physical-isolation contract PR-A14a
// guarantees.
//
// Bootstrap binds this handler to the internal HTTP listener, typically on
// a separate port bound to an internal network segment (9090 by default).
func (r *Router) InternalHandler() http.Handler {
	return r.internalMux
}

// Handler is retained for backward compatibility with older callers that
// used a single handler entry point. It returns PublicHandler — PR-A14a
// makes the dual-handler split explicit via PublicHandler + InternalHandler.
//
// Deprecated: use PublicHandler for the primary listener or InternalHandler
// for the internal listener.
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
		// PR-A14a: enforce Delegated ↔ /internal/v1/* consistency. A route
		// declared with Delegated=true must live on /internal/v1/*, and any
		// route on /internal/v1/* must carry Delegated=true. This replaces
		// the pre-PR-A14a selective-guard behavior with a startup-time
		// assertion that catches mis-declared routes before traffic flows.
		if err := verifyInternalPrefixConsistency(r.declaredAuthMetas); err != nil {
			return err
		}

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
		r.deriveHint()

		// Warn when auth declarations exist but no verifier is installed: the
		// Public/PasswordResetExempt matchers need AuthMiddleware to run, but
		// Policy guards are inlined into handlers via RequirePolicy at Declare
		// time and remain effective regardless.
		if r.authVerifier == nil {
			slog.Warn("router: FinalizeAuth compiled route auth declarations but AuthMiddleware is not installed; Public/PasswordResetExempt matchers will have no effect (Policy guards run inline regardless)",
				slog.Int("declared", len(r.declaredAuthMetas)))
		}
	}

	// Policy coverage verification: every business route registered on the
	// public + internal muxes must have a corresponding auth.Declare call
	// (Public, Delegated, or with Policy). Infrastructure routes on outerMux
	// (healthz, readyz, metrics) are excluded because they are not registered
	// through Cell RegisterRoutes.
	routes := r.enumerateBusinessRoutes()
	if err := verifyPolicyCoverage(routes, r.declaredAuthMetas, r.policyCoverageWhitelist); err != nil {
		return fmt.Errorf("router: policy coverage: %w", err)
	}

	return nil
}

// verifyInternalPrefixConsistency enforces the PR-A14a invariant that
// Delegated=true and /internal/v1/* path co-occur on every auth meta:
//
//   - Delegated=true + non-/internal/v1/* path → fail
//   - Delegated=false + /internal/v1/* path   → fail
//
// Caught at FinalizeAuth time so Cells that mis-declare routes fail at
// startup instead of at first request (which would dispatch to the wrong
// mux and bypass the authoritative auth layer).
func verifyInternalPrefixConsistency(metas []kcell.AuthRouteMeta) error {
	for _, m := range metas {
		isInternal := strings.HasPrefix(m.Path, internalPathPrefix)
		switch {
		case m.Delegated && !isInternal:
			return fmt.Errorf(
				"router: Delegated=true route %s %s must live under %s",
				m.Method, m.Path, internalPathPrefix)
		case !m.Delegated && isInternal:
			return fmt.Errorf(
				"router: route %s %s on %s prefix must declare Delegated=true",
				m.Method, m.Path, internalPathPrefix)
		}
	}
	return nil
}

// enumerateBusinessRoutes walks both business muxes (publicMux + internalMux)
// and returns all registered (method, path) pairs. Infrastructure routes on
// outerMux (healthz, readyz, metrics) are excluded because they are not
// registered through Cell RegisterRoutes.
func (r *Router) enumerateBusinessRoutes() []routeKey {
	var routes []routeKey
	// chi.Walk never returns a non-nil error when the walker callback always
	// returns nil; the error return is part of the interface but unused here.
	collect := func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, routeKey{Method: method, Path: route})
		return nil
	}
	_ = chi.Walk(r.publicMux, collect)
	_ = chi.Walk(r.internalMux, collect)
	return routes
}

// authMetaPartition holds the categorised results of partitionAuthMetas.
//
// PR-A14a: the previous delegatedMatcher field was removed because internal
// routes are now physically mounted on internalMux and never reach the
// public-mux JWT middleware. The Delegated flag remains on AuthRouteMeta as
// a declarative annotation cross-checked against path prefix by
// verifyInternalPrefixConsistency.
type authMetaPartition struct {
	publicEntries []string
	exemptEntries []string
}

// partitionAuthMetas deduplicates the metas and splits them into the
// "METHOD /path" entry slices consumed by the compile helpers (public and
// password-reset-exempt). Duplicates (method, path) pairs are rejected.
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
	}
	return p, nil
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
