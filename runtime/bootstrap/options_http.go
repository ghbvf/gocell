package bootstrap

// options_http.go — With* option functions for the bootstrapHTTP group.
//
// Covers: WithRouterOptions, WithTracer, WithRateLimiter, WithCircuitBreaker,
// WithSecurityHeadersOptions, WithErrorRedactor, WithHealthChecker,
// WithReadyzDeadline, WithAdapterInfo, WithHealthRoutes.
//
// ref: go-kratos/kratos transport/http/server.go — per-server option pattern.
// ref: go-zero — resilience middleware configuration at app level.

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/runtime/http/middleware"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
)

// WithRouterOptions passes options to the router builder.
func WithRouterOptions(opts ...router.Option) Option {
	return func(b *Bootstrap) {
		b.http.routerOpts = append(b.http.routerOpts, opts...)
	}
}

// WithTracer enables distributed tracing. The tracer is forwarded to
// router.WithTracer (the single HTTP request span owner) and stored on
// Bootstrap.http.wrapperTracer so eventrouter.ContractTracingMiddleware can create
// consumer-side wrapper.WrapConsumer spans. Without this option, HTTP tracing
// is disabled and WrapConsumer falls back to wrapper.NoopTracer{}; a slog.Warn
// is emitted at bootstrap time so ops notice the silent degrade.
//
// ref: go-zero — observability configuration at app level
func WithTracer(t tracing.Tracer) Option {
	return func(b *Bootstrap) {
		b.http.routerOpts = append(b.http.routerOpts,
			router.WithTracer(t),
			// Skip span creation for canonical infra probe endpoints
			// (/healthz, /readyz, /metrics) so high-rate liveness/readiness
			// probes do not pollute trace storage. Pre-PR-A14b this was
			// implicit because probe routes lived on the outer mux and
			// bypassed Tracing entirely; with per-listener routers the
			// HealthListener's full middleware chain runs, so we must
			// install the filter explicitly here.
			router.WithTracingOptions(middleware.WithProbeFilter(middleware.DefaultProbeFilter)),
		)
		b.http.wrapperTracer = t
	}
}

// WithRateLimiter enables per-IP rate limiting for HTTP requests. The limiter
// is forwarded to the router's middleware chain via router.WithRateLimiter.
// If the limiter implements lifecycle.ContextCloser or io.Closer
// (e.g. adapters/ratelimit.Limiter), Bootstrap registers it for teardown on
// shutdown and startup rollback. ContextCloser is preferred so the shared
// shutCtx budget flows through to the resource.
//
// Note: the rate limiter uses the client IP from RealIP middleware as the
// bucket key. Ensure WithTrustedProxies is correctly configured; an overly
// permissive trust list allows X-Forwarded-For spoofing, which bypasses
// rate limiting.
//
// ref: go-zero — rate limiting configuration at app level
// ref: uber-go/fx lifecycle OnStop(ctx) — ContextCloser preferred over io.Closer
func WithRateLimiter(rl middleware.RateLimiter) Option {
	return func(b *Bootstrap) {
		b.http.routerOpts = append(b.http.routerOpts, router.WithRateLimiter(rl))
		b.lc.closers = append(b.lc.closers, rl)
	}
}

// WithCircuitBreaker enables circuit breaker protection for HTTP requests.
// The breaker is forwarded to the router's middleware chain via
// router.WithCircuitBreaker. If the breaker implements lifecycle.ContextCloser
// or io.Closer, Bootstrap registers it for teardown on shutdown and startup
// rollback. ContextCloser is preferred so the shared shutCtx budget flows
// through to the resource.
//
// A nil cb is rejected at Run() time with a fatal error so operators are not
// silently left without circuit-breaker protection.
//
// ref: go-zero — resilience middleware configuration at app level
// ref: kubernetes/kubernetes apiserver — option fail-fast at startup
// ref: uber-go/fx lifecycle OnStop(ctx) — ContextCloser preferred over io.Closer
func WithCircuitBreaker(cb middleware.Allower) Option {
	return func(b *Bootstrap) {
		if cb == nil || middleware.IsTypedNilAllower(cb) {
			b.http.circuitBreakerNil = true
			return
		}
		b.http.routerOpts = append(b.http.routerOpts, router.WithCircuitBreaker(cb))
		b.lc.closers = append(b.lc.closers, cb)
	}
}

// WithSecurityHeadersOptions configures HSTS and other security header
// directives. This is a convenience wrapper around
// WithRouterOptions(router.WithSecurityHeadersOptions(...)).
//
// ref: unrolled/secure — configurable HSTS directives via struct fields
func WithSecurityHeadersOptions(opts ...middleware.SecurityHeadersOption) Option {
	return func(b *Bootstrap) {
		b.http.routerOpts = append(b.http.routerOpts, router.WithSecurityHeadersOptions(opts...))
	}
}

// WithErrorRedactor installs a wrapper.ErrorRedactor that scrubs error text
// before it reaches span.RecordError on HTTP request spans and consumer-side
// CONSUME spans. A nil fn disables redaction (identity semantics).
//
// Use when strict source-side sanitisation is required (regulated
// environments); otherwise leave unset and let the OTel span processor /
// exporter filter handle scrubbing at export time.
func WithErrorRedactor(fn wrapper.ErrorRedactor) Option {
	return func(b *Bootstrap) {
		if fn != nil {
			b.http.errorRedactor = fn
			b.http.routerOpts = append(b.http.routerOpts, router.WithTracingOptions(middleware.WithErrorRedactor(fn)))
		}
	}
}

// WithHealthChecker registers a named readiness checker that contributes to
// aggregate /readyz and appears in `/readyz?verbose` responses. Use this to
// wire adapter health probes (e.g., conn.Health for RabbitMQ) without
// bootstrap depending on adapter types.
//
// Accepts func(context.Context) error so callers can honour the /readyz probe
// deadline. Validation (empty name, nil fn) is deferred to Run() where it fires
// at Step 0 before any component starts, returning an error directly.
func WithHealthChecker(name string, fn func(context.Context) error) Option {
	return func(b *Bootstrap) {
		b.http.healthCheckers = append(b.http.healthCheckers, namedChecker{name: name, fn: fn})
	}
}

// WithReadyzDeadline overrides the per-probe deadline for /readyz. All
// registered checkers must complete within this duration; checkers that exceed
// it are reported as status="timeout". A zero or negative value uses the
// health.Handler default (5 s, Kubernetes readiness probe convention).
//
// ref: k8s.io/apiserver/pkg/server/healthz — server-side readyz deadline
// independent of the kubelet HTTP connection deadline.
func WithReadyzDeadline(d time.Duration) Option {
	return func(b *Bootstrap) {
		b.http.readyzDeadline = d
	}
}

// WithAdapterInfo sets static adapter configuration metadata that is exposed
// in /readyz?verbose output. Helps operators verify which storage/bus backends
// are active without inspecting application logs.
func WithAdapterInfo(info map[string]string) Option {
	return func(b *Bootstrap) {
		b.http.adapterInfo = info
	}
}

// WithHealthRoutes accumulates HealthRouteGroupOption values that customise
// the framework-owned /healthz, /readyz, and /metrics route groups. The
// canonical use cases are:
//
//	bootstrap.WithHealthRoutes(bootstrap.WithMetricsHandler(promHandler))
//	bootstrap.WithHealthRoutes(bootstrap.WithReadyzVerboseToken(token))
//	bootstrap.WithHealthRoutes(bootstrap.WithReadyzVerboseDisabled())
//
// Multiple WithHealthRoutes calls accumulate; later options for the same
// concern (metrics handler, verbose-token, verbose-disabled) overwrite earlier
// ones in the order they were appended. Pass nil-valued options at your peril —
// they overwrite any previously-set value with the zero value.
//
// PR-A35 / PR269 round-3 strict semantics: a request with ?verbose= but no
// matching readyz verbose-token / disabled flag yields 401
// ErrReadyzVerboseDenied at the health handler layer, never a silent
// downgrade to plain 200.
func WithHealthRoutes(opts ...HealthRouteGroupOption) Option {
	return func(b *Bootstrap) {
		b.http.healthRouteGroupOpts = append(b.http.healthRouteGroupOpts, opts...)
	}
}
