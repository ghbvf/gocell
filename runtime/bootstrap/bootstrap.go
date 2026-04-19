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
	"io"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
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
// session-aware IntentTokenVerifier (e.g. access-core's TokenVerifier()).
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

// WithHTTPAddr sets the HTTP listen address (default ":8080").
func WithHTTPAddr(addr string) Option {
	return func(b *Bootstrap) {
		b.httpAddr = addr
	}
}

// WithAssembly sets a pre-built CoreAssembly.
func WithAssembly(asm *assembly.CoreAssembly) Option {
	return func(b *Bootstrap) {
		b.assembly = asm
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

// WithTracer enables distributed tracing for HTTP requests. The tracer is
// forwarded to the router's middleware chain via router.WithTracer.
//
// ref: go-zero — observability configuration at app level
func WithTracer(t tracing.Tracer) Option {
	return func(b *Bootstrap) {
		b.routerOpts = append(b.routerOpts, router.WithTracer(t))
	}
}

// WithRateLimiter enables per-IP rate limiting for HTTP requests. The limiter
// is forwarded to the router's middleware chain via router.WithRateLimiter.
// If the limiter implements io.Closer (e.g. adapters/ratelimit.Limiter),
// Bootstrap registers it for teardown on shutdown and startup rollback.
//
// Note: the rate limiter uses the client IP from RealIP middleware as the
// bucket key. Ensure WithTrustedProxies is correctly configured; an overly
// permissive trust list allows X-Forwarded-For spoofing, which bypasses
// rate limiting.
//
// ref: go-zero — rate limiting configuration at app level
func WithRateLimiter(rl middleware.RateLimiter) Option {
	return func(b *Bootstrap) {
		b.routerOpts = append(b.routerOpts, router.WithRateLimiter(rl))
		if cl, ok := rl.(io.Closer); ok {
			b.closers = append(b.closers, cl)
		}
	}
}

// WithCircuitBreaker enables circuit breaker protection for HTTP requests.
// The breaker is forwarded to the router's middleware chain via
// router.WithCircuitBreaker. If the breaker implements io.Closer,
// Bootstrap registers it for teardown on shutdown and startup rollback.
//
// A nil cb is rejected at Run() time with a fatal error so operators are not
// silently left without circuit-breaker protection.
//
// ref: go-zero — resilience middleware configuration at app level
// ref: kubernetes/kubernetes apiserver — option fail-fast at startup
func WithCircuitBreaker(cb middleware.Allower) Option {
	return func(b *Bootstrap) {
		if cb == nil || middleware.IsTypedNilAllower(cb) {
			b.circuitBreakerNil = true
			return
		}
		b.routerOpts = append(b.routerOpts, router.WithCircuitBreaker(cb))
		if cl, ok := cb.(io.Closer); ok {
			b.closers = append(b.closers, cl)
		}
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
// explicitly injected verifier. Complementary to WithPublicEndpoints:
// use this option when the verifier must be supplied directly (tests,
// advanced scenarios, non-cell composition); use WithPublicEndpoints when a
// cell in the assembly exposes an AuthProvider for automatic discovery.
//
// The verifier is applied to the router's middleware chain at Run() time via
// router.WithAuthMiddleware.
//
// publicEndpoints specifies business-route paths that bypass authentication
// (path-only match). If nil, no business routes are public (fail-closed).
// For method-aware bypass, combine with WithPublicEndpoints (but note the two
// options are mutually exclusive at Run() time; see the Run error path).
//
// Infra endpoints (/healthz, /readyz, /metrics) are registered on the
// router's outer mux and naturally bypass business-route middleware, so they
// do not need to be listed in publicEndpoints.
//
// ref: go-kratos/kratos — auth middleware at service level
// ref: go-zero — per-route WithJwt() opt-in auth
func WithAuthMiddleware(verifier auth.IntentTokenVerifier, publicEndpoints []string) Option {
	return func(b *Bootstrap) {
		b.authVerifier = verifier
		b.authPublicEndpoints = publicEndpoints
	}
}

// WithPublicEndpoints declares endpoints that bypass authentication when an
// AuthProvider cell is discovered post-Init. Unlike WithAuthMiddleware
// (which provides the verifier explicitly), this option defers verifier
// resolution to Run() time.
//
// Each entry must be in "METHOD /path" format (e.g. "POST /api/v1/auth/login").
// Entries without a method prefix panic immediately at Option-construction time
// (fail-fast — surfaces in unit tests and dev workflows before Run()).
// Run() still re-validates via router.NewE for defense-in-depth. Entries with
// method GET also automatically cover HEAD requests, following stdlib ServeMux
// and chi v5 semantics (RFC 7231 §4.3.2).
//
// The same entries configure all three trust boundaries simultaneously:
//   - Auth bypass: the matching (method + path) pair skips JWT verification.
//   - Tracing: matching requests start a new trace root instead of inheriting
//     an upstream traceparent header.
//   - Request-ID: matching requests reject client-supplied X-Request-Id headers.
//
// WithPublicEndpoints must not be combined with WithAuthMiddleware — use one
// or the other. If both are called, Run() returns an error immediately
// (fail-fast) and the service will not start.
//
// Example:
//
//	bootstrap.WithPublicEndpoints([]string{
//	    "POST /api/v1/auth/login",
//	    "POST /api/v1/auth/refresh",
//	})
//
// ref: Go 1.22 net/http ServeMux pattern grammar "[METHOD] PATH"
func WithPublicEndpoints(endpoints []string) Option {
	// Preflight validation: fail-fast at Option-construction time so
	// malformed entries surface in unit tests and dev workflows before Run().
	// Run() still re-validates via router.NewE for defense-in-depth.
	if _, err := middleware.CompilePublicEndpoints(endpoints); err != nil {
		panic(fmt.Sprintf("bootstrap.WithPublicEndpoints: %v", err))
	}
	return func(b *Bootstrap) {
		b.authPublicEndpoints = endpoints
		b.authDiscovery = true
	}
}

// WithPasswordResetExemptEndpoints declares routes that remain reachable while
// an authenticated token carries password_reset_required=true. Each entry must
// be in "METHOD /path" format; path templates may use {xxx} wildcards.
//
// Composition roots supply these paths explicitly so runtime/auth does not
// encode cell-specific routes — the default (no entries) is fail-closed.
//
// Forwarded to router.WithPasswordResetExemptEndpoints at Run() time.
func WithPasswordResetExemptEndpoints(endpoints []string) Option {
	return func(b *Bootstrap) {
		b.passwordResetExemptEndpoints = endpoints
	}
}

// WithPasswordResetChangeEndpointHint sets the client-navigation hint emitted
// as details.change_password_endpoint in 403 ERR_AUTH_PASSWORD_RESET_REQUIRED
// responses. Empty (default) omits the hint. Composition roots typically set
// this to the same path they list in WithPasswordResetExemptEndpoints that
// finishes the reset flow — keeping business path literals out of runtime/auth.
//
// Forwarded to router.WithPasswordResetChangeEndpointHint at Run() time.
func WithPasswordResetChangeEndpointHint(hint string) Option {
	return func(b *Bootstrap) {
		b.passwordResetChangeEndpointHint = hint
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

// WithListener sets a pre-built net.Listener for the HTTP server,
// useful in tests to avoid port conflicts.
func WithListener(ln net.Listener) Option {
	return func(b *Bootstrap) {
		b.listener = ln
	}
}

// WithHealthChecker registers a named readiness checker that contributes to
// aggregate /readyz and appears in `/readyz?verbose` responses. Use this to
// wire adapter health probes (e.g., conn.Health for RabbitMQ) without
// bootstrap depending on adapter types.
//
// Accepts func() error so callers do not need to import runtime/http/health.
// Validation (empty name, nil fn) is deferred to Run() where it fires at
// Step 0 before any component starts, returning an error directly.
func WithHealthChecker(name string, fn func() error) Option {
	return func(b *Bootstrap) {
		b.healthCheckers = append(b.healthCheckers, namedChecker{name: name, fn: fn})
	}
}

// BrokerHealthChecker is the narrow interface GoCell's bootstrap consumes
// to aggregate message-broker connectivity into the /readyz endpoint.
// Implementations must return nil when the broker is reachable and able to
// publish/consume; any non-nil error flips /readyz to 503.
//
// ref: github.com/ghbvf/gocell/adapters/rabbitmq.Connection — the canonical
// implementer (its Health method exposes ConnectionState four-state model:
// Connecting, Connected, Disconnected, Terminal).
//
// ref: docs/references/202604181900-outbox-wire-framework-comparison.md —
// design analysis concluded three surveyed frameworks (Watermill, fx,
// Kratos) lack a directly reusable broker-health contract; GoCell's
// four-state model surpasses watermill-amqp's binary IsConnected() by
// preserving sub-state for debugging.
type BrokerHealthChecker interface {
	Health(ctx context.Context) error
}

// WithBrokerHealth registers the given broker's Health method as a /readyz
// aggregator under the name "rabbitmq". It is a thin convenience over
// WithHealthChecker that picks a canonical name and adapts the interface
// to func() error, calling Health with a 5-second timeout per K8s readiness
// probe convention.
//
// A nil (or typed-nil) bc is rejected at Run() time with a fatal error so
// operators are not silently left with a checker that nil-derefs on the
// first probe. Mirrors WithCircuitBreaker's fail-fast contract.
//
// ref: github.com/ghbvf/gocell/runtime/bootstrap.WithCircuitBreaker — sibling
// fail-fast pattern for nil option arguments.
func WithBrokerHealth(bc BrokerHealthChecker) Option {
	return func(b *Bootstrap) {
		if isNilBrokerHealthChecker(bc) {
			b.brokerHealthNil = true
			return
		}
		b.healthCheckers = append(b.healthCheckers, namedChecker{
			name: "rabbitmq",
			fn: func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return bc.Health(ctx)
			},
		})
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
// fatal error, mirroring the WithBrokerHealth fail-fast contract.
//
// ref: controller-runtime AddReadyzCheck — named-checker aggregation pattern.
// ref: runtime/bootstrap.WithBrokerHealth — sibling fail-fast pattern.
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
			b.healthCheckers = append(b.healthCheckers, namedChecker{name: name, fn: checkers[name]})
		}
	}
}

// isNilBrokerHealthChecker detects both plain-nil interface values and the
// "typed nil" gotcha (non-nil interface wrapping a nil pointer/slice/etc.).
// The typed-nil case would satisfy `bc != nil` but panic on method dispatch.
//
// ref: github.com/ghbvf/gocell/runtime/http/middleware.IsTypedNilAllower —
// mirrors the allower helper so the bootstrap-layer fail-fast pattern is
// symmetric across Option variants.
func isNilBrokerHealthChecker(bc BrokerHealthChecker) bool {
	if bc == nil {
		return true
	}
	v := reflect.ValueOf(bc)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}

// validateInternalGuard validates the internal endpoint guard configuration.
// Returns an error when prefix or guard violate their constraints so Run()
// can fail-fast before any side effects.
func (b *Bootstrap) validateInternalGuard() error {
	if b.internalGuardPrefix == "" && b.internalGuard == nil {
		return nil // option not used
	}
	if b.internalGuardPrefix == "" {
		return fmt.Errorf("bootstrap: internal guard prefix must not be empty")
	}
	if !strings.HasPrefix(b.internalGuardPrefix, "/") {
		return fmt.Errorf("bootstrap: internal guard prefix %q must start with '/'", b.internalGuardPrefix)
	}
	if !strings.HasSuffix(b.internalGuardPrefix, "/") {
		return fmt.Errorf("bootstrap: internal guard prefix %q must end with '/'", b.internalGuardPrefix)
	}
	if b.internalGuard == nil {
		return fmt.Errorf("bootstrap: internal guard must not be nil when prefix %q is set", b.internalGuardPrefix)
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
// header to access /readyz?verbose output. When not set, verbose mode is
// unrestricted (backward compatible).
func WithVerboseToken(token string) Option {
	return func(b *Bootstrap) {
		b.verboseToken = token
	}
}

// WithDisableObservabilityRestore prevents the bootstrap from registering
// ObservabilityContextMiddleware on the event subscriber. When set, consumer
// handlers will not have request_id/correlation_id/trace_id restored from
// entry metadata into the handler context. This is the canonical kill switch
// for the consume-side observability bridge.
func WithDisableObservabilityRestore() Option {
	return func(b *Bootstrap) {
		b.disableObservabilityRestore = true
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

// WithConsumerMiddleware registers subscriber-side middleware applied to every
// topic handler before it is passed to the underlying Subscriber.Subscribe call.
// Middleware is applied in registration order; each entry wraps the next, so the
// first registered middleware is outermost at invocation time.
//
// Typical use: inject ConsumerBase.AsMiddleware so every consumer inherits
// two-phase Claimer idempotency, backoff retry, and DLX routing without each
// slice wiring it individually. Bootstrap always prepends
// ObservabilityContextMiddleware (unless disabled via
// WithDisableObservabilityRestore) so trace_id/request_id restoration runs
// before any middleware registered here.
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

// WithInternalEndpointGuard registers a guard middleware that protects every
// HTTP route whose path starts with prefix. The canonical value for prefix is
// "/internal/v1/" (must start and end with '/').
//
// guard is a standard http.Handler middleware factory (func(http.Handler) http.Handler).
// Run() validates both constraints and returns an error immediately (fail-fast)
// when either is violated:
//   - prefix empty / not starting with '/' / not ending with '/'
//   - guard nil
//
// The guard is wired into the router's business mux via
// router.WithInternalPathPrefixGuard; infrastructure endpoints (/healthz,
// /readyz, /metrics) are on outerMux and are never reached by the guard.
//
// Actual token-validation logic lives in the guard function — this option is
// a pure wiring point (injection, not policy).
//
// ref: go-kratos/kratos middleware/selector — default-deny + Option injection.
func WithInternalEndpointGuard(prefix string, guard func(http.Handler) http.Handler) Option {
	return func(b *Bootstrap) {
		b.internalGuardPrefix = prefix
		b.internalGuard = guard
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
	name string       // unique identifier shown in /readyz?verbose output
	fn   func() error // nil return = healthy; non-nil = unhealthy
}

// Bootstrap orchestrates the GoCell application lifecycle.
type Bootstrap struct {
	configPath                      string
	envPrefix                       string
	httpAddr                        string
	assembly                        *assembly.CoreAssembly
	workers                         []worker.Worker
	publisher                       outbox.Publisher
	subscriber                      outbox.Subscriber
	routerOpts                      []router.Option
	authVerifier                    auth.IntentTokenVerifier
	authPublicEndpoints             []string
	authDiscovery                   bool // true when WithPublicEndpoints was called
	passwordResetExemptEndpoints    []string
	passwordResetChangeEndpointHint string
	shutdownTimeout                 time.Duration
	preShutdownDelay                time.Duration
	listener                        net.Listener
	healthCheckers                  []namedChecker
	adapterInfo                     map[string]string // static adapter metadata for /readyz verbose
	verboseToken                    string            // token for /readyz?verbose access control
	closers                         []io.Closer       // middleware dependencies that need shutdown
	disableObservabilityRestore     bool
	eventRouterReadyTimeout         time.Duration
	eventRouterReadyTimeoutSet      bool
	consumerMiddleware              []outbox.SubscriptionMiddleware
	hookTimeout                     time.Duration // applied when assembly not pre-built
	hookTimeoutSet                  bool          // distinguishes zero-value "unset" from explicit zero
	hookObserver                    cell.LifecycleHookObserver
	metricsProvider                 kernelmetrics.Provider
	shutdownMet                     *shutdownMetrics // nil only when provider is nil
	shutdownMetricsErr              error            // non-nil when metric registration failed in New
	runOnce                         sync.Once

	// configWatcherFactory creates a config watcher. Defaults to
	// config.NewWatcher. Override per-instance in tests to inject failures
	// without mutating package-level state (safe for parallel tests).
	configWatcherFactory func(string, ...config.WatcherOption) (*config.Watcher, error)

	// circuitBreakerNil is set by WithCircuitBreaker when a nil Allower is
	// passed. Checked at Run() to fail-fast instead of silently skipping CB.
	circuitBreakerNil bool

	// brokerHealthNil is set by WithBrokerHealth when a nil (or typed-nil)
	// BrokerHealthChecker is passed. Checked at Run() to fail-fast instead
	// of registering a closure that would nil-deref on the first /readyz
	// probe. Mirrors the circuitBreakerNil contract.
	brokerHealthNil bool

	// internalGuardPrefix and internalGuard hold the configuration set by
	// WithInternalEndpointGuard. Both are forwarded to the router at Run()
	// time via router.WithInternalPathPrefixGuard after prefix validation.
	internalGuardPrefix string
	internalGuard       func(http.Handler) http.Handler

	// relayHealthNil is set by WithRelayHealth when a nil relay is passed.
	// Checked at Run() to fail-fast rather than silently skipping relay health.
	relayHealthNil bool

	// lifecycle fields wired by WithLifecycle* options.
	lifecycle                    Lifecycle
	lifecycleDefaultStartTimeout time.Duration
	lifecycleDefaultStopTimeout  time.Duration
	lifecycleRegistrars          []func(Lifecycle) // accumulated by WithLifecycle

	// managedResources holds resources registered via WithManagedResource.
	// Each resource is expanded into health checkers, workers, and LIFO teardowns
	// by expandManagedResources() at the beginning of Run().
	managedResources []ManagedResource

	// managedResourceTeardowns holds LIFO close functions derived from
	// managedResources during expandManagedResources(). Iterated in reverse
	// order during shutdown so the last-registered resource is closed first.
	managedResourceTeardowns []func()
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
		httpAddr:             ":8080",
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
		s.addTeardown(func(_ context.Context) error {
			td()
			return nil
		})
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
