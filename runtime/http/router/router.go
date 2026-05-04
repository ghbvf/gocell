// Package router provides an HTTP router that implements kernel/cell.RouteMux
// with default observability middleware. Each Router instance wraps a single
// stdlib *http.ServeMux for ONE physical listener. Bootstrap builds one Router
// per declared listener (primary / internal / health) and applies the
// listener's default Policy before any routes are registered.
//
// ref: net/http.ServeMux (Go 1.22+) — method+pattern routing, automatic 405,
// automatic HEAD-from-GET, r.PathValue for path params.
// Adopted: stdlib ServeMux as the underlying multiplexer, one per listener.
// Wrapped behind kernel/cell.RouteMux interface so Cells remain decoupled from
// the multiplexer choice.
//
// ref: go-kratos/kratos transport/http/server.go — per-server middleware
// Adopted: policy applied at server build time; observability baked in.
package router

import (
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"reflect"
	"strings"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/http/middleware"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
)

// Middleware is the standard net/http middleware shape used throughout the
// router and its adapters.
type Middleware = func(http.Handler) http.Handler

// chain composes mws around h so the first mw runs outermost. An empty mws
// returns h unchanged.
func chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

// patternRecordingMux is the innermost dispatch wrapper. It calls
// (*http.ServeMux).Handler explicitly to recover the matched pattern, writes
// it into the request-scoped recorder installed by patternRecorderMiddleware,
// then dispatches. Stdlib's ServeMux only stores the pattern on the
// *http.Request after dispatch; using Handler(req) lifts the pattern up to
// every middleware in the chain via the shared *patternRecorder.
type patternRecordingMux struct {
	mux *http.ServeMux
}

func (p *patternRecordingMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Two-pass dispatch: (*ServeMux).Handler returns the matched pattern but
	// does NOT populate the request's {param} PathValues — those are written
	// by (*ServeMux).ServeHTTP when it constructs the dispatched request.
	// Read the pattern first for the observability recorder, then let
	// ServeHTTP perform the actual dispatch so r.PathValue keeps working
	// inside leaf handlers.
	if _, pattern := p.mux.Handler(r); pattern != "" {
		middleware.RecordRoutePattern(r.Context(), stripPatternMethod(pattern))
	}
	p.mux.ServeHTTP(w, r)
}

// stripPatternMethod drops the leading "METHOD " prefix from a ServeMux 1.22+
// pattern so the recorded route stays method-agnostic. Tracing, AccessLog,
// and Metrics compose the method with the route as they emit, so keeping the
// recorded value path-only avoids double-prefixing in span names like
// "GET GET /api/v1/users/{id}".
func stripPatternMethod(pattern string) string {
	if idx := strings.IndexByte(pattern, ' '); idx >= 0 {
		return pattern[idx+1:]
	}
	return pattern
}

// patternRecorderMiddleware installs an empty *patternRecorder into ctx so
// the dispatch wrapper has somewhere to write the matched pattern. Must be
// the outermost middleware so all observability layers can read the result
// after next.ServeHTTP returns.
func patternRecorderMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := middleware.WithRoutePatternRecorder(r.Context())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Compile-time checks.
var _ kcell.RouteMux = (*Router)(nil)
var _ kcell.AuthRouteDeclarer = (*Router)(nil)
var _ kcell.HTTPContractDeclarer = (*Router)(nil)

// Option configures a Router.
type Option func(*Router)

// WithMetricsCollector adds the metrics middleware using the given Collector.
// To also serve a /metrics endpoint, pass the handler to the RouteGroups mechanism.
func WithMetricsCollector(c metrics.Collector) Option {
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

// WithTracer enables distributed tracing middleware using the given Tracer.
// When provided, each request gets a trace span with trace_id and span_id
// propagated through context. Inbound W3C `traceparent` headers are extracted
// before span creation, with B3 used only as a fallback. The tracing
// middleware is placed after Recorder and before AccessLog so trace IDs appear
// in access logs.
//
// ref: go-zero — observability wired by default when configured
// ref: otelchi — chi middleware for OpenTelemetry trace propagation
func WithTracer(t tracing.Tracer) Option {
	return func(r *Router) {
		r.tracer = t
	}
}

// WithTracingOptions passes additional TracingOption values to the Tracing
// middleware.
func WithTracingOptions(opts ...middleware.TracingOption) Option {
	return func(r *Router) {
		r.tracingOpts = append(r.tracingOpts, opts...)
	}
}

// WithRequestIDOptions passes additional RequestIDOption values to the
// RequestID middleware.
func WithRequestIDOptions(opts ...middleware.RequestIDOption) Option {
	return func(r *Router) {
		r.requestIDOpts = append(r.requestIDOpts, opts...)
	}
}

// WithRateLimiter enables per-IP rate limiting in the default middleware chain.
// When provided, the rate limiter is placed after observability and before auth.
//
// ref: go-zero — rate limiting as default middleware when configured
func WithRateLimiter(rl middleware.RateLimiter) Option {
	return func(r *Router) {
		r.rateLimiter = rl
	}
}

// WithCircuitBreaker enables a circuit breaker in the default middleware chain.
// A nil cb is rejected by NewForListener so the circuit breaker is never silently absent.
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

// WithAuthMiddleware enables authentication middleware with an explicitly
// injected verifier. The middleware is placed in the mux chain after any
// rate-limiter/circuit-breaker and before BodyLimit. Public endpoints declared
// via auth.Mount with Public:true inside cell RouteGroups bypass JWT
// verification; FinalizeAuth compiles them into the router's auth predicates.
//
// In the per-listener model this option is most commonly used for the
// PrimaryListener router. InternalListener routers use cell.NewAuthServiceToken
// or cell.AuthMTLS{} at the listener level, not this option.
//
// ref: go-kratos/kratos — auth middleware at service level with selector-based bypass
// ref: go-zero — per-route WithJwt() opt-in auth
func WithAuthMiddleware(verifier auth.IntentTokenVerifier) Option {
	return func(r *Router) {
		if isNilIntentTokenVerifier(verifier) {
			r.authVerifierNil = true
			return
		}
		r.authVerifier = verifier
	}
}

func isNilIntentTokenVerifier(verifier auth.IntentTokenVerifier) bool {
	if verifier == nil {
		return true
	}
	v := reflect.ValueOf(verifier)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}

// WithAuthMetrics sets the AuthMetrics instance used by AuthMiddleware when wired
// via WithAuthMiddleware.
func WithAuthMetrics(m *auth.AuthMetrics) Option {
	return func(r *Router) { r.authMetrics = m }
}

// WithSecurityHeadersOptions passes additional SecurityHeadersOption values to
// the SecurityHeaders middleware.
//
// ref: unrolled/secure — configurable HSTS directives via struct fields
func WithSecurityHeadersOptions(opts ...middleware.SecurityHeadersOption) Option {
	return func(r *Router) {
		r.securityHeadersOpts = append(r.securityHeadersOpts, opts...)
	}
}

// WithTrustedProxies configures the set of trusted proxy IPs/CIDRs for
// X-Forwarded-For header processing.
//
// ref: gin-gonic/gin — SetTrustedProxies([]string) with CIDR support
func WithTrustedProxies(proxies []string) Option {
	return func(r *Router) {
		r.trustedProxies = proxies
	}
}

// WithPolicyCoverageWhitelist sets route patterns exempt from policy coverage
// verification.
//
// ref: kubernetes/apiserver — structural injection guarantees authorizer
// presence; GoCell's startup-time verification achieves the same guarantee.
func WithPolicyCoverageWhitelist(patterns []string) Option {
	return func(r *Router) {
		r.policyCoverageWhitelist = append(r.policyCoverageWhitelist, patterns...)
	}
}

// WithPublicPathPrefix pre-seeds the router's public-endpoint matcher so that
// any request whose URL path begins with prefix bypasses JWT authentication.
// Multiple calls OR-merge the predicates. The seed is applied before FinalizeAuth
// compiles the per-route declarations, so the merged matcher includes both the
// prefix exemption and any auth.Mount(Public:true) routes.
//
// Use this for framework-owned path prefixes that must be exempt from auth but
// are not declared via auth.Mount (e.g. the /internal/v1/* 404 isolation
// handler on the primary listener).
//
// F6 round-3: matches both the prefix form (".../path/") and the exact bare
// path (".../path") so the canonical isolation 404 fires for both
// /internal/v1 and /internal/v1/foo. Without the bare-path branch the
// pre-prefix path was challenged by JWT (401) instead of reaching the 404
// isolation handler — leaking the existence of the internal subtree.
func WithPublicPathPrefix(prefix string) Option {
	bare := strings.TrimSuffix(prefix, "/")
	return func(r *Router) {
		seed := func(req *http.Request) bool {
			p := req.URL.Path
			return strings.HasPrefix(p, prefix) || p == bare
		}
		r.authPublicMatcher = orMergeRequest(r.authPublicMatcher, seed)
	}
}

// WithEarlyResponder registers a predicate-driven middleware that runs
// before the policy layer (auth, rate limit, etc.). When the predicate
// matches, handler writes the response and the chain short-circuits.
//
// Intended for framework-owned isolation contracts that should NOT depend
// on a public-matcher exemption. The canonical example is the primary
// listener's /internal/v1/* 404 isolation: by deciding the response BEFORE
// auth runs, we avoid having to add the prefix to the JWT public-bypass
// matcher (which would also bypass any cell route mistakenly registered
// under that prefix). PR-258 RES-5 narrowing.
//
// Multiple WithEarlyResponder calls accumulate in declaration order; the
// first matching predicate wins.
func WithEarlyResponder(predicate func(*http.Request) bool, handler http.HandlerFunc) Option {
	return func(r *Router) {
		r.earlyResponders = append(r.earlyResponders, earlyResponder{
			predicate: predicate,
			handler:   handler,
		})
	}
}

// WithSuppressNoAuthVerifierWarn silences the FinalizeAuth slog.Warn that
// fires when auth route metas are declared on a router with no AuthMiddleware
// installed.
//
// Intended for routers that intentionally serve auth-declared routes without
// a JWT verifier — typically the HealthListener (whose framework probes use
// auth.Mount with Public:true) and the InternalListener (which gates traffic
// with cell.AuthMTLS{} or cell.NewAuthServiceToken instead of JWT). Without this opt-out the
// router emits a Warn at every production startup, drowning operators in
// alert noise. R2-11.
func WithSuppressNoAuthVerifierWarn() Option {
	return func(r *Router) {
		r.suppressNoVerifierWarn = true
	}
}

// WithRouterClock sets the clock used by the router's auth middleware for
// latency metric recording. Required when WithAuthMiddleware is also supplied;
// auth.AuthMiddleware panics on nil clock.
func WithRouterClock(clk clock.Clock) Option {
	return func(r *Router) { r.clock = clk }
}

// WithDefaultMiddleware appends middleware functions to the router's default
// middleware chain. These are installed AFTER the early-responder layer and
// BEFORE the per-router protections (rate-limiter, circuit-breaker, auth).
//
// Bootstrap uses this to install the listener-level auth middleware derived
// from the ListenerAuth chain (e.g. mTLS peer-cert check, ServiceToken HMAC
// guard). Multiple calls append in order.
func WithDefaultMiddleware(mws ...func(http.Handler) http.Handler) Option {
	return func(r *Router) {
		r.defaultMiddleware = append(r.defaultMiddleware, mws...)
	}
}

// Router wraps a single *http.ServeMux root for ONE physical listener.
// The observability middleware chain is baked in at construction time, and
// the listener's default Policy is applied as an inner layer before any
// cell routes are registered.
//
// Bootstrap creates one Router per declared listener (primary/internal/health)
// via NewForListener. The old shared-root multiplexer design has been replaced
// by this per-listener model.
//
// ref: go-kratos/kratos transport/http/server.go — per-server middleware
// ref: net/http.ServeMux — one ServeMux root per listener; method+pattern
//
//	routing (Go 1.22+).
type Router struct {
	ref kcell.ListenerRef // which listener this router serves
	mux *http.ServeMux    // single ServeMux root; observability + auth wrap it

	// middlewares is the listener-root chain wrapping mux. Order matches
	// declaration order: the first entry is outermost. buildMux populates it
	// via r.use(...) so archtest can statically assert ordering.
	middlewares []Middleware
	// handler is the lazily-built composition of middlewares around the
	// pattern-recording dispatch wrapper. Reset only when the chain changes
	// (currently only during NewForListener).
	handler http.Handler
	// muxHandlers tracks (method, path) pairs that have been registered on
	// the underlying ServeMux. Stdlib panics on a second registration of the
	// same pattern, so duplicate Handle calls are silently dropped here so
	// FinalizeAuth's structured duplicate-route error remains the canonical
	// signal — preserving the chi-era contract where duplicate auth.Mount
	// surfaces as a metadata error during phase5.
	muxHandlers map[string]bool

	// configuration fields (read during build)
	metricsCollector    metrics.Collector
	tracer              tracing.Tracer
	tracingOpts         []middleware.TracingOption
	requestIDOpts       []middleware.RequestIDOption
	rateLimiter         middleware.RateLimiter
	circuitBreaker      middleware.Allower
	circuitBreakerNil   bool
	authVerifier        auth.IntentTokenVerifier
	authVerifierNil     bool
	authMetrics         *auth.AuthMetrics
	securityHeadersOpts []middleware.SecurityHeadersOption
	bodyLimit           int64
	trustedProxies      []string
	// defaultMiddleware are installed AFTER early-responders and BEFORE
	// rate-limiter / circuit-breaker / auth. Bootstrap populates this by
	// converting the listener's AuthPlan chain (mTLS, ServiceToken, etc.)
	// via applyListenerAuthChain then passing them with WithDefaultMiddleware.
	defaultMiddleware []func(http.Handler) http.Handler

	// FinalizeAuth state
	authPublicMatcher func(*http.Request) bool
	authFinalized     bool
	declaredAuthMetas []kcell.AuthRouteMeta
	// declaredHTTPContracts accumulates HTTP ContractSpec entries forwarded
	// by auth.Mount. Tracing uses these as a fallback when upstream middleware
	// short-circuits before wrapper.HTTPHandler can contribute attrs.
	declaredHTTPContracts      []wrapper.ContractSpec
	ownedRoutes                []ownedRoutePath
	routePatterns              []registeredRoutePattern
	ownedPrefixes              []ownedRoutePrefix
	passwordResetExemptMatcher func(method, urlPath string) bool
	derivedHint                string

	policyCoverageWhitelist []string

	// suppressNoVerifierWarn silences the FinalizeAuth slog.Warn that fires when
	// auth route metas are declared on a router with no AuthMiddleware installed.
	// Set by SuppressNoAuthVerifierWarn for routers that intentionally run
	// without a JWT verifier (HealthListener, InternalListener with mTLS or
	// service-token gates) so their startup does not produce false alarms.
	// R2-11 ops noise fix.
	suppressNoVerifierWarn bool

	// clock is required when authVerifier is set; passed to auth.AuthMiddleware
	// via auth.WithAuthClock so the auth middleware can record latency metrics.
	clock clock.Clock

	// earlyResponders runs as middleware BEFORE the policy layer; the first
	// matching predicate writes a response and short-circuits the chain.
	// Owned by the framework (e.g. /internal/v1/* 404 isolation on primary).
	// PR-258 RES-5 narrowing.
	earlyResponders []earlyResponder
}

// earlyResponder captures a predicate + response handler pair.
type earlyResponder struct {
	predicate func(*http.Request) bool
	handler   http.HandlerFunc
}

type ownedRoutePrefix struct {
	prefix string
	cellID string
}

type ownedRoutePath struct {
	path   string
	cellID string
}

type registeredRoutePattern struct {
	method string
	path   string
}

type routeMatchRank struct {
	staticSegments int
	paramSegments  int
	depth          int
	length         int
}

// earlyResponderMiddleware turns an earlyResponder into a middleware that
// short-circuits when the predicate matches.
func earlyResponderMiddleware(er earlyResponder) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if er.predicate(r) {
				er.handler(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// internalPathPrefix marks URL paths that belong on the internal listener.
// This is an alias for kcell.InternalPathPrefix, kept as a local const
// to avoid changing call-site references throughout this file.
const internalPathPrefix = kcell.InternalPathPrefix

// New creates a Router with default middleware and optional configuration.
// It returns an error when configuration is invalid.
//
// The resulting Router serves the zero-value ListenerRef (no listener
// affinity). For production use call NewForListener instead.
//
// ref: gin-gonic/gin — SetTrustedProxies returns error at config time
// ref: uber-go/fx — startup failures return error, trigger rollback
func New(opts ...Option) (*Router, error) {
	return NewForListener(kcell.ListenerRef{}, opts...)
}

// MustNew is the composition-root fail-fast variant of New.
func MustNew(opts ...Option) *Router {
	r, err := New(opts...)
	if err != nil {
		panic(err.Error())
	}
	return r
}

// NewForListener builds a Router for a specific listener. Listener-level
// authentication middleware (mTLS, ServiceToken, etc.) is injected by
// Bootstrap via WithDefaultMiddleware(mws...) after applyListenerAuthChain
// converts the ListenerAuth chain; there is no longer a separate defaultPolicy
// parameter. JWT auth is wired via WithAuthMiddleware (a separate Option) so
// the router-aware Public/PasswordResetExempt matchers are available.
//
// The middleware chain is:
//
//	ListenerContext → RequestID → RealIP → Recorder → CellAttribution → [Tracing] → AccessLog → [Metrics]
//	→ Recovery → SecurityHeaders → [earlyResponders] → [defaultMiddleware]
//	→ [RateLimit] → [CircuitBreaker] → [Auth] → BodyLimit → handlers
//
// ref: go-kratos/kratos app.go WithServer + errgroup (adopted)
// ref: net/http.ServeMux (one ServeMux per listener)
// ref: kubernetes/kubernetes apiserver/server/genericapiserver.go (rejected single-listener)
func NewForListener(ref kcell.ListenerRef, opts ...Option) (*Router, error) {
	r := &Router{
		ref:       ref,
		mux:       http.NewServeMux(),
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
	if r.authVerifierNil {
		return nil, fmt.Errorf("router: auth middleware verifier must not be nil")
	}

	realIPMW, err := r.buildRealIPMiddleware()
	if err != nil {
		return nil, err
	}
	if r.clock == nil {
		return nil, fmt.Errorf("router: clock required — use WithRouterClock(clk)")
	}

	if err := r.buildMux(realIPMW); err != nil {
		return nil, err
	}
	return r, nil
}

// Handler returns the http.Handler for this listener's router. This is the
// handler to pass to http.Server. Unlike the legacy PublicHandler()/
// InternalHandler(), each listener now has exactly one Handler().
//
// The composition (outer → inner) is:
//
//	patternRecorderMiddleware → r.middlewares (in declaration order) →
//	  patternRecordingMux → *http.ServeMux → leaf handler
//
// patternRecorderMiddleware installs the *patternRecorder so observability
// layers can read the matched route pattern after next.ServeHTTP returns.
// patternRecordingMux fills the recorder by asking ServeMux.Handler for the
// matched pattern before dispatching the leaf handler.
func (r *Router) Handler() http.Handler {
	if r.handler == nil {
		dispatcher := &patternRecordingMux{mux: r.mux}
		all := make([]Middleware, 0, len(r.middlewares)+1)
		all = append(all, patternRecorderMiddleware)
		all = append(all, r.middlewares...)
		r.handler = chain(http.Handler(dispatcher), all...)
	}
	return r.handler
}

// ServeHTTP delegates to the router's composed handler, making Router drop-in
// compatible with http.Handler. If auth route metadata was declared but
// FinalizeAuth was not called, ServeHTTP fails closed with 500 instead of
// serving routes with incomplete auth state.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if len(r.declaredAuthMetas) > 0 && !r.authFinalized {
		slog.ErrorContext(req.Context(), "router: FinalizeAuth must be called before ServeHTTP",
			slog.Int("declared_auth_routes", len(r.declaredAuthMetas)))
		httputil.WriteError(req.Context(), w, http.StatusInternalServerError,
			string(errcode.ErrInternal), "internal server error")
		return
	}
	r.Handler().ServeHTTP(w, req)
}

// use appends middlewares to the router-root chain. Each call appends in
// declaration order; the first registered middleware ends up outermost (after
// patternRecorderMiddleware, which is always installed first by Handler).
func (r *Router) use(mws ...Middleware) {
	r.middlewares = append(r.middlewares, mws...)
}

// buildRealIPMiddleware constructs the RealIP middleware, validating trusted
// proxy configuration eagerly.
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

// buildMux wires the full middleware chain onto r.mux. Observability is baked
// in first; then defaultMiddleware (from WithDefaultMiddleware, e.g. mTLS /
// ServiceToken guards derived from the ListenerAuth chain) is applied; then
// the protection chain (RL/CB/Auth/BodyLimit) wraps the handlers.
//
// Chain order (outer to inner):
//
//	ListenerContext → RequestID → RealIP → Recorder → CellAttribution → [Tracing] → AccessLog → [Metrics]
//	→ Recovery → SecurityHeaders → [earlyResponders] → [defaultMiddleware]
//	→ [RateLimit] → [CircuitBreaker] → [Auth] → BodyLimit → handlers
func (r *Router) buildMux(realIPMW func(http.Handler) http.Handler) error {
	// Lazy public predicate so Tracing/RequestID honor public routes declared
	// later via auth.Mount / FinalizeAuth.
	lazyPublic := func(req *http.Request) bool {
		if r.authPublicMatcher == nil {
			return false
		}
		return r.authPublicMatcher(req)
	}
	r.tracingOpts = append(
		[]middleware.TracingOption{
			middleware.WithPublicEndpointFn(lazyPublic),
			middleware.WithContractAttrsResolver(r.resolveHTTPContractAttrs),
		},
		r.tracingOpts...,
	)
	r.requestIDOpts = append([]middleware.RequestIDOption{middleware.WithReqIDPublicEndpointFn(lazyPublic)}, r.requestIDOpts...)

	// --- Observability layer ---
	r.use(
		middleware.ListenerContext(r.ref.String()),
		middleware.RequestIDWithOptions(r.requestIDOpts...),
		realIPMW,
		middleware.Recorder,
		middleware.CellAttribution(r.resolveCellID),
	)
	if r.tracer != nil {
		r.use(middleware.Tracing(r.tracer, r.tracingOpts...))
	}
	r.use(middleware.AccessLog(r.clock))
	if r.metricsCollector != nil {
		r.use(middleware.Metrics(
			r.metricsCollector,
			r.clock,
			middleware.WithRoutePatternResolver(r.resolveHTTPRoutePattern),
		))
	}
	r.use(
		middleware.Recovery,
		middleware.SecurityHeadersWithOptions(r.securityHeadersOpts...),
	)

	// --- Early responders (PR-258 RES-5 narrowing) ---
	// Predicate-driven middleware that short-circuits BEFORE the policy
	// layer so framework-owned isolation contracts (e.g. /internal/v1/*
	// 404 on the primary listener) do not depend on a public-matcher
	// exemption that would bypass JWT for any future cell mistakenly
	// registering routes under that prefix. The predicate is the only
	// surface that can match — no per-route policy is consulted.
	for _, er := range r.earlyResponders {
		r.use(earlyResponderMiddleware(er))
	}

	// --- Default middleware layer (listener-level auth guards from AuthPlan chain) ---
	// Bootstrap populates r.defaultMiddleware via WithDefaultMiddleware after
	// converting the ListenerAuth chain (mTLS, ServiceToken, etc.) through
	// applyListenerAuthChain. Applied AFTER early-responders so framework
	// isolation contracts fire before the auth gate.
	if len(r.defaultMiddleware) > 0 {
		r.use(r.defaultMiddleware...)
	}

	// --- Protection chain (per-router options) ---
	if r.rateLimiter != nil {
		r.use(middleware.RateLimit(r.rateLimiter))
	}
	if r.circuitBreaker != nil {
		cb, err := middleware.CircuitBreaker(r.circuitBreaker)
		if err != nil {
			return fmt.Errorf("router: circuit breaker middleware: %w", err)
		}
		r.use(cb)
	}
	if r.authVerifier != nil {
		r.use(auth.AuthMiddleware(r.authVerifier, r.buildAuthOpts()...))
	}
	r.use(middleware.BodyLimit(r.bodyLimit))
	return nil
}

// buildAuthOpts constructs the AuthOption slice for the auth middleware.
func (r *Router) buildAuthOpts() []auth.AuthOption {
	opts := []auth.AuthOption{
		auth.WithAuthClock(r.clock),
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

// Handle registers a handler for the given pattern, implementing cell.RouteMux.
// pattern follows the stdlib ServeMux 1.22 form ("METHOD /path/{param}" or
// "/path"). The handler is registered directly on the underlying ServeMux
// without any extra middleware wrap; root-level middleware (observability,
// auth, body-limit, etc.) is composed by Router.Handler.
//
// Each registration is mirrored into routePatterns so policy coverage and
// route-pattern resolution see the same routes that ServeMux dispatches to.
// Duplicate registrations are tracked separately (muxHandlers) so the second
// call drops at the mux layer — FinalizeAuth's structured error is the
// canonical user-visible signal for duplicate auth.Mount.
func (r *Router) Handle(pattern string, handler http.Handler) {
	method, routePath := splitHandlePattern(pattern)
	if routePath == "" {
		r.mux.Handle(pattern, handler)
		return
	}
	if r.markMuxHandler(method, routePath) {
		r.recordRoutePattern(method, routePath)
		r.mux.Handle(pattern, handler)
	}
}

// markMuxHandler records that (method, path) was passed to the underlying
// ServeMux and returns true on the first registration. Subsequent calls for
// the same key return false so the caller can skip mux.Handle and avoid
// stdlib's duplicate-pattern panic.
func (r *Router) markMuxHandler(method, routePath string) bool {
	key := strings.ToUpper(method) + "\x00" + cleanRoutePath(routePath)
	if r.muxHandlers == nil {
		r.muxHandlers = make(map[string]bool)
	}
	if r.muxHandlers[key] {
		return false
	}
	r.muxHandlers[key] = true
	return true
}

// Group creates a sub-scope, implementing cell.RouteMux. The returned adapter
// shares the same underlying ServeMux as the parent. Group is a structural
// helper for clustering related route registrations; it does not introduce
// its own middleware scope (use With for that).
func (r *Router) Group(fn func(kcell.RouteMux)) {
	sub := r.newAdapter("", nil)
	fn(sub)
}

// Route mounts a sub-router under the given prefix. The adapter carries both
// the prefix and a reference to the Router-rooted declarer so that auth.Mount
// called on a nested sub-mux composes the mount prefix with the declared path
// and forwards AuthRouteMeta to the top-level Router.
func (r *Router) Route(pattern string, fn func(kcell.RouteMux)) {
	sub := r.newAdapter(pattern, nil)
	fn(sub)
}

// Mount attaches an http.Handler under the given prefix. Stdlib ServeMux
// requires sub-tree patterns to end in "/"; the trailing slash is added
// implicitly. The handler sees the request path with the prefix stripped.
//
// Both the bare prefix ("/api") and the sub-tree ("/api/...") are registered
// so requests that hit the mount root without a trailing slash still reach
// the inner handler's "/" registration instead of stdlib's 301 redirect.
func (r *Router) Mount(prefix string, handler http.Handler) {
	canonical := strings.TrimSuffix(prefix, "/")
	if canonical == "" {
		r.mux.Handle("/", handler)
		return
	}
	recorded := mountPatternRecorder(canonical, handler)
	r.mux.Handle(canonical+"/", http.StripPrefix(canonical, recorded))
	r.mux.Handle(canonical, mountBareHandler(recorded))
}

// mountBareHandler rewrites the request path to "/" before dispatching to the
// inner mount handler so a "GET /" registration inside the inner handler
// still matches when the outer request path equals the mount prefix exactly
// (e.g. "/api" with mount prefix "/api"). Without this rewrite stdlib
// ServeMux issues a 301 redirect to "/api/".
func mountBareHandler(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		req2 := req.Clone(req.Context())
		req2.URL.Path = "/"
		if req.URL.RawPath != "" {
			req2.URL.RawPath = "/"
		}
		handler.ServeHTTP(w, req2)
	})
}

// mountPatternRecorder upgrades a *http.ServeMux mount handler so it writes
// the matched-inner pattern composed with the mount prefix into the
// request-scoped *patternRecorder. This recovers chi's "Mount + sub-route =
// composed full pattern" semantics for observability — without this wrap
// the outer dispatch only records the mount subtree pattern (e.g. "/api/")
// and metrics/tracing/access-log lose route granularity for mounted cells.
//
// Non-ServeMux handlers are returned unchanged: their internal routing is
// opaque, so the outer mount-subtree pattern is the best label available.
func mountPatternRecorder(prefix string, handler http.Handler) http.Handler {
	subMux, ok := handler.(*http.ServeMux)
	if !ok {
		return handler
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, inner := subMux.Handler(r)
		if inner != "" {
			if idx := strings.IndexByte(inner, ' '); idx >= 0 {
				inner = inner[idx+1:]
			}
			middleware.RecordRoutePattern(r.Context(), prefix+inner)
		}
		subMux.ServeHTTP(w, r)
	})
}

// With returns a new RouteMux that applies the given middleware to routes
// registered through it, without modifying the receiver. Each subsequent
// Handle on the returned adapter wraps the handler with the captured chain
// before registering on the shared ServeMux.
func (r *Router) With(mw ...func(http.Handler) http.Handler) kcell.RouteMux {
	return r.newAdapter("", mw)
}

// newAdapter builds a fresh nativeMuxAdapter rooted at this Router. prefix
// composes with auth.Mount paths declared inside the adapter; mws is the
// per-adapter middleware chain that wraps each Handle/Mount registration.
func (r *Router) newAdapter(prefix string, mws []Middleware) *nativeMuxAdapter {
	var mwsCopy []Middleware
	if len(mws) > 0 {
		mwsCopy = append([]Middleware(nil), mws...)
	}
	return &nativeMuxAdapter{
		mux:         r.mux,
		prefix:      prefix,
		middlewares: mwsCopy,
		declarer:    r,
		owner:       r,
	}
}

// MountRouteGroup mounts a cell RouteGroup and records its HTTP namespace
// ownership for root-level observability. Cell ownership is resolved before
// protection middleware, so auth/rate-limit/circuit-breaker/body-limit rejects
// and ServeMux 405 responses all receive the same cell label as successful
// handler executions.
func (r *Router) MountRouteGroup(rg kcell.RouteGroup) error {
	if rg.Register == nil {
		return fmt.Errorf("router: RouteGroup for listener %q has nil Register function", rg.Listener.String())
	}
	if rg.CellID != "" && rg.Prefix != "" {
		if err := r.recordOwnedPrefix(rg.CellID, rg.Prefix); err != nil {
			return err
		}
	}

	var adapterErr error
	sub := &nativeMuxAdapter{
		mux:         r.mux,
		prefix:      rg.Prefix,
		middlewares: append([]Middleware(nil), rg.Middleware...),
		declarer:    r,
		owner:       r,
		cellID:      rg.CellID,
		err:         &adapterErr,
	}
	if err := rg.Register(sub); err != nil {
		return err
	}
	return adapterErr
}

// DeclareAuthMeta implements cell.AuthRouteDeclarer. It accumulates auth route
// metadata forwarded by auth.Mount during RouteGroup.Register. FinalizeAuth
// compiles the accumulated metas into matchers that the AuthMiddleware reads
// via lazy closures installed in buildAuthOpts.
//
// Returns an error if called after FinalizeAuth — all declarations must precede
// the compile step.
func (r *Router) DeclareAuthMeta(m kcell.AuthRouteMeta) error {
	if r.authFinalized {
		return fmt.Errorf(
			"router: DeclareAuthMeta called after FinalizeAuth — route %s %s must be declared before FinalizeAuth",
			m.Method, m.Path)
	}
	r.declaredAuthMetas = append(r.declaredAuthMetas, m)
	return nil
}

// DeclareHTTPContract implements cell.HTTPContractDeclarer. It stores the
// route contract metadata separately from auth metadata so Tracing can tag
// spans for requests rejected before reaching wrapper.HTTPHandler.
func (r *Router) DeclareHTTPContract(spec wrapper.ContractSpec) error {
	if r.authFinalized {
		return fmt.Errorf(
			"router: DeclareHTTPContract called after FinalizeAuth — route %s %s must be declared before FinalizeAuth",
			spec.Method, spec.Path)
	}
	r.declaredHTTPContracts = append(r.declaredHTTPContracts, spec)
	return nil
}

// FinalizeAuth compiles all accumulated AuthRouteMeta declarations into
// matchers used by the auth middleware. Bootstrap calls this after all cells
// have completed route registration but before the HTTP listener starts.
//
// Internal-route affinity is derived from path prefix via
// AuthRouteMeta.IsInternal(); /internal/v1/* routes on non-InternalListener
// routers fail fast at startup. Zero-ref routers (NewE / unit tests) skip
// the listener-identity check.
//
// Returns error (not panic) so Bootstrap can perform a clean rollback:
//   - "router: FinalizeAuth called twice"
//   - "router: duplicate auth declaration METHOD /path"
//
// It is safe to call FinalizeAuth with an empty declaredAuthMetas slice; the
// policy coverage check still runs to catch routes registered without any
// auth.Mount call.
func (r *Router) FinalizeAuth() error {
	if r.authFinalized {
		return fmt.Errorf("router: FinalizeAuth called twice")
	}
	r.authFinalized = true

	if len(r.declaredAuthMetas) > 0 {
		if err := r.verifyInternalRouteAffinity(); err != nil {
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

		if r.authVerifier == nil && !r.suppressNoVerifierWarn {
			r.warnNoAuthVerifier(partitioned)
		}
	}

	// Policy coverage verification.
	routes := r.enumerateRoutes()
	if err := verifyPolicyCoverage(routes, r.declaredAuthMetas, r.policyCoverageWhitelist); err != nil {
		return fmt.Errorf("router: policy coverage: %w", err)
	}

	return nil
}

// warnNoAuthVerifier emits observability warnings when auth declarations have been
// compiled but no AuthMiddleware is installed. Public:true warnings are emitted
// separately to guide operators toward the correct setup.
func (r *Router) warnNoAuthVerifier(p authMetaPartition) {
	slog.Warn("router: FinalizeAuth compiled route auth declarations but AuthMiddleware is not installed;"+
		" Public/PasswordResetExempt matchers will have no effect",
		slog.String("listener", r.ref.String()),
		slog.Int("declared", len(r.declaredAuthMetas)))
	if len(p.publicEntries) > 0 {
		slog.Warn("router: Public:true routes declared on a listener with no JWT middleware; "+
			"Public:true is a JWT exemption flag and has no effect without an auth verifier — "+
			"use cell.NewAuthJWTFromAssembly(asm) as authChain in bootstrap.WithListener to install JWT auth",
			slog.String("listener", r.ref.String()),
			slog.Int("public_routes", len(p.publicEntries)))
	}
}

// verifyInternalRouteAffinity verifies that routes with /internal/v1/* paths
// are mounted on an InternalListener router and vice versa.
//
// Internal-route affinity is derived structurally from the path prefix via
// AuthRouteMeta.IsInternal(). This function retains the listener-ref check so
// that a /internal/v1/* route mounted on the wrong listener still fails fast
// at startup.
func (r *Router) verifyInternalRouteAffinity() error {
	isInternal := r.ref == kcell.InternalListener
	// Zero-ref routers are used in unit tests without a listener identity.
	// Skip the listener-ref check for those; Bootstrap-built routers always
	// have a real ref so the production path remains guarded.
	isZeroRef := r.ref.IsZero()
	for _, m := range r.declaredAuthMetas {
		if m.IsInternal() && !isInternal && !isZeroRef {
			return fmt.Errorf(
				"router: route %s %s (internal path) must be mounted on InternalListener (got %q); "+
					"check the RouteGroup.Listener field",
				m.Method, m.Path, r.ref.String())
		}
		if !m.IsInternal() && isInternal {
			return fmt.Errorf(
				"router %q: route %s %s mounted on internal listener but path lacks %s prefix",
				r.ref, m.Method, m.Path, kcell.InternalPathPrefix)
		}
	}
	return nil
}

// enumerateRoutes returns all registered (method, path) pairs collected via
// recordRoutePattern during route registration. This replaces the prior
// chi.Walk traversal: stdlib ServeMux exposes no public route-iteration API,
// so the router is the sole source of truth for registered patterns.
func (r *Router) enumerateRoutes() []routeKey {
	if len(r.routePatterns) == 0 {
		return nil
	}
	out := make([]routeKey, 0, len(r.routePatterns))
	for _, p := range r.routePatterns {
		out = append(out, routeKey{Method: p.method, Path: p.path})
	}
	return out
}

// authMetaPartition holds the categorized results of partitionAuthMetas.
type authMetaPartition struct {
	publicEntries []string
	exemptEntries []string
}

// partitionAuthMetas deduplicates the metas and splits them into entry slices.
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

// mergeExemptMatcher OR-merges the compiled exempt entries.
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
// docs/operations/first-run-setup.md (changePasswordEndpoint field).
func (r *Router) deriveHint() {
	for _, m := range r.declaredAuthMetas {
		if m.Method == "POST" && m.PasswordResetExempt {
			r.derivedHint = m.Method + " " + m.Path
			return
		}
	}
}

// DeclaredAuthMetas returns a copy of all AuthRouteMeta entries accumulated.
func (r *Router) DeclaredAuthMetas() []kcell.AuthRouteMeta {
	if len(r.declaredAuthMetas) == 0 {
		return nil
	}
	out := make([]kcell.AuthRouteMeta, len(r.declaredAuthMetas))
	copy(out, r.declaredAuthMetas)
	return out
}

func (r *Router) recordOwnedPrefix(cellID, prefix string) error {
	if cellID == "" || strings.TrimSpace(prefix) == "" {
		return nil
	}
	prefix = cleanRoutePath(prefix)
	for _, owned := range r.ownedPrefixes {
		if owned.prefix != prefix {
			continue
		}
		if owned.cellID == cellID {
			return nil
		}
		return fmt.Errorf(
			"router: duplicate route ownership for path %q: cell %q already owns it, cell %q cannot also own it",
			prefix, owned.cellID, cellID)
	}
	r.ownedPrefixes = append(r.ownedPrefixes, ownedRoutePrefix{prefix: prefix, cellID: cellID})
	return nil
}

func (r *Router) recordOwnedRoutePath(cellID, routePath string) error {
	routePath = cleanRoutePath(routePath)
	if cellID == "" || routePath == "" {
		return nil
	}
	for _, owned := range r.ownedRoutes {
		if owned.path != routePath {
			continue
		}
		if owned.cellID == cellID {
			return nil
		}
		return fmt.Errorf(
			"router: duplicate route ownership for path %q: cell %q already owns it, cell %q cannot also own it",
			routePath, owned.cellID, cellID)
	}
	r.ownedRoutes = append(r.ownedRoutes, ownedRoutePath{path: routePath, cellID: cellID})
	return nil
}

func (r *Router) recordRoutePattern(method, routePath string) {
	routePath = cleanRoutePath(routePath)
	if routePath == "" {
		return
	}
	method = strings.ToUpper(method)
	if method != "" {
		r.routePatterns = append(r.routePatterns, registeredRoutePattern{
			method: method,
			path:   routePath,
		})
		return
	}
	// stdlib ServeMux treats method-less patterns as matching every HTTP
	// method. Mirror chi.Walk semantics: emit one entry per business method
	// so policy coverage and route-pattern resolution can reason about each
	// (method, path) pair independently.
	for m := range businessMethods {
		r.routePatterns = append(r.routePatterns, registeredRoutePattern{
			method: m,
			path:   routePath,
		})
	}
}

// not404Handler writes a 404 JSON body. Used when a primary-listener router
// needs an explicit 404 for /internal/v1/* paths.
// Exported for bootstrap's phase5 to install as a route group on the primary
// router to maintain the physical isolation contract.
func not404Handler(w http.ResponseWriter, r *http.Request) {
	if reqID, ok := ctxkeys.RequestIDFrom(r.Context()); ok && reqID != "" {
		w.Header().Set("X-Request-ID", reqID)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":{"code":"ERR_NOT_FOUND","message":"not found"}}`))
}

// InternalPrefixIsolationResponder returns a router.Option that 404s any
// request whose path starts with /internal/v1 (or equals the bare path)
// BEFORE any auth or policy middleware runs. Bootstrap installs this on
// the primary listener via WithEarlyResponder so the physical-isolation
// contract — primary listener never reveals that /internal/v1/* routes
// exist — does not depend on a JWT public-matcher exemption.
//
// PR-258 RES-5 narrowing: replaces the prior listener-mux Handle 404 +
// WithPublicPathPrefix("/internal/v1/") + frameworkPrimaryWhitelist triple-
// mechanism. The new model has a single surface: the predicate.
func InternalPrefixIsolationResponder() Option {
	bare := strings.TrimSuffix(internalPathPrefix, "/")
	predicate := func(r *http.Request) bool {
		p := r.URL.Path
		return strings.HasPrefix(p, internalPathPrefix) || p == bare
	}
	return WithEarlyResponder(predicate, not404Handler)
}

// resolveHTTPContractAttrs looks up span attributes for a request by matching
// against declared HTTP contract specs. Used by the Tracing middleware as a
// fallback when upstream middleware short-circuits before wrapper.HTTPHandler
// can contribute contract attributes.
func (r *Router) resolveHTTPContractAttrs(method, urlPath string) ([]wrapper.Attr, bool) {
	for _, spec := range r.declaredHTTPContracts {
		if !contractMethodMatches(spec.Method, method) {
			continue
		}
		if !contractPathMatches(spec.Path, urlPath) {
			continue
		}
		return httpContractAttrs(spec), true
	}
	return nil, false
}

func (r *Router) resolveCellID(_, urlPath string) (string, bool) {
	urlPath = cleanRoutePath(urlPath)
	bestCell := ""
	var bestScore routeMatchRank

	for _, route := range r.ownedRoutes {
		if !routePatternMatches(route.path, urlPath) {
			continue
		}
		if score := routeMatchScore(route.path); routeMatchScoreGreater(score, bestScore) {
			bestScore = score
			bestCell = route.cellID
		}
	}
	for _, prefix := range r.ownedPrefixes {
		if !segmentPrefixMatches(urlPath, prefix.prefix) {
			continue
		}
		if score := routeMatchScore(prefix.prefix); routeMatchScoreGreater(score, bestScore) {
			bestScore = score
			bestCell = prefix.cellID
		}
	}
	return bestCell, bestCell != ""
}

func (r *Router) resolveHTTPRoutePattern(method, urlPath string) (string, bool) {
	if route, ok := r.resolveRegisteredRoutePattern(method, urlPath, true); ok {
		return route, true
	}
	if route, ok := r.resolveContractRoutePattern(method, urlPath, true); ok {
		return route, true
	}
	if route, ok := r.resolveRegisteredRoutePattern(method, urlPath, false); ok {
		return route, true
	}
	return r.resolveContractRoutePattern(method, urlPath, false)
}

func (r *Router) resolveRegisteredRoutePattern(method, urlPath string, requireMethod bool) (string, bool) {
	urlPath = cleanRoutePath(urlPath)
	bestRoute := ""
	var bestScore routeMatchRank
	for _, route := range r.routePatterns {
		if requireMethod && !routeMethodMatches(route.method, method) {
			continue
		}
		if !routePatternMatches(route.path, urlPath) {
			continue
		}
		if score := routeMatchScore(route.path); routeMatchScoreGreater(score, bestScore) {
			bestScore = score
			bestRoute = route.path
		}
	}
	return bestRoute, bestRoute != ""
}

func (r *Router) resolveContractRoutePattern(method, urlPath string, requireMethod bool) (string, bool) {
	urlPath = cleanRoutePath(urlPath)
	bestRoute := ""
	var bestScore routeMatchRank
	for _, spec := range r.declaredHTTPContracts {
		if requireMethod && !contractMethodMatches(spec.Method, method) {
			continue
		}
		routePath := cleanRoutePath(spec.Path)
		if !routePatternMatches(routePath, urlPath) {
			continue
		}
		if score := routeMatchScore(routePath); routeMatchScoreGreater(score, bestScore) {
			bestScore = score
			bestRoute = routePath
		}
	}
	return bestRoute, bestRoute != ""
}

func routeMethodMatches(routeMethod, requestMethod string) bool {
	return routeMethod == "" || contractMethodMatches(routeMethod, requestMethod)
}

func contractMethodMatches(contractMethod, requestMethod string) bool {
	return contractMethod == requestMethod || (contractMethod == http.MethodGet && requestMethod == http.MethodHead)
}

func contractPathMatches(template, concrete string) bool {
	return routePatternMatches(template, concrete)
}

func routePatternMatches(template, concrete string) bool {
	template = cleanRoutePath(template)
	concrete = cleanRoutePath(concrete)
	if strings.HasSuffix(template, "/*") {
		return segmentPrefixMatches(concrete, strings.TrimSuffix(template, "/*"))
	}
	tParts := strings.Split(strings.Trim(template, "/"), "/")
	cParts := strings.Split(strings.Trim(concrete, "/"), "/")
	if len(tParts) != len(cParts) {
		return false
	}
	for i, t := range tParts {
		c := cParts[i]
		if strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
			if c == "" {
				return false
			}
			continue
		}
		if t != c {
			return false
		}
	}
	return true
}

func segmentPrefixMatches(concrete, prefix string) bool {
	concrete = cleanRoutePath(concrete)
	prefix = cleanRoutePath(prefix)
	if prefix == "/" {
		return true
	}
	return concrete == prefix || strings.HasPrefix(concrete, prefix+"/")
}

func routeMatchScore(routePath string) routeMatchRank {
	routePath = cleanRoutePath(routePath)
	rank := routeMatchRank{length: len(routePath)}
	for _, segment := range routeSegments(routePath) {
		if segment == "*" || segment == "" {
			continue
		}
		rank.depth++
		if strings.HasPrefix(segment, "{") && strings.HasSuffix(segment, "}") {
			rank.paramSegments++
			continue
		}
		rank.staticSegments++
	}
	return rank
}

func routeMatchScoreGreater(candidate, current routeMatchRank) bool {
	if candidate.staticSegments != current.staticSegments {
		return candidate.staticSegments > current.staticSegments
	}
	if candidate.paramSegments != current.paramSegments {
		return candidate.paramSegments > current.paramSegments
	}
	if candidate.depth != current.depth {
		return candidate.depth > current.depth
	}
	return candidate.length > current.length
}

func routeSegments(routePath string) []string {
	trimmed := strings.Trim(cleanRoutePath(routePath), "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func splitHandlePattern(pattern string) (method, routePath string) {
	fields := strings.Fields(pattern)
	if len(fields) >= 2 {
		return strings.ToUpper(fields[0]), fields[1]
	}
	return "", pattern
}

func cleanRoutePath(routePath string) string {
	if routePath == "" {
		return "/"
	}
	if !strings.HasPrefix(routePath, "/") {
		routePath = "/" + routePath
	}
	cleaned := path.Clean(routePath)
	if cleaned == "." {
		return "/"
	}
	return cleaned
}

func httpContractAttrs(spec wrapper.ContractSpec) []wrapper.Attr {
	return []wrapper.Attr{
		{Key: "gocell.contract.id", Value: spec.ID},
		{Key: "gocell.contract.kind", Value: spec.Kind},
		{Key: "gocell.contract.transport", Value: spec.Transport},
		{Key: "http.method", Value: spec.Method},
		{Key: "http.route", Value: spec.Path},
	}
}

// orMergeRequest returns a predicate that returns true when either a or b matches.
func orMergeRequest(a, b func(*http.Request) bool) func(*http.Request) bool {
	if a == nil {
		return b
	}
	return func(req *http.Request) bool {
		return a(req) || b(req)
	}
}

// orMergeMethodPath returns a predicate that returns true when either a or b matches.
func orMergeMethodPath(a, b func(method, urlPath string) bool) func(string, string) bool {
	if a == nil {
		return b
	}
	return func(method, urlPath string) bool {
		return a(method, urlPath) || b(method, urlPath)
	}
}

// nativeMuxAdapter implements cell.RouteMux on top of stdlib *http.ServeMux.
//
// All adapters under the same Router share a single ServeMux pointer; prefix
// composition and middleware chains are tracked per adapter and applied at
// Handle time. declarer points to the Router-rooted AuthRouteDeclarer so
// nested auth.Mount calls propagate metadata with the fully-composed path.
type nativeMuxAdapter struct {
	mux         *http.ServeMux
	prefix      string
	middlewares []Middleware
	declarer    kcell.AuthRouteDeclarer
	owner       *Router
	cellID      string
	err         *error
}

// Compile-time checks: nativeMuxAdapter forwards AuthRouteMeta + declares its
// mount prefix so auth.Mount can derive ServeMux-relative registration paths
// from fully-qualified Contract.Path literals.
var _ kcell.AuthRouteDeclarer = (*nativeMuxAdapter)(nil)
var _ kcell.Prefixer = (*nativeMuxAdapter)(nil)
var _ kcell.HTTPContractDeclarer = (*nativeMuxAdapter)(nil)

// Prefix returns the sub-route mount prefix this adapter inherited from its
// parent Route. An empty prefix means the adapter sits directly under the
// Router (Group / top-level) and contributes no path composition.
func (a *nativeMuxAdapter) Prefix() string { return a.prefix }

func (a *nativeMuxAdapter) Handle(pattern string, handler http.Handler) {
	if a.hasRegistrationErr() {
		return
	}
	method, routePath := splitHandlePattern(pattern)
	if routePath != "" {
		fullPath := joinPrefix(a.prefix, routePath)
		if a.owner != nil && !a.owner.markMuxHandler(method, fullPath) {
			// Duplicate registration — defer to FinalizeAuth for the
			// user-visible error. See Router.Handle for context.
			return
		}
		if err := a.recordOwnedRoute(method, routePath); err != nil {
			a.setRegistrationErr(err)
			return
		}
	}
	if len(a.middlewares) > 0 {
		handler = chain(handler, a.middlewares...)
	}
	a.mux.Handle(combineHandlePattern(a.prefix, pattern), handler)
}

func (a *nativeMuxAdapter) Route(pattern string, fn func(kcell.RouteMux)) {
	if a.hasRegistrationErr() {
		return
	}
	fullPrefix := joinPrefix(a.prefix, pattern)
	if err := a.recordOwnedPrefix(fullPrefix); err != nil {
		a.setRegistrationErr(err)
		return
	}
	sub := &nativeMuxAdapter{
		mux:         a.mux,
		prefix:      fullPrefix,
		middlewares: append([]Middleware(nil), a.middlewares...),
		declarer:    a.declarer,
		owner:       a.owner,
		cellID:      a.cellID,
		err:         a.err,
	}
	fn(sub)
}

func (a *nativeMuxAdapter) Mount(pattern string, handler http.Handler) {
	if a.hasRegistrationErr() {
		return
	}
	fullPrefix := joinPrefix(a.prefix, pattern)
	if err := a.recordOwnedPrefix(fullPrefix); err != nil {
		a.setRegistrationErr(err)
		return
	}
	canonical := strings.TrimSuffix(fullPrefix, "/")
	if len(a.middlewares) > 0 {
		handler = chain(handler, a.middlewares...)
	}
	if canonical == "" {
		a.mux.Handle("/", handler)
		return
	}
	recorded := mountPatternRecorder(canonical, handler)
	a.mux.Handle(canonical+"/", http.StripPrefix(canonical, recorded))
	a.mux.Handle(canonical, mountBareHandler(recorded))
}

func (a *nativeMuxAdapter) Group(fn func(kcell.RouteMux)) {
	if a.hasRegistrationErr() {
		return
	}
	sub := &nativeMuxAdapter{
		mux:         a.mux,
		prefix:      a.prefix,
		middlewares: append([]Middleware(nil), a.middlewares...),
		declarer:    a.declarer,
		owner:       a.owner,
		cellID:      a.cellID,
		err:         a.err,
	}
	fn(sub)
}

func (a *nativeMuxAdapter) With(mw ...func(http.Handler) http.Handler) kcell.RouteMux {
	merged := make([]Middleware, 0, len(a.middlewares)+len(mw))
	merged = append(merged, a.middlewares...)
	merged = append(merged, mw...)
	return &nativeMuxAdapter{
		mux:         a.mux,
		prefix:      a.prefix,
		middlewares: merged,
		declarer:    a.declarer,
		owner:       a.owner,
		cellID:      a.cellID,
		err:         a.err,
	}
}

// DeclareAuthMeta composes the adapter's mount prefix with the declared path
// before handing the metadata off to the Router.
func (a *nativeMuxAdapter) DeclareAuthMeta(m kcell.AuthRouteMeta) error {
	if a.declarer == nil {
		return nil
	}
	if a.prefix != "" {
		m.Path = joinPrefix(a.prefix, m.Path)
	}
	return a.declarer.DeclareAuthMeta(m)
}

// DeclareHTTPContract forwards the route's full ContractSpec to the
// Router-rooted declarer. ContractSpec.Path is already the canonical full
// path, so unlike AuthRouteMeta it is not composed with the adapter prefix.
func (a *nativeMuxAdapter) DeclareHTTPContract(spec wrapper.ContractSpec) error {
	if a.owner != nil {
		a.owner.recordRoutePattern(spec.Method, spec.Path)
		if a.cellID != "" {
			if err := a.owner.recordOwnedRoutePath(a.cellID, spec.Path); err != nil {
				a.setRegistrationErr(err)
				return err
			}
		}
	}
	if a.declarer == nil {
		return nil
	}
	if declarer, ok := a.declarer.(kcell.HTTPContractDeclarer); ok {
		return declarer.DeclareHTTPContract(spec)
	}
	return nil
}

func (a *nativeMuxAdapter) recordOwnedPrefix(prefix string) error {
	if a.owner == nil || a.cellID == "" {
		return nil
	}
	return a.owner.recordOwnedPrefix(a.cellID, prefix)
}

func (a *nativeMuxAdapter) recordOwnedRoute(method, routePath string) error {
	if a.owner == nil {
		return nil
	}
	fullPath := joinPrefix(a.prefix, routePath)
	a.owner.recordRoutePattern(method, fullPath)
	if a.cellID == "" {
		return nil
	}
	if err := a.owner.recordOwnedRoutePath(a.cellID, fullPath); err != nil {
		return err
	}
	return nil
}

func (a *nativeMuxAdapter) setRegistrationErr(err error) {
	if err == nil || a.err == nil || *a.err != nil {
		return
	}
	*a.err = err
}

func (a *nativeMuxAdapter) hasRegistrationErr() bool {
	return a.err != nil && *a.err != nil
}

// combineHandlePattern composes the adapter's prefix into a stdlib ServeMux
// pattern. Input pattern follows the "[METHOD ]/path" form; the prefix is
// applied to the path component only, and the optional method is preserved
// verbatim. Empty prefix returns the original pattern.
//
// When the child path is the bare slash ("/"), the result is registered as
// the subtree of the parent prefix (trailing slash) so requests to the
// prefix and any deeper path still hit the registered handler — chi's
// "Route(prefix) + Handle('/', h)" semantics.
func combineHandlePattern(prefix, pattern string) string {
	if prefix == "" {
		return pattern
	}
	method, routePath := splitHandlePattern(pattern)
	full := joinPrefix(prefix, routePath)
	if routePath == "/" {
		full = strings.TrimSuffix(full, "/") + "/"
	}
	if method == "" {
		return full
	}
	return method + " " + full
}

// joinPrefix composes a parent mount prefix with a child pattern/path,
// normalising the result via path.Clean so nested chains like
// `/api/v1` + `/access` + `/sessions/{id}` collapse to `/api/v1/access/sessions/{id}`.
func joinPrefix(parent, child string) string {
	if parent == "" {
		return child
	}
	return path.Clean(parent + child)
}
