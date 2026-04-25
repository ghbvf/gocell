// Package router provides a chi-based HTTP router that implements
// kernel/cell.RouteMux with default observability middleware. Each Router
// instance wraps a single chi.Mux root for ONE physical listener. Bootstrap
// builds one Router per declared listener (primary / internal / health) and
// applies the listener's default Policy before any routes are registered.
//
// ref: go-chi/chi/v5 — Mux pattern (Group, Mount, Route, Use)
// Adopted: chi.NewRouter as the underlying multiplexer, one per listener.
// Deviated: wrapped behind kernel/cell.RouteMux interface so Cells remain
// decoupled from any specific router library.
//
// ref: go-kratos/kratos transport/http/server.go — per-server middleware
// Adopted: policy applied at server build time; observability baked in.
package router

import (
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"strings"

	"github.com/go-chi/chi/v5"

	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/http/middleware"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
)

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
// When provided, the rate limiter is placed after AccessLog and before Metrics.
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
// In the new per-listener model this option is most commonly used for the
// PrimaryListener router. InternalListener routers use PolicyServiceToken or
// PolicyMTLS at the listener level, not this option.
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
// with mTLS or PolicyServiceToken instead of JWT). Without this opt-out the
// router emits a Warn at every production startup, drowning operators in
// alert noise. R2-11.
func WithSuppressNoAuthVerifierWarn() Option {
	return func(r *Router) {
		r.suppressNoVerifierWarn = true
	}
}

// Router wraps a single chi.Mux root for ONE physical listener.
// The observability middleware chain is baked in at construction time, and
// the listener's default Policy is applied as an inner layer before any
// cell routes are registered.
//
// Bootstrap creates one Router per declared listener (primary/internal/health)
// via NewForListener. The old multi-mux design (outerMux/publicMux/internalMux)
// has been replaced by this per-listener model.
//
// ref: go-kratos/kratos transport/http/server.go — per-server middleware
// ref: go-chi/chi mux.go — one chi.Mux root per listener
type Router struct {
	ref kcell.ListenerRef // which listener this router serves
	mux *chi.Mux          // single chi.Mux root; observability + policy already applied

	// configuration fields (read during build)
	metricsCollector    metrics.Collector
	tracer              tracing.Tracer
	tracingOpts         []middleware.TracingOption
	requestIDOpts       []middleware.RequestIDOption
	rateLimiter         middleware.RateLimiter
	circuitBreaker      middleware.Allower
	circuitBreakerNil   bool
	authVerifier        auth.IntentTokenVerifier
	authMetrics         *auth.AuthMetrics
	securityHeadersOpts []middleware.SecurityHeadersOption
	bodyLimit           int64
	trustedProxies      []string

	// FinalizeAuth state
	authPublicMatcher func(*http.Request) bool
	authFinalized     bool
	declaredAuthMetas []kcell.AuthRouteMeta
	// declaredHTTPContracts accumulates HTTP ContractSpec entries forwarded
	// by auth.Mount. Tracing uses these as a fallback when upstream middleware
	// short-circuits before wrapper.HTTPHandler can contribute attrs.
	declaredHTTPContracts      []wrapper.ContractSpec
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

// earlyResponderMiddleware turns an earlyResponder into a chi middleware that
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
// Used only for FinalizeAuth consistency checks: Delegated=true ↔ path
// starts with this prefix (or the router serves InternalListener).
const internalPathPrefix = "/internal/v1/"

// New creates a Router with default middleware and optional configuration.
// Convenience wrapper around NewForListener using cell.PrimaryListener and
// no default policy (PolicyNone semantics: observability only, no auth).
//
// It panics if the configuration is invalid.
// Use NewE for an error-returning variant suitable for managed startup.
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
// startup sequences.
//
// The resulting Router serves the zero-value ListenerRef (no listener
// affinity). For production use call NewForListener instead.
//
// ref: gin-gonic/gin — SetTrustedProxies returns error at config time
// ref: uber-go/fx — startup failures return error, trigger rollback
func NewE(opts ...Option) (*Router, error) {
	return NewForListener(kcell.ListenerRef{}, kcell.Policy{}, opts...)
}

// NewForListener builds a Router for a specific listener. defaultPolicy is
// applied to the mux root (via its middleware) after the observability chain
// is installed but before any routes are registered. A zero-value Policy is
// treated as PolicyNone (no policy middleware; observability only).
//
// The observability chain is:
//
//	RequestID → RealIP → Recorder → [Tracing] → AccessLog → [Metrics]
//	→ Recovery → SecurityHeaders → [policy middleware] → [RL/CB/Auth/BodyLimit] → handlers
//
// ref: go-kratos/kratos app.go WithServer + errgroup (adopted)
// ref: go-chi/chi mux.go (one chi.Mux per listener)
// ref: kubernetes/kubernetes apiserver/server/genericapiserver.go (rejected single-listener)
func NewForListener(ref kcell.ListenerRef, defaultPolicy kcell.Policy, opts ...Option) (*Router, error) {
	r := &Router{
		ref:       ref,
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

	realIPMW, err := r.buildRealIPMiddleware()
	if err != nil {
		return nil, err
	}

	r.buildMux(realIPMW, defaultPolicy)
	return r, nil
}

// Handler returns the http.Handler for this listener's router. This is the
// handler to pass to http.Server. Unlike the legacy PublicHandler()/
// InternalHandler(), each listener now has exactly one Handler().
func (r *Router) Handler() http.Handler { return r.mux }

// ServeHTTP delegates to the router's mux, making Router drop-in compatible
// with http.Handler. Panics if FinalizeAuth has not been called when auth route
// metadata has been declared.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if len(r.declaredAuthMetas) > 0 && !r.authFinalized {
		panic("router: FinalizeAuth must be called before ServeHTTP when auth route metadata has been declared")
	}
	r.mux.ServeHTTP(w, req)
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
// in first; then the policy (if any) is applied; then the protection chain
// (RL/CB/Auth/BodyLimit) wraps the handlers.
//
// Chain order (outer to inner):
//
//	RequestID → RealIP → Recorder → [Tracing] → AccessLog → [Metrics]
//	→ Recovery → SecurityHeaders → [policy] → [RateLimit] → [CircuitBreaker]
//	→ [Auth] → BodyLimit → handlers
func (r *Router) buildMux(realIPMW func(http.Handler) http.Handler, defaultPolicy kcell.Policy) {
	// Lazy public predicate so Tracing/RequestID honour public routes declared
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
	r.mux.Use(
		middleware.RequestIDWithOptions(r.requestIDOpts...),
		realIPMW,
		middleware.Recorder,
	)
	if r.tracer != nil {
		r.mux.Use(middleware.Tracing(r.tracer, r.tracingOpts...))
	}
	r.mux.Use(middleware.AccessLog)
	if r.metricsCollector != nil {
		r.mux.Use(middleware.Metrics(r.metricsCollector))
	}
	r.mux.Use(
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
		r.mux.Use(earlyResponderMiddleware(er))
	}

	// --- Policy layer (listener-default policy) ---
	// Apply the policy middleware AFTER observability so all requests are
	// observable regardless of whether they pass the policy gate.
	if !defaultPolicy.IsZero() {
		applyPolicyToMux(defaultPolicy, r.mux)
	}

	// --- Protection chain (per-router options) ---
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
}

// applyPolicyToMux installs the policy's middleware on mux. If p.Middleware is
// nil (PolicyNone / zero Policy) the function is a no-op.
//
// B1 simplification: cell.Policy is now a value struct, not an interface, so
// no type-assertion is needed — middleware is accessed directly.
func applyPolicyToMux(p kcell.Policy, mux *chi.Mux) {
	if p.Middleware != nil {
		mux.Use(p.Middleware)
	}
}

// buildAuthOpts constructs the AuthOption slice for the auth middleware.
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

// Handle registers a handler for the given pattern, implementing cell.RouteMux.
func (r *Router) Handle(pattern string, handler http.Handler) {
	r.mux.Handle(pattern, handler)
}

// Group creates a sub-scope, implementing cell.RouteMux.
func (r *Router) Group(fn func(kcell.RouteMux)) {
	r.mux.Group(func(cr chi.Router) {
		sub := &chiRouterAdapter{cr: cr, declarer: r}
		fn(sub)
	})
}

// Route mounts a sub-router under the given pattern. The adapter carries
// both the chi sub-router and a reference to the Router-rooted declarer so
// that auth.Mount called on a nested sub-mux composes the mount prefix
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
// registered through it, without modifying the receiver.
func (r *Router) With(mw ...func(http.Handler) http.Handler) kcell.RouteMux {
	return &chiRouterAdapter{cr: r.mux.With(mw...), declarer: r}
}

// DeclareAuthMeta implements cell.AuthRouteDeclarer. It accumulates auth route
// metadata forwarded by auth.Mount during Cell RegisterRoutes. FinalizeAuth
// compiles the accumulated metas into matchers that the AuthMiddleware reads
// via lazy closures installed in buildAuthOpts.
//
// Panics if called after FinalizeAuth — all declarations must precede the
// compile step.
func (r *Router) DeclareAuthMeta(m kcell.AuthRouteMeta) {
	if r.authFinalized {
		panic(fmt.Sprintf(
			"router: DeclareAuthMeta called after FinalizeAuth — route %s %s must be declared before FinalizeAuth",
			m.Method, m.Path))
	}
	r.declaredAuthMetas = append(r.declaredAuthMetas, m)
}

// DeclareHTTPContract implements cell.HTTPContractDeclarer. It stores the
// route contract metadata separately from auth metadata so Tracing can tag
// spans for requests rejected before reaching wrapper.HTTPHandler.
func (r *Router) DeclareHTTPContract(spec wrapper.ContractSpec) {
	if r.authFinalized {
		panic(fmt.Sprintf(
			"router: DeclareHTTPContract called after FinalizeAuth — route %s %s must be declared before FinalizeAuth",
			spec.Method, spec.Path))
	}
	r.declaredHTTPContracts = append(r.declaredHTTPContracts, spec)
}

// FinalizeAuth compiles all accumulated AuthRouteMeta declarations into
// matchers used by the auth middleware. Bootstrap calls this after all cells
// have completed route registration but before the HTTP listener starts.
//
// PR-A14b: the Delegated ↔ /internal/v1/* invariant from PR-A14a is relaxed
// to be per-router context: Delegated=true routes must be on an
// InternalListener router. When the router has a zero-value ref (created via
// NewE), the old path-prefix check is preserved for backward compatibility.
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
		if err := r.verifyDelegatedConsistency(); err != nil {
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
	slog.Warn("router: FinalizeAuth compiled route auth declarations but AuthMiddleware is not installed; Public/PasswordResetExempt matchers will have no effect",
		slog.Int("declared", len(r.declaredAuthMetas)))
	if len(p.publicEntries) > 0 {
		slog.Warn("router: Public:true routes declared on a listener with no JWT middleware; "+
			"Public:true is a JWT exemption flag and has no effect without an auth verifier — "+
			"use PolicyJWTFromAssembly(asm) or WithAuthMiddleware(verifier) to install JWT auth",
			slog.Int("public_routes", len(p.publicEntries)))
	}
}

// verifyDelegatedConsistency enforces the Delegated ↔ InternalListener
// invariant. PR-A14b's intent is that Delegated=true marks an internal-only
// route, both at the URL prefix layer (/internal/v1/*) and the physical
// listener layer (mounted on InternalListener). All three projections must
// agree, otherwise an internal route can land on the public port without the
// service-token / mTLS gate.
//
// Rules (router-aware, F2 round-3 fix):
//   - Delegated=true must use /internal/v1/* prefix.
//   - Delegated=true must be mounted on InternalListener (or a zero-ref
//     router used in unit tests where the listener identity is unspecified).
//   - Delegated=false on /internal/v1/* prefix is rejected on every router.
//
// The router-aware InternalListener check closes the F2 gap where a typo'd
// RouteGroup.Listener ("PrimaryListener" instead of "InternalListener")
// silently passed FinalizeAuth and exposed an internal route on the public
// port.
func (r *Router) verifyDelegatedConsistency() error {
	isInternal := r.ref == kcell.InternalListener
	// Zero-ref routers are created by NewE() / New() in unit tests without a
	// listener identity. The Delegated→InternalListener invariant is a
	// production-only check; tests that drive FinalizeAuth directly without
	// going through Bootstrap (which always assigns a real ListenerRef via
	// NewForListener) would otherwise need to rewire every fixture. Skip the
	// listener check for zero-ref routers — Bootstrap-built routers always
	// have a non-zero ref so the prod path stays guarded.
	isZeroRef := r.ref.IsZero()
	for _, m := range r.declaredAuthMetas {
		pathIsInternal := strings.HasPrefix(m.Path, internalPathPrefix)
		switch {
		case m.Delegated && !pathIsInternal:
			return fmt.Errorf(
				"router: Delegated=true route %s %s must live under %s",
				m.Method, m.Path, internalPathPrefix)
		case m.Delegated && !isInternal && !isZeroRef:
			return fmt.Errorf(
				"router: Delegated=true route %s %s must be mounted on InternalListener (got %q); "+
					"check the RouteGroup.Listener field",
				m.Method, m.Path, r.ref.String())
		case !m.Delegated && pathIsInternal:
			return fmt.Errorf(
				"router: route %s %s under %s must declare Delegated=true",
				m.Method, m.Path, internalPathPrefix)
		}
	}
	return nil
}

// enumerateRoutes walks the single mux and returns all registered (method, path) pairs.
func (r *Router) enumerateRoutes() []routeKey {
	var routes []routeKey
	collect := func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, routeKey{Method: method, Path: route})
		return nil
	}
	_ = chi.Walk(r.mux, collect)
	return routes
}

// authMetaPartition holds the categorised results of partitionAuthMetas.
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
// PR-258 RES-5 narrowing: replaces the prior chi.Handle("/internal/v1/*",
// 404) + WithPublicPathPrefix("/internal/v1/") + frameworkPrimaryWhitelist
// triple-mechanism. The new model has a single surface: the predicate.
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

func contractMethodMatches(contractMethod, requestMethod string) bool {
	return contractMethod == requestMethod || (contractMethod == http.MethodGet && requestMethod == http.MethodHead)
}

func contractPathMatches(template, concrete string) bool {
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

// chiRouterAdapter wraps chi.Router to implement cell.RouteMux. prefix is the
// mount prefix the adapter inherited from its parent Route; declarer points to
// the Router-rooted AuthRouteDeclarer so nested auth.Mount calls propagate
// metadata with the fully-composed path.
type chiRouterAdapter struct {
	cr       chi.Router
	prefix   string
	declarer kcell.AuthRouteDeclarer
}

// Compile-time checks: chiRouterAdapter forwards AuthRouteMeta + declares
// its mount prefix so auth.Mount can derive chi-relative registration paths
// from fully-qualified Contract.Path literals.
var _ kcell.AuthRouteDeclarer = (*chiRouterAdapter)(nil)
var _ kcell.Prefixer = (*chiRouterAdapter)(nil)
var _ kcell.HTTPContractDeclarer = (*chiRouterAdapter)(nil)

// Prefix returns the sub-route mount prefix this adapter inherited from
// its parent Route. An empty prefix means the adapter sits directly under
// the Router (Group / top-level) and contributes no path composition.
func (a *chiRouterAdapter) Prefix() string { return a.prefix }

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
// before handing the metadata off to the Router.
func (a *chiRouterAdapter) DeclareAuthMeta(m kcell.AuthRouteMeta) {
	if a.declarer == nil {
		return
	}
	if a.prefix != "" {
		m.Path = joinPrefix(a.prefix, m.Path)
	}
	a.declarer.DeclareAuthMeta(m)
}

// DeclareHTTPContract forwards the route's full ContractSpec to the
// Router-rooted declarer. ContractSpec.Path is already the canonical full
// path, so unlike AuthRouteMeta it is not composed with the chi prefix.
func (a *chiRouterAdapter) DeclareHTTPContract(spec wrapper.ContractSpec) {
	if a.declarer == nil {
		return
	}
	if declarer, ok := a.declarer.(kcell.HTTPContractDeclarer); ok {
		declarer.DeclareHTTPContract(spec)
	}
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
