// Package bootstrap orchestrates the full GoCell application lifecycle:
// config loading, assembly init/start, HTTP serving, event subscriptions,
// background workers, and graceful shutdown.
//
// ref: uber-go/fx app.go — Run/Start/Stop lifecycle, withRollback pattern
// Adopted: sequential startup with transactional rollback on failure;
// LIFO shutdown order for safe resource cleanup.
// Deviated: explicit typed options instead of DI container; direct signal
// handling via runtime/shutdown.Manager.
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/config"
	"github.com/ghbvf/gocell/runtime/http/middleware"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	runtimeoutbox "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/shutdown"
	"github.com/ghbvf/gocell/runtime/worker"
)

// authProvider is discovered post-Init from cells that provide a
// session-aware IntentTokenVerifier (e.g. accesscore's TokenVerifier()).
// Intent-awareness is required so AuthMiddleware can enforce
// token_use=access at the type level.
type authProvider interface {
	TokenVerifier() auth.IntentTokenVerifier
}

// Option configures a Bootstrap instance.
type Option func(*Bootstrap)

const (
	configWatcherCheckerName = "config-watcher"
	configDriftCheckerName   = "config-drift"
	eventRouterCheckerName   = "eventrouter"
)

// WithConfig sets the YAML config path and environment prefix.
func WithConfig(yamlPath, envPrefix string) Option {
	return func(b *Bootstrap) {
		b.configPath = yamlPath
		b.envPrefix = envPrefix
	}
}

// WithHTTPPrimaryAddr sets the primary HTTP listen address used for
// /api/v1/*, public routes, and infra endpoints (/healthz, /readyz, /metrics).
// Default (when this option is absent) is ":8080".
//
// PR-A14a: replaces the former single-listener WithHTTPAddr. See also
// WithHTTPInternalAddr for the /internal/v1/* listener.
func WithHTTPPrimaryAddr(addr string) Option {
	return func(b *Bootstrap) {
		b.primaryAddr = addr
	}
}

// WithHTTPInternalAddr sets the internal HTTP listen address dedicated to
// /internal/v1/* routes (control-plane). Default (when this option is absent)
// is ":9090".
//
// Operators should bind the internal listener to an internal network segment
// so that service-token / mTLS enforcement is the primary line of defence
// for control-plane traffic. Mounting infra endpoints or /api/v1/* on this
// listener returns 404 — the mux physically excludes them.
//
// PR-A14a: the internal listener + `WithInternalMiddleware` replace the
// pre-PR-A14a path-prefix guard.
func WithHTTPInternalAddr(addr string) Option {
	return func(b *Bootstrap) {
		b.internalAddr = addr
	}
}

// WithAssembly sets a pre-built CoreAssembly.
func WithAssembly(asm *assembly.CoreAssembly) Option {
	return func(b *Bootstrap) {
		b.assembly = asm
	}
}

// WithAssemblyID sets the cell ID label used in HTTP metrics emitted by the
// auto-wired metrics collector (R2).
//
// Recommended to set this matching asm.ID() when using WithAssembly(asm);
// omit to reuse assembly ID (auto-derived). Explicit value overrides
// assembly-derived.
//
// When neither WithAssemblyID nor WithAssembly is used, Bootstrap defaults
// to "default" (the ID of the auto-built assembly).
func WithAssemblyID(id string) Option {
	return func(b *Bootstrap) {
		b.assemblyID = id
	}
}

// WithWorkers adds background workers.
func WithWorkers(ws ...worker.Worker) Option {
	return func(b *Bootstrap) {
		b.workers = append(b.workers, ws...)
	}
}

// WithPublisher sets the outbox.Publisher used for event publishing.
//
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.
func WithPublisher(p outbox.Publisher) Option {
	return func(b *Bootstrap) {
		b.publisher = p
	}
}

// WithSubscriber sets the outbox.Subscriber used for event consumption.
//
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.
func WithSubscriber(s outbox.Subscriber) Option {
	return func(b *Bootstrap) {
		b.subscriber = s
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
// Bootstrap.wrapperTracer so eventrouter.ContractTracingMiddleware can create
// consumer-side wrapper.WrapConsumer spans. Without this option, HTTP tracing
// is disabled and WrapConsumer falls back to wrapper.NoopTracer{}; a slog.Warn
// is emitted at bootstrap time so ops notice the silent degrade.
//
// ref: go-zero — observability configuration at app level
func WithTracer(t tracing.Tracer) Option {
	return func(b *Bootstrap) {
		b.routerOpts = append(b.routerOpts, router.WithTracer(t))
		b.wrapperTracer = t
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
		b.routerOpts = append(b.routerOpts, router.WithRateLimiter(rl))
		b.closers = append(b.closers, rl)
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
			b.circuitBreakerNil = true
			return
		}
		b.routerOpts = append(b.routerOpts, router.WithCircuitBreaker(cb))
		b.closers = append(b.closers, cb)
	}
}

// WithManagedCloser registers an adapter or resource that implements
// lifecycle.ContextCloser for LIFO teardown during graceful shutdown.
// The shared shutCtx budget propagates directly to c.Close(ctx), so the
// resource participates in the same shutdown deadline as all other components.
//
// Use this instead of a bare defer c.Close() so that:
//
//   - The resource is closed in LIFO order after HTTP and worker shutdown.
//   - The shared shutdownTimeout ctx is honoured (not an arbitrary timeout).
//   - Startup rollback also triggers the teardown on phase failures.
//
// A nil c is silently ignored (consistent with addCloser semantics).
//
// ref: uber-go/fx Lifecycle.Append OnStop(ctx) — managed teardown registration.
// ref: sigs.k8s.io/controller-runtime pkg/manager/internal.go engageStopProcedure LIFO.
func WithManagedCloser(c kernellifecycle.ContextCloser) Option {
	return func(b *Bootstrap) {
		if c == nil {
			return
		}
		b.closers = append(b.closers, c)
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

// WithAuthMiddleware enables authentication for HTTP business routes with an
// explicitly injected verifier. Use this option when the verifier must be
// supplied directly (tests, advanced scenarios, non-cell composition); use
// WithAuthDiscovery when a cell in the assembly exposes an AuthProvider for
// automatic discovery.
//
// The verifier is applied to the router's middleware chain at Run() time via
// router.WithAuthMiddleware. Public endpoints are declared via auth.Mount
// with Public:true inside each Cell's RegisterRoutes; Bootstrap's FinalizeAuth
// compiles them into the router's auth predicates.
//
// Infra endpoints (/healthz, /readyz, /metrics) are registered on the
// router's outer mux and naturally bypass business-route middleware, so they
// do not need to be declared public.
//
// ref: go-kratos/kratos — auth middleware at service level
// ref: go-zero — per-route WithJwt() opt-in auth
func WithAuthMiddleware(verifier auth.IntentTokenVerifier) Option {
	return func(b *Bootstrap) {
		b.authVerifier = verifier
	}
}

// WithAuthDiscovery opts into auth verifier discovery from the assembly.
// When invoked, Bootstrap inspects every Cell post-Init for an AuthProvider
// implementation and wires the discovered verifier into AuthMiddleware.
// If no provider is found (or multiple conflicting ones), Run returns an
// error — fail-closed.
//
// This is the F3 successor to the dual-purpose WithPublicEndpoints opt-in:
// public routes are now declared via auth.Mount in each Cell, so bootstrap
// only needs an explicit signal that "this assembly expects JWT-backed auth
// and a provider cell will expose it".
//
// Mutually exclusive with WithAuthMiddleware — that option injects the
// verifier directly, bypassing discovery. Using both is rejected by phase 0.
func WithAuthDiscovery() Option {
	return func(b *Bootstrap) {
		b.authDiscovery = true
	}
}

// WithShutdownTimeout overrides the default graceful shutdown timeout.
func WithShutdownTimeout(d time.Duration) Option {
	return func(b *Bootstrap) {
		b.shutdownTimeout = d
	}
}

// WithPreShutdownDelay sets a delay between marking /readyz as 503 and
// starting the HTTP server shutdown. This gives load balancers (e.g.,
// Kubernetes kube-proxy) time to observe the unhealthy readiness probe
// and stop routing new traffic before the server closes connections.
//
// Default is 0 (no delay). Typical Kubernetes deployments use 3-5 seconds.
// The delay counts toward the total shutdownTimeout budget (not additive).
//
// ref: Kubernetes pod shutdown — preStop counts toward terminationGracePeriodSeconds
func WithPreShutdownDelay(d time.Duration) Option {
	return func(b *Bootstrap) {
		b.preShutdownDelay = d
	}
}

// WithPrimaryListener sets a pre-built net.Listener for the primary HTTP
// server, useful in tests to avoid port conflicts. When set, the value from
// WithHTTPPrimaryAddr is ignored for bind purposes (the listener's own
// address is used for logging).
func WithPrimaryListener(ln net.Listener) Option {
	return func(b *Bootstrap) {
		b.primaryListener = ln
	}
}

// WithInternalListener sets a pre-built net.Listener for the internal HTTP
// server. Mirrors WithPrimaryListener for the control-plane listener.
func WithInternalListener(ln net.Listener) Option {
	return func(b *Bootstrap) {
		b.internalListener = ln
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
		b.healthCheckers = append(b.healthCheckers, namedChecker{name: name, fn: fn})
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
		b.readyzDeadline = d
	}
}

// WithRelayHealth registers the relay's named health checkers (one per enabled
// FailureBudget) into the /readyz endpoint. Checkers are named:
//
//   - "outbox-relay-poll"
//   - "outbox-relay-reclaim"
//   - "outbox-relay-cleanup"
//
// Only budgets with a positive threshold are registered; threshold=0 (disabled)
// budgets are silently skipped. A nil relay is rejected at Run() time with a
// fatal error, mirroring the WithCircuitBreaker fail-fast contract.
//
// ref: controller-runtime AddReadyzCheck — named-checker aggregation pattern.
// ref: runtime/bootstrap.WithCircuitBreaker — sibling fail-fast pattern.
func WithRelayHealth(r *runtimeoutbox.Relay) Option {
	return func(b *Bootstrap) {
		if r == nil {
			b.relayHealthNil = true
			return
		}
		checkers := r.HealthCheckers()
		names := make([]string, 0, len(checkers))
		for k := range checkers {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, name := range names {
			fn := checkers[name] // capture loop var
			b.healthCheckers = append(b.healthCheckers, namedChecker{
				name: name,
				fn:   func(ctx context.Context) error { return fn(ctx) },
			})
		}
	}
}

// validateHTTPListenerAddrs fail-fasts on mis-configured HTTP listeners. Each
// side (primary / internal) must have either a non-empty bind address OR a
// caller-injected listener — a listener renders its addr irrelevant because
// the socket is already bound. When both sides bind from addr fields, the
// addrs must differ so Run() never tries to bind two sockets on the same port.
//
// PR-A14a: replaces validateInternalGuard. Internal middleware wiring no
// longer requires a prefix because routes are dispatched by Router.Route at
// registration time.
func (b *Bootstrap) validateHTTPListenerAddrs() error {
	if b.primaryListener == nil && b.primaryAddr == "" {
		return fmt.Errorf("bootstrap: primary HTTP addr or listener required; use WithHTTPPrimaryAddr or WithPrimaryListener")
	}
	if b.internalListener == nil && b.internalAddr == "" {
		return fmt.Errorf("bootstrap: internal HTTP addr or listener required; use WithHTTPInternalAddr or WithInternalListener")
	}
	// Same-addr collision only matters when both sides bind via addr.
	if b.primaryListener == nil && b.internalListener == nil && b.primaryAddr == b.internalAddr {
		return fmt.Errorf("bootstrap: primary and internal HTTP addrs must differ (both %q)", b.primaryAddr)
	}
	return nil
}

// WithAdapterInfo sets static adapter configuration metadata that is exposed
// in /readyz?verbose output. Helps operators verify which storage/bus backends
// are active without inspecting application logs.
func WithAdapterInfo(info map[string]string) Option {
	return func(b *Bootstrap) {
		b.adapterInfo = info
	}
}

// WithVerboseToken sets a token that must be provided via the X-Readyz-Token
// header to access /readyz?verbose output. After PR-A35 verbose requests
// without a matching token receive 401 rather than the previous silent
// downgrade to a plain 200. Operators who deliberately do not want the
// verbose endpoint should use WithVerboseDisabled instead of leaving this
// unset.
func WithVerboseToken(token string) Option {
	return func(b *Bootstrap) {
		b.verboseToken = token
	}
}

// WithVerboseDisabled declares that /readyz?verbose must never be served on
// this deployment. Any ?verbose request is answered with the plain aggregate
// body — the token gate and 401 path are inert. Use this for ephemeral
// deployments (test harnesses, single-node demos) that waive the verbose
// debug channel; production deployments should configure a verbose token
// instead so operators retain a gated diagnostic path.
func WithVerboseDisabled() Option {
	return func(b *Bootstrap) {
		b.verboseDisabled = true
	}
}

// WithEventRouterReadyTimeout overrides the EventRouter Phase-3 ready-wait
// budget. A non-positive value disables the bound (router waits indefinitely
// until ctx cancel). Default: eventrouter.DefaultReadyTimeout (30s).
//
// On timeout, Bootstrap.Run returns an error listing not-ready
// "consumerGroup/topic" pairs so operators can pinpoint the stuck subscription.
func WithEventRouterReadyTimeout(d time.Duration) Option {
	return func(b *Bootstrap) {
		b.eventRouterReadyTimeoutSet = true
		b.eventRouterReadyTimeout = d
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
			b.errorRedactor = fn
			b.routerOpts = append(b.routerOpts, router.WithTracingOptions(middleware.WithErrorRedactor(fn)))
		}
	}
}

// WithConsumerMiddleware registers subscriber-side middleware applied to every
// topic handler before it is passed to the underlying Subscriber.Subscribe call.
// Middleware is applied in registration order; each entry wraps the next, so the
// first registered middleware is outermost at invocation time.
//
// Typical use: inject ConsumerBase.AsMiddleware so every consumer inherits
// two-phase Claimer idempotency, backoff retry, and DLX routing without each
// slice wiring it individually. Observability context restoration
// (entry.Observability → ctx) is the outermost step inside
// outbox.SubscriberWithMiddleware.Subscribe, so middleware registered here
// always sees a context populated with trace_id/request_id/correlation_id.
//
// ref: ThreeDotsLabs/watermill message/router.go — AddMiddleware wraps handlers
// at router level; MassTransit UseMessageRetry — pipeline middleware at
// receive-endpoint configuration.
func WithConsumerMiddleware(mw ...outbox.SubscriptionMiddleware) Option {
	return func(b *Bootstrap) {
		b.consumerMiddleware = append(b.consumerMiddleware, mw...)
	}
}

// WithHookTimeout configures the per-hook deadline for the default
// assembly built when no WithAssembly option is supplied. Zero uses
// assembly.DefaultHookTimeout. Negative values disable per-hook
// timeouts entirely.
//
// When WithAssembly is used, the pre-built assembly's Config.HookTimeout
// takes precedence — this option has no effect. For pre-built assemblies,
// set the value directly on assembly.Config when constructing.
func WithHookTimeout(d time.Duration) Option {
	return func(b *Bootstrap) {
		b.hookTimeout = d
		b.hookTimeoutSet = true
	}
}

// WithMetricsProvider registers a provider-neutral metrics backend used by
// components that need to emit counters/histograms through a common
// abstraction (hook dispatcher drop counters, OTel pool-stats collector,
// custom caller-registered metrics). Pass nil or omit the option to use
// kernel/observability/metrics.NopProvider (no emission).
//
// Callers can read b.MetricsProvider() to register additional metrics
// against the same backend — useful when cmd/* builds both an HTTP
// Collector and a relay Collector on the same Provider instance.
//
// ref: opentelemetry-go otel.GetMeterProvider@main — single global
// provider entry point; GoCell exposes it per-Bootstrap instance to avoid
// mutable global state.
func WithMetricsProvider(p kernelmetrics.Provider) Option {
	return func(b *Bootstrap) {
		if p != nil {
			b.metricsProvider = p
		}
	}
}

// WithInternalMiddleware appends one or more middleware factories to the
// internal HTTP listener's mux. Callers use this to install the service-token
// (HMAC) / mTLS authentication middleware that protects /internal/v1/*
// routes — those routes are physically mounted on the internal mux, so this
// is the sole authentication layer between the network and the handler.
//
// Each middleware must be non-nil; NewE returns an error when any entry is nil.
//
// PR-A14a: replaces WithInternalEndpointGuard(prefix, guard). The prefix
// parameter is no longer needed because /internal/v1/* routes land on a
// physically separate mux by Router construction.
//
// ref: go-kratos/kratos middleware — per-transport middleware chains.
func WithInternalMiddleware(mws ...func(http.Handler) http.Handler) Option {
	return func(b *Bootstrap) {
		b.internalMiddlewares = append(b.internalMiddlewares, mws...)
	}
}

// WithHookObserver registers a cell lifecycle hook observer for the
// default assembly built when no WithAssembly option is supplied.
//
// When WithAssembly is used, the pre-built assembly's Config.HookObserver
// takes precedence — this option has no effect. For pre-built assemblies,
// set the observer directly on assembly.Config when constructing.
//
// A nil observer (including a typed nil wrapping a nil concrete pointer)
// is equivalent to not calling this option.
func WithHookObserver(obs cell.LifecycleHookObserver) Option {
	return func(b *Bootstrap) {
		if cell.IsNilHookObserver(obs) {
			return
		}
		b.hookObserver = obs
	}
}

// WithLifecycleDefaultStartTimeout overrides the per-hook default StartTimeout.
// Zero value retains DefaultStartTimeout (30s). Negative disables default timeout
// (hooks without own StartTimeout will block indefinitely).
func WithLifecycleDefaultStartTimeout(d time.Duration) Option {
	return func(b *Bootstrap) { b.lifecycleDefaultStartTimeout = d }
}

// WithLifecycleDefaultStopTimeout mirrors WithLifecycleDefaultStartTimeout for StopTimeout.
// Zero value retains DefaultStopTimeout (10s). Negative disables default timeout.
func WithLifecycleDefaultStopTimeout(d time.Duration) Option {
	return func(b *Bootstrap) { b.lifecycleDefaultStopTimeout = d }
}

// WithLifecycle registers a hook-registration callback invoked during New()
// (after all options are applied, as part of lifecycle initialisation). Use
// for composition-root Hook registration without needing a Bootstrap reference.
// Multiple WithLifecycle options and direct b.Lifecycle().Append() calls
// accumulate in the order they are applied.
func WithLifecycle(fn func(lc Lifecycle)) Option {
	return func(b *Bootstrap) {
		if fn != nil {
			b.lifecycleRegistrars = append(b.lifecycleRegistrars, fn)
		}
	}
}

// namedChecker pairs a readiness probe name with its check function.
type namedChecker struct {
	name string                      // unique identifier shown in /readyz?verbose output
	fn   func(context.Context) error // nil return = healthy; non-nil = unhealthy
}

// Bootstrap orchestrates the GoCell application lifecycle.
type Bootstrap struct {
	configPath                 string
	envPrefix                  string
	primaryAddr                string // PR-A14a: primary HTTP listener addr (public, /api/v1/*, infra)
	internalAddr               string // PR-A14a: internal HTTP listener addr (/internal/v1/*)
	assembly                   *assembly.CoreAssembly
	workers                    []worker.Worker
	publisher                  outbox.Publisher
	subscriber                 outbox.Subscriber
	routerOpts                 []router.Option
	authVerifier               auth.IntentTokenVerifier
	authDiscovery              bool // true when WithAuthDiscovery was called
	shutdownTimeout            time.Duration
	preShutdownDelay           time.Duration
	primaryListener            net.Listener // PR-A14a: pre-bound listener for primary server (tests)
	internalListener           net.Listener // PR-A14a: pre-bound listener for internal server (tests)
	healthCheckers             []namedChecker
	adapterInfo                map[string]string // static adapter metadata for /readyz verbose
	verboseToken               string            // token for /readyz?verbose access control
	verboseDisabled            bool              // PR-A35: suppress /readyz?verbose entirely (used when operator waives the debug channel)
	closers                    []any             // middleware/adapter dependencies that need shutdown (ContextCloser preferred, io.Closer fallback)
	eventRouterReadyTimeout    time.Duration
	eventRouterReadyTimeoutSet bool
	consumerMiddleware         []outbox.SubscriptionMiddleware
	hookTimeout                time.Duration // applied when assembly not pre-built
	hookTimeoutSet             bool          // distinguishes zero-value "unset" from explicit zero
	hookObserver               cell.LifecycleHookObserver
	metricsProvider            kernelmetrics.Provider
	shutdownMet                *shutdownMetrics // nil only when provider is nil
	shutdownMetricsErr         error            // non-nil when metric registration failed in New
	runOnce                    sync.Once

	// configWatcherFactory creates a config watcher. Defaults to
	// config.NewWatcher. Override per-instance in tests to inject failures
	// without mutating package-level state (safe for parallel tests).
	configWatcherFactory func(string, ...config.WatcherOption) (*config.Watcher, error)

	// circuitBreakerNil is set by WithCircuitBreaker when a nil Allower is
	// passed. Checked at Run() to fail-fast instead of silently skipping CB.
	circuitBreakerNil bool

	// internalMiddlewares hold the ordered middleware stack applied to the
	// internal mux by WithInternalMiddleware. Forwarded to the router at
	// Run() time via router.WithInternalMiddleware.
	internalMiddlewares []func(http.Handler) http.Handler

	// relayHealthNil is set by WithRelayHealth when a nil relay is passed.
	// Checked at Run() to fail-fast rather than silently skipping relay health.
	relayHealthNil bool

	// readyzDeadline overrides the per-probe deadline for /readyz.
	// Zero means use health.Handler default (5 s).
	readyzDeadline time.Duration

	// assemblyID is the cell ID label used in HTTP metrics emitted by the
	// auto-wired metrics collector. Defaults to "default" when empty.
	assemblyID string

	// lifecycle fields wired by WithLifecycle* options.
	lifecycle                    Lifecycle
	lifecycleDefaultStartTimeout time.Duration
	lifecycleDefaultStopTimeout  time.Duration
	lifecycleRegistrars          []func(Lifecycle) // accumulated by WithLifecycle

	// managedResources holds resources registered via WithManagedResource.
	// Each resource is expanded into health checkers, workers, and LIFO teardowns
	// by expandManagedResources() at the beginning of Run().
	managedResources []kernellifecycle.ManagedResource

	// managedResourceTeardowns holds LIFO close functions derived from
	// managedResources during expandManagedResources(). Iterated in reverse
	// order during shutdown so the last-registered resource is closed first.
	// Each func returns the Close error so phase10LIFOTeardown can aggregate
	// it into the Run() return value.
	managedResourceTeardowns []func(ctx context.Context) error

	// managedResourceNil is set by WithManagedResource when a nil resource is
	// passed. Checked in phase0 to fail-fast rather than silently skipping
	// resource registration.
	managedResourceNil bool

	// wrapperTracer is the Tracer supplied via WithTracer. It is threaded into
	// router.WithTracer (HTTP) and ContractTracingMiddleware (consumer) at
	// phase6/phase7 construction. When nil, wrapper.HTTPHandler and
	// wrapper.WrapConsumer each fall back to wrapper.NoopTracer{} at call
	// time, and phase1 logs a slog.Warn so missing tracer wiring surfaces.
	wrapperTracer tracing.Tracer

	// errorRedactor (set via WithErrorRedactor) sanitises error text before
	// it reaches span.RecordError on consumer spans. nil → identity.
	errorRedactor wrapper.ErrorRedactor
}

// New creates a Bootstrap with the given options.
//
// shutdownMetrics are registered against the provider here (plan option B):
// instruments live as long as the Bootstrap, matching the "register at
// start-up" convention used by relay_collector.go and the hook dispatcher.
// On registration failure the error is stored and surfaced by Run() at
// phase0, before any side effects start.
func New(opts ...Option) *Bootstrap {
	b := &Bootstrap{
		// PR-A14a: dual listener defaults. primary 8080 = public (business /
		// api / infra); internal 127.0.0.1:9090 = control-plane /internal/v1/*.
		// The internal listener defaults to loopback so dev-mode deployments
		// without a service-token guard are not trivially reachable across
		// the network. Override with WithHTTPPrimaryAddr / WithHTTPInternalAddr
		// (production: bind internal to an internal-VPC interface).
		primaryAddr:          ":8080",
		internalAddr:         "127.0.0.1:9090",
		shutdownTimeout:      shutdown.DefaultTimeout,
		configWatcherFactory: config.NewWatcher,
		metricsProvider:      kernelmetrics.NopProvider{},
	}
	for _, o := range opts {
		o(b)
	}
	// Create the Lifecycle after all options are applied so that
	// lifecycleDefaultStartTimeout / lifecycleDefaultStopTimeout are set.
	// Zero values are forwarded as-is; NewLifecycle falls back to the
	// DefaultStartTimeout / DefaultStopTimeout constants internally.
	logger := slog.Default()
	b.lifecycle = NewLifecycle(LifecycleConfig{
		DefaultStartTimeout: b.lifecycleDefaultStartTimeout,
		DefaultStopTimeout:  b.lifecycleDefaultStopTimeout,
		Logger:              logger,
	})
	for _, reg := range b.lifecycleRegistrars {
		reg(b.lifecycle)
	}
	// Register shutdown metrics against the (potentially Nop) provider.
	// newShutdownMetrics returns (nil, nil) for NopProvider — that is the
	// correct "disabled" state; nil *shutdownMetrics is safe to call.
	m, err := newShutdownMetrics(b.metricsProvider)
	if err != nil {
		// Store error; phase0 will surface it before any component starts.
		b.shutdownMetricsErr = err
	} else {
		b.shutdownMet = m
	}
	return b
}

// Lifecycle returns the bootstrap's Lifecycle for programmatic Hook
// registration. Must be called after New() returns and before Run() begins;
// not goroutine-safe concurrent with Run(). Hooks registered here are
// appended to those from WithLifecycle options.
func (b *Bootstrap) Lifecycle() Lifecycle {
	return b.lifecycle
}

// MetricsProvider returns the configured provider-neutral metrics backend.
// The returned Provider is never nil; when no WithMetricsProvider option is
// used the NopProvider default surfaces, so callers can register metrics
// unconditionally.
func (b *Bootstrap) MetricsProvider() kernelmetrics.Provider {
	if b.metricsProvider == nil {
		// Defensive: if a future refactor clears the field post-New, keep the
		// contract of never returning nil so call sites can omit nil checks.
		return kernelmetrics.NopProvider{}
	}
	return b.metricsProvider
}

// Run executes the full startup sequence. It blocks until ctx is cancelled
// (or a signal is received), then performs orderly shutdown.
//
// The ten phases and their responsibilities:
//
//	phase0: validate all options before any side effects
//	phase1: load config + create watcher + register middleware closers
//	phase2: init publisher/subscriber (default InMemoryEventBus)
//	phase3: init and start assembly; register LIFO teardown
//	phase4: discover auth verifier; bind config-watcher OnChange; start watcher
//	phase5: build HTTP router + health handler; register all health checkers
//	phase6: register event subscriptions; start event router on runCtx
//	phase7: start HTTP server; wire httpErrCh
//	phase8: start worker group on runCtx; wire workerErrCh
//	phase9: block until external ctx cancel, HTTP error, worker error, or router error
//	phase10: LIFO teardown (readiness flip → pre-shutdown delay → components)
//
// runCtx is derived from context.Background(), NOT from the caller ctx.
// External ctx cancellation only triggers phase9 to return; workers and the
// event router continue until their phase10 teardown functions run.
//
// ref: uber-go/fx app.go (Run/Start/Stop lifecycle, withRollback pattern)
// ref: sigs.k8s.io/controller-runtime pkg/manager/internal.go (engageStopProcedure LIFO)
func (b *Bootstrap) Run(ctx context.Context) error {
	// Guard against double-Run. A second call would create duplicate
	// teardowns and race on shared resources.
	// ref: uber-go/fx App.Run — returns immediately if already started.
	started := false
	b.runOnce.Do(func() { started = true })
	if !started {
		return fmt.Errorf("bootstrap: Run called more than once")
	}

	// Pre-phase: expand ManagedResources into health checkers, workers, and
	// LIFO teardown callbacks. Must run before phase0 so checker validation
	// in phase0ValidateOptions covers resource-contributed checkers.
	b.expandManagedResources()

	if err := b.phase0ValidateOptions(); err != nil {
		return err
	}

	runCtx, s := newPhaseState()
	// Safety net: always release runCtx resources on exit (phase10 also calls
	// runCancel after teardowns, but defer guarantees release on panic paths).
	defer s.runCancel()

	// Register managed-resource teardowns into the phase-state LIFO teardown
	// chain. Appended first so they execute LAST in LIFO order — resources
	// close after assembly/HTTP/workers are stopped (outermost layer), same
	// as fx OnStop registration order.
	//
	// managedResourceTeardowns is in registration order; reversed by the LIFO
	// shutdown loop at the end of Run().
	for _, td := range b.managedResourceTeardowns {
		s.addTeardown(td) // td already returns error; phase10 aggregates via LIFO teardown chain
	}

	rollback := func(cause error) error {
		if s.hh != nil {
			s.hh.SetShuttingDown()
		}
		rctx, cancel := context.WithTimeout(context.Background(), b.shutdownTimeout)
		defer cancel()
		return s.rollback(rctx, cause)
	}

	if err := b.phase1LoadConfig(s); err != nil {
		return err // no side effects started yet; no rollback needed
	}
	b.phase2InitPubSub(s)
	if err := b.phase3InitAssembly(ctx, s); err != nil {
		return rollback(err)
	}
	if err := b.phase3bDiscoverLifecycleContributor(s); err != nil {
		return rollback(err)
	}
	// Lifecycle Start — fail-fast; LIFO rollback is handled by Lifecycle itself.
	// Registered after the asm.Stop teardown (phase3) so that lifecycle.Stop
	// executes before asm.Stop in the LIFO teardown sequence, letting hooks
	// still access cell resources during shutdown.
	// ref: uber-go/fx internal/lifecycle/lifecycle.go — numStarted LIFO rollback.
	if err := b.lifecycle.Start(ctx); err != nil {
		return rollback(fmt.Errorf("bootstrap: lifecycle start: %w", err))
	}
	s.addTeardown(func(stopCtx context.Context) error {
		return b.lifecycle.Stop(stopCtx)
	})
	if err := b.phase4WireAuthAndWatcher(s); err != nil {
		return rollback(err)
	}
	if err := b.phase5BuildHTTPRouter(s); err != nil {
		return rollback(err)
	}
	if err := b.phase6StartEventRouter(runCtx, s); err != nil {
		return rollback(err)
	}
	if err := b.phase7StartHTTPServer(s); err != nil {
		return rollback(err)
	}
	b.phase8StartWorkers(runCtx, s)

	sig := b.phase9AwaitShutdownSignal(ctx, s)
	return b.phase10OrchestrateShutdown(s, sig)
}

// cloneStrings returns a shallow copy of a string slice.
// If src is nil, returns nil (preserving the nil vs empty distinction).
func cloneStrings(src []string) []string {
	if src == nil {
		return nil
	}
	dst := make([]string, len(src))
	copy(dst, src)
	return dst
}

// cloneMap returns a deep copy of a map[string]any. Values that are slices
// or nested maps are recursively cloned so that mutations by one consumer
// cannot affect another.
func filterMapByPrefixes(src map[string]any, prefixes []string) map[string]any {
	dst := make(map[string]any, len(src))
	for k, v := range src {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				dst[k] = v
				break
			}
		}
	}
	return dst
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = config.DeepCloneValue(v)
	}
	return dst
}

// snapshotConfig builds an atomic point-in-time copy of the config.
// If the config implements Snapshotter (the concrete *config from Load does),
// the snapshot is taken under a single read lock for consistency. Otherwise,
// it falls back to iterating Keys()+Get() which is non-atomic but functional.
func snapshotConfig(cfg config.Config) map[string]any {
	if s, ok := cfg.(config.Snapshotter); ok {
		return s.Snapshot()
	}
	snap := make(map[string]any)
	for _, k := range cfg.Keys() {
		snap[k] = cfg.Get(k)
	}
	return snap
}
