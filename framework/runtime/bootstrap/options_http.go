package bootstrap

// options_http.go — With* option functions covering HTTP listener, router,
// health, and middleware setup.
//
// Covers: WithRouterOptions, WithTracer, WithRateLimiter, WithCircuitBreaker,
// WithSecurityHeadersOptions, WithHealthChecker, WithReadyzDeadline,
// WithAdapterInfo, WithHealthRoutes, WithPrimaryAuthorizer.
//
// Note: WithRateLimiter and WithCircuitBreaker also append to b.closers (lifecycle teardown).
//
// ref: go-kratos/kratos transport/http/server.go — per-server option pattern.
// ref: go-zero — resilience middleware configuration at app level.

import (
	"context"
	"time"

	kerneldepgraph "github.com/ghbvf/gocell/framework/kernel/depgraph"
	"github.com/ghbvf/gocell/framework/kernel/healthz"
	"github.com/ghbvf/gocell/framework/kernel/metadata"
	"github.com/ghbvf/gocell/framework/kernel/wrapper"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	idemhttp "github.com/ghbvf/gocell/framework/runtime/http/idempotency"
	"github.com/ghbvf/gocell/framework/runtime/http/middleware"
	"github.com/ghbvf/gocell/framework/runtime/http/router"
	"github.com/ghbvf/gocell/framework/runtime/sysinfo"
)

// WithHealthAggregator injects a custom healthz.Aggregator into the bootstrap.
// The aggregator is used by the health.Handler (/readyz) and by drainProbes
// to register framework-level probes.
//
// Both bare-nil and typed-nil interface values are rejected at phase0 with
// errcode ERR_VALIDATION_FAILED. When WithHealthAggregator is not called,
// bootstrap constructs a default obshealthz.NewAggregator in phase0 (with
// b.clock and, if set, the WithReadyzDeadline per-probe deadline) — the option
// exists for hosts that want to substitute (e.g. tests with a fake aggregator).
// A custom aggregator owns its own deadline, so it cannot combine with
// WithReadyzDeadline (that combination fails fast at phase0).
//
// ref: runtime-api.md strong-dependency wiring option pattern.
func WithHealthAggregator(agg healthz.Aggregator) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(agg) {
			b.healthAggregatorNil = true
			return
		}
		b.healthAggregator = agg
	}
}

// WithRouterOptions passes options to the router builder.
func WithRouterOptions(opts ...router.Option) Option {
	return func(b *Bootstrap) {
		b.routerOpts = append(b.routerOpts, opts...)
	}
}

// WithTracer enables distributed tracing. The tracer is forwarded to
// router.WithTracer (the single HTTP request span owner) and stored on
// Bootstrap.wrapperTracer so eventrouter.NewContractTracingSubscriber can create
// consumer-side wrapper.WrapSubscriber spans. Without this option, HTTP tracing
// is disabled and WrapSubscriber falls back to wrapper.NoopTracer{}; a slog.Warn
// is emitted at bootstrap time so ops notice the silent degrade.
//
// ref: go-zero — observability configuration at app level
func WithTracer(t wrapper.Tracer) Option {
	return func(b *Bootstrap) {
		b.routerOpts = append(
			b.routerOpts,
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
		b.wrapperTracer = t
	}
}

// WithRateLimiter enables per-IP rate limiting for HTTP requests. The limiter
// is forwarded to the router's middleware chain via router.WithRateLimiter.
// Also registers the resource for LIFO teardown via b.closers.
// If the limiter implements lifecycle.ContextCloser or io.Closer
// (e.g. adapters/ratelimit.Limiter), Bootstrap registers it for teardown on
// shutdown and startup rollback. ContextCloser is preferred so the shared
// shutCtx budget flows through to the resource.
//
// Both bare-nil and typed-nil (non-nil interface holding a nil pointer) are
// rejected at phase0 with a fatal error so operators are not silently left
// without rate-limiter protection. This mirrors the WithCircuitBreaker /
// WithManagedResource fail-fast pattern.
//
// Note: the rate limiter uses the client IP from RealIP middleware as the
// bucket key. Ensure WithTrustedProxies is correctly configured; an overly
// permissive trust list allows X-Forwarded-For spoofing, which bypasses
// rate limiting.
//
// ref: go-zero — rate limiting configuration at app level
// ref: uber-go/fx lifecycle OnStop(ctx) — ContextCloser preferred over io.Closer
// ref: kubernetes-sigs/controller-runtime pkg/manager/manager.go — strong
// dependency fail-fast.
func WithRateLimiter(rl middleware.RateLimiter) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(rl) {
			b.rateLimiterNil = true
			return
		}
		b.routerOpts = append(b.routerOpts, router.WithRateLimiter(rl))
		b.closers = append(b.closers, rl)
	}
}

// WithIdempotencyStore enables HTTP idempotency middleware for mutating methods
// (POST/PUT/PATCH/DELETE). The store is forwarded to the router's middleware
// chain via router.WithIdempotency so replayed responses are served directly
// without re-invoking business handlers.
//
// Both bare-nil and typed-nil (non-nil interface holding a nil pointer) are
// rejected at phase0 with a fatal error so operators are not silently left
// without idempotency protection.
//
// Concrete implementations:
//   - production: adapters/redis.NewHTTPIdempotencyStore(client, ns)
//   - tests: idempotency.NewMemStore(clk) from runtime/http/idempotency
//
// ref: runtime-api.md strong-dependency wiring option pattern.
func WithIdempotencyStore(store idemhttp.Store) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(store) {
			b.idempotencyStoreNil = true
			return
		}
		b.routerOpts = append(b.routerOpts, router.WithIdempotency(store))
	}
}

// WithCircuitBreaker enables circuit breaker protection for HTTP requests.
// The breaker is forwarded to the router's middleware chain via
// router.WithCircuitBreaker. Also registers the resource for LIFO teardown via b.closers.
// If the breaker implements lifecycle.ContextCloser
// or io.Closer, Bootstrap registers it for teardown on shutdown and startup
// rollback. ContextCloser is preferred so the tearCtx (stage 3 budget; see
// WithShutdownTimeout godoc) flows through to the resource.
//
// Both bare-nil and typed-nil (non-nil interface holding a nil pointer) are
// rejected at phase0 with a fatal error so operators are not silently left
// without circuit-breaker protection.
//
// ref: go-zero — resilience middleware configuration at app level
// ref: kubernetes/kubernetes apiserver — option fail-fast at startup
// ref: uber-go/fx lifecycle OnStop(ctx) — ContextCloser preferred over io.Closer
func WithCircuitBreaker(cb middleware.Allower) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(cb) {
			b.circuitBreakerNil = true
			return
		}
		b.routerOpts = append(b.routerOpts, router.WithCircuitBreaker(cb))
		b.closers = append(b.closers, cb)
	}
}

// WithSecurityHeadersOptions configures HSTS and other security header
// directives. This is a convenience wrapper around
// WithRouterOptions(router.WithSecurityHeadersOptions(...)).
//
// ref: unrolled/secure — configurable HSTS directives via struct fields
func WithSecurityHeadersOptions(opts ...middleware.SecurityHeadersOption) Option {
	return func(b *Bootstrap) {
		b.routerOpts = append(b.routerOpts, router.WithSecurityHeadersOptions(opts...))
	}
}

// WithHealthChecker registers a named readiness checker that contributes to
// aggregate /readyz and appears in `/readyz?verbose` responses. Use this to
// wire adapter health probes (e.g., conn.Health for RabbitMQ) without
// bootstrap depending on adapter types.
//
// The first parameter MUST be a declared healthz.ProbeName const
// (e.g. adapters/postgres.ProbeReady, adapters/rabbitmq.ProbeReady).
// Bare untyped string literals are accepted at compile time but are
// rejected by archtest PROBENAME-SEALED-FUNNEL-01/A2 (typed funnel
// discipline). NewProbeName(s) and MustProbeName(s) are NOT valid here:
// NewProbeName is for adapter packages declaring their own typed const
// (not for composition-root callsites), and MustProbeName is restricted
// to kernel/healthz internal and test-only use (A4b rejects production
// callers outside kernel/healthz). This is the sole sanctioned entry
// point for wiring adapter/framework readiness probes into bootstrap.
//
// Accepts func(context.Context) error so callers can honor the /readyz probe
// deadline. Validation (empty name, nil fn) is deferred to Run() where it fires
// at Step 0 before any component starts, returning an error directly.
func WithHealthChecker(name healthz.ProbeName, fn func(context.Context) error) Option {
	return func(b *Bootstrap) {
		b.healthCheckers = append(b.healthCheckers, namedChecker{name: name, fn: fn})
	}
}

// WithReadyzDeadline overrides the per-probe deadline for /readyz. All
// registered probes must complete within this duration; probes that exceed it
// are reported as status="timeout". A zero or negative value uses the default
// aggregator's 5 s default (Kubernetes readiness probe convention).
//
// The deadline is owned by the healthz.Aggregator: bootstrap applies it to the
// default aggregator it builds in phase0. It therefore CANNOT combine with
// WithHealthAggregator (a custom aggregator owns its own deadline) — that
// combination fails fast at phase0. Configure a custom aggregator's deadline
// via runtime/observability/healthz.WithDeadline at construction instead.
//
// ref: k8s.io/apiserver/pkg/server/healthz — server-side readyz deadline
// independent of the kubelet HTTP connection deadline.
func WithReadyzDeadline(d time.Duration) Option {
	return func(b *Bootstrap) {
		b.readyzDeadline = d
	}
}

// WithAdapterInfo sets static adapter configuration metadata that is exposed
// in /readyz?verbose output. Helps operators verify which storage/bus backends
// are active without inspecting application logs.
func WithAdapterInfo(info map[string]string) Option {
	return func(b *Bootstrap) {
		b.adapterInfo = info
	}
}

// WithSystemInfo sets startup-stable build/environment/deployment metadata for
// the admin system endpoint. Bootstrap fills assembly name and cell order from
// its CoreAssembly so callers cannot drift from the mounted cell set.
func WithSystemInfo(cfg sysinfo.Config) Option {
	return func(b *Bootstrap) {
		b.systemInfoConfig = cfg
	}
}

// WithDevtoolsCatalog enables the GET /devtools/catalog endpoint on
// the primary listener with admin-only gating (auth.AnyRole("admin")).
//
// Pass nil pm to leave the endpoint disabled; this allows composition roots
// to attempt metadata parse and degrade gracefully (no error / no warning
// from bootstrap layer when parse fails).
//
// pkgGraph is the build-time generated package dependency graph (from
// cmd/corebundle/catalog_gen.go generatedPackageGraph). Pass nil to omit the
// packageDeps block entirely. The graph is produced at build time by running
// `go generate ./cmd/corebundle/` and committed as catalog_gen.go.
//
// wireSummaries supplies per-cell wire surface summaries derived from cell.go
// marker comments via BuildCellWireSummaries. Pass nil to omit the wireSummary
// field on all Cell entities (safe default when markers are absent).
func WithDevtoolsCatalog(
	pm *metadata.ProjectMeta,
	root string,
	pkgGraph *kerneldepgraph.Graph,
	wireSummaries ...[]metadata.CellWireSummary,
) Option {
	return func(b *Bootstrap) {
		b.devtoolsMeta = pm
		b.devtoolsRoot = root
		b.devtoolsPkgGraph = pkgGraph
		if len(wireSummaries) > 0 {
			b.devtoolsWireSummaries = wireSummaries[0]
		}
	}
}

// WithHealthRoutes accumulates HealthRouteGroupOption values that customize
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
// downgrade to plain 20.
func WithHealthRoutes(opts ...HealthRouteGroupOption) Option {
	return func(b *Bootstrap) {
		b.healthRouteGroupOpts = append(b.healthRouteGroupOpts, opts...)
	}
}

// WithPrimaryAuthorizer wires an ABAC Authorizer (PDP) into the primary
// listener's request context. A middleware installed on the primary listener's
// router calls auth.WithAuthorizer(ctx, a) on every incoming request so that
// route Policies built with auth.RequirePermission can find the PDP.
//
// This is the symmetric counterpart to the JWT AuthMiddleware already installed
// on the primary listener: both inject per-request state before route handlers
// run.
//
// Strong-dependency fail-fast (runtime-api.md option 范式): both bare-nil and
// typed-nil (non-nil interface holding a nil pointer) are rejected at phase0
// with a clear error. Passing a nil Authorizer would silently leave
// RequirePermission fail-closed (deny all), masking a misconfiguration. Use
// this option only when the PDP is ready; omit it entirely when ABAC gating is
// not required on this assembly.
//
// The Authorizer is installed ONLY on cell.PrimaryListener. Internal, Health,
// and Admin listeners do not receive it: internal routes use service-token
// identity (no ABAC policy gate), and health/admin listeners are out-of-band.
//
// ref: runtime-api.md — strong-dependency fail-fast option pattern.
func WithPrimaryAuthorizer(a auth.Authorizer) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(a) {
			b.primaryAuthorizerNil = true
			return
		}
		b.primaryAuthorizer = a
	}
}
