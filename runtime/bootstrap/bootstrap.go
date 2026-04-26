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
	"crypto/tls"
	"fmt"
	"log/slog"
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
	"github.com/ghbvf/gocell/runtime/config"
	"github.com/ghbvf/gocell/runtime/http/middleware"
	"github.com/ghbvf/gocell/runtime/http/router"
	metricsmiddleware "github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	"github.com/ghbvf/gocell/runtime/shutdown"
	"github.com/ghbvf/gocell/runtime/worker"
)

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

// WithAssembly sets a pre-built CoreAssembly.
func WithAssembly(asm *assembly.CoreAssembly) Option {
	return func(b *Bootstrap) {
		b.assembly = asm
	}
}

// WithAssemblyID sets only the `cell_id` label used by the auto-wired HTTP
// metrics collector (R2). It does not change assembly identity, cell metadata,
// routing, health, tracing, or non-HTTP metrics.
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
		b.routerOpts = append(b.routerOpts,
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

// JWT authentication is wired through a typed []cell.ListenerAuth chain mounted
// on a listener via WithListener:
//
//	bootstrap.WithListener(cell.PrimaryListener, addr, []cell.ListenerAuth{cell.NewAuthJWT(verifier)})
//	bootstrap.WithListener(cell.PrimaryListener, addr, []cell.ListenerAuth{cell.NewAuthJWTFromAssembly(asm)})
//
// The previous bootstrap.WithAuthMiddleware / WithAuthDiscovery options have
// been removed (PR-A14b round-3 F3): one mechanism, one source of truth. The
// listener's auth chain carries the verifier; Bootstrap installs the router-aware
// AuthMiddleware at phase5 so Public / PasswordResetExempt route bypass works
// for free.

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

// validateHTTPListenerConfigs fail-fasts when no listeners are declared via
// WithListener, or when a listener config has neither addr nor pre-bound net.
//
// PR-A14b: replaces validateHTTPListenerAddrs. Listener configuration is now
// declarative via WithListener; the old primaryAddr/internalAddr fields are gone.
// CORR-02: also rejects duplicate listener refs recorded by WithListener.
func (b *Bootstrap) validateHTTPListenerConfigs() error {
	if len(b.listenerConfigs) == 0 {
		return fmt.Errorf("bootstrap: no HTTP listeners declared; use WithListener to declare at least one listener")
	}
	if err := b.validateNoDuplicateListenerRefs(); err != nil {
		return err
	}
	for ref, cfg := range b.listenerConfigs {
		if err := validateListenerConfig(ref, cfg); err != nil {
			return err
		}
	}
	// B2: when a metrics handler is configured, a dedicated HealthListener must
	// be declared so /metrics is isolated from the public primary listener.
	if b.resolveHealthRouteGroupCfg().metricsHandler != nil {
		if _, ok := b.listenerConfigs[cell.HealthListener]; !ok {
			return fmt.Errorf(
				"bootstrap: WithHealthRoutes(WithMetricsHandler(...)) requires a dedicated HealthListener; " +
					"add WithListener(cell.HealthListener, ...) to isolate /metrics from the primary listener")
		}
	}
	return nil
}

// validateListenerConfig validates a single listener config: address presence,
// shutdownGrace sign, and TLS handshake-ability. Extracted from
// validateHTTPListenerConfigs to keep that function's cognitive complexity within
// budget: combining the three conditions (addr+net presence, shutGrace sign, TLS
// certificate availability) with per-listener ref context and error formatting
// would push the outer function beyond the limit of 15.
func validateListenerConfig(ref cell.ListenerRef, cfg listenerConfig) error {
	if cfg.net == nil && cfg.addr == "" {
		return fmt.Errorf("bootstrap: listener %q has no address or pre-bound net.Listener; use WithListener addr or WithListenerNet", ref.String())
	}
	if cfg.shutGrace < 0 {
		return fmt.Errorf("bootstrap: listener %q has negative shutdownGrace %v; use a non-negative duration or zero to inherit the global shutdownTimeout", ref.String(), cfg.shutGrace)
	}
	if err := validateListenerTLSConfig(ref, cfg.tls); err != nil {
		return err
	}
	return nil
}

// validateListenerTLSConfig fail-fasts when the supplied tls.Config cannot
// possibly produce a successful handshake. The check covers two distinct
// failure modes that crypto/tls otherwise only surfaces at handshake time:
//
//  1. No certificate source at all (Certificates / GetCertificate /
//     GetConfigForClient all empty / nil).
//  2. A static Certificates slice present but every entry is a zero-value
//     tls.Certificate — i.e. no certificate chain AND no private key — which
//     trips an opaque "tls: no certificates configured" / "tls: failed to
//     find any PEM data" once the first ClientHello arrives.
//
// nil cfg is a non-TLS listener and returns nil.
func validateListenerTLSConfig(ref cell.ListenerRef, cfg *tls.Config) error {
	if cfg == nil {
		return nil
	}
	if len(cfg.Certificates) == 0 && cfg.GetCertificate == nil && cfg.GetConfigForClient == nil {
		return fmt.Errorf("bootstrap: listener %q TLS config has no Certificates / GetCertificate / GetConfigForClient; the server cannot perform a TLS handshake", ref.String())
	}
	// Static Certificates must each carry at least a chain or a key. Dynamic
	// sources (GetCertificate / GetConfigForClient) are trusted as opaque
	// callbacks and intentionally not introspected here.
	if len(cfg.Certificates) > 0 {
		for i, c := range cfg.Certificates {
			if len(c.Certificate) == 0 && c.PrivateKey == nil && c.Leaf == nil {
				return fmt.Errorf("bootstrap: listener %q TLS Certificates[%d] is a zero-value tls.Certificate (no chain, no private key); load a real key pair via tls.LoadX509KeyPair or set GetCertificate", ref.String(), i)
			}
		}
	}
	return nil
}

// validateNoDuplicateListenerRefs returns an error when the same ListenerRef
// was declared more than once via WithListener (CORR-02).
func (b *Bootstrap) validateNoDuplicateListenerRefs() error {
	if len(b.duplicateListenerRefs) == 0 {
		return nil
	}
	dups := make([]string, 0, len(b.duplicateListenerRefs))
	seen := make(map[string]bool)
	for _, ref := range b.duplicateListenerRefs {
		name := ref.String()
		if !seen[name] {
			dups = append(dups, name)
			seen[name] = true
		}
	}
	sort.Strings(dups)
	return fmt.Errorf("bootstrap: duplicate WithListener call(s) for ref(s): [%s]; each listener ref may only be declared once",
		strings.Join(dups, ", "))
}

// WithAdapterInfo sets static adapter configuration metadata that is exposed
// in /readyz?verbose output. Helps operators verify which storage/bus backends
// are active without inspecting application logs.
func WithAdapterInfo(info map[string]string) Option {
	return func(b *Bootstrap) {
		b.adapterInfo = info
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
		b.healthRouteGroupOpts = append(b.healthRouteGroupOpts, opts...)
	}
}

// PR-A35's bootstrap-level WithVerboseDisabled has been removed (PR-A14b
// round-3 F3 collapse): use WithHealthRoutes(WithReadyzVerboseDisabled()).
// The single-mechanism rule applies — verbose-mode configuration lives on
// the readyz route group exclusively.

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

// WithDisableObservabilityRestore prevents the consumer-side
// ObservabilityContextMiddleware from restoring request_id / correlation_id /
// trace_id from outbox entry metadata into the handler context. The kill
// switch for the consume-side observability bridge — set this only when
// integrating with a custom observability stack that resets context keys
// itself.
func WithDisableObservabilityRestore() Option {
	return func(b *Bootstrap) {
		b.disableObservabilityRestore = true
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
//
// Fields are organised by concern; the dividers below are not load-bearing
// (Go has no notion of struct sub-groups) but reading and reviewing this 45-
// field struct is materially easier when related fields sit together.
type Bootstrap struct {
	// --- assembly + config ---
	configPath string
	envPrefix  string
	assembly   *assembly.CoreAssembly

	// --- workers + outbox pubsub ---
	workers    []worker.Worker
	publisher  outbox.Publisher
	subscriber outbox.Subscriber

	// --- router options (per-listener routerOpts append target) ---
	routerOpts []router.Option

	// --- shutdown + draining budgets ---
	shutdownTimeout  time.Duration
	preShutdownDelay time.Duration

	// --- health probes + adapter metadata ---
	healthCheckers []namedChecker
	adapterInfo    map[string]string // static adapter metadata for /readyz verbose

	// --- closers + observability lifecycle ---
	closers                     []any // middleware/adapter dependencies that need shutdown (ContextCloser preferred, io.Closer fallback)
	disableObservabilityRestore bool

	// --- consumer / event-router wiring ---
	eventRouterReadyTimeout    time.Duration
	eventRouterReadyTimeoutSet bool
	consumerMiddleware         []outbox.SubscriptionMiddleware

	// --- lifecycle hooks (assembly-level start/stop callbacks) ---
	hookTimeout    time.Duration // applied when assembly not pre-built
	hookTimeoutSet bool          // distinguishes zero-value "unset" from explicit zero
	hookObserver   cell.LifecycleHookObserver

	// --- metrics provider + auto-wired HTTP collector ---
	metricsProvider    kernelmetrics.Provider
	httpCollector      metricsmiddleware.Collector // cached auto-wired HTTP collector (created once, shared across listeners)
	shutdownMet        *shutdownMetrics            // nil only when provider is nil
	shutdownMetricsErr error                       // non-nil when metric registration failed in New

	// --- run state ---
	runOnce sync.Once

	// configWatcherFactory creates a config watcher. Defaults to
	// config.NewWatcher. Override per-instance in tests to inject failures
	// without mutating package-level state (safe for parallel tests).
	configWatcherFactory func(string, ...config.WatcherOption) (*config.Watcher, error)

	// --- option-validation flags (fail-fast in phase0) ---

	// circuitBreakerNil is set by WithCircuitBreaker when a nil Allower is
	// passed. Checked at Run() to fail-fast instead of silently skipping CB.
	circuitBreakerNil bool

	// readyzDeadline overrides the per-probe deadline for /readyz.
	// Zero means use health.Handler default (5 s).
	readyzDeadline time.Duration

	// assemblyID is only the `cell_id` label used in HTTP metrics emitted by
	// the auto-wired metrics collector. Defaults to "default" when empty.
	assemblyID string

	// --- kernel/cell Lifecycle (uber/fx-style start/stop) ---

	// lifecycle fields wired by WithLifecycle* options.
	lifecycle                    Lifecycle
	lifecycleDefaultStartTimeout time.Duration
	lifecycleDefaultStopTimeout  time.Duration
	lifecycleRegistrars          []func(Lifecycle) // accumulated by WithLifecycle

	// --- managed resources (LIFO teardown) ---

	// managedResources holds resources registered via WithManagedResource.
	// Each resource is expanded into health checkers, workers, and LIFO teardowns
	// by expandManagedResources() at the beginning of Run().
	managedResources []kernellifecycle.ManagedResource

	// managedResourceTeardowns holds named LIFO close functions derived from
	// managedResources during expandManagedResources(). Iterated in reverse
	// order during shutdown so the last-registered resource is closed first.
	// Each teardown returns the Close error so phase10LIFOTeardown can wrap it
	// with the resource type and aggregate it into the Run() return value.
	managedResourceTeardowns []namedTeardown

	// managedResourceNil is set by WithManagedResource when a nil resource is
	// passed. Checked in phase0 to fail-fast rather than silently skipping
	// resource registration.
	managedResourceNil bool

	// --- declarative listeners + route groups (PR-A14b) ---

	// listenerConfigs holds the PR-A14b declarative listener registrations.
	// Keyed by ListenerRef to deduplicate declarations.
	// Initialized lazily by the first WithListener option.
	listenerConfigs map[cell.ListenerRef]listenerConfig

	// duplicateListenerRefs records refs that were passed to WithListener more
	// than once. validateHTTPListenerConfigs surfaces these as a phase0 error.
	// CORR-02: doc says "duplicate ref is a phase0 error" — now enforced.
	duplicateListenerRefs []cell.ListenerRef

	// healthRouteGroupOpts accumulates HealthRouteGroupOption values from
	// WithHealthRoutes calls. phase5 passes them straight to HealthRouteGroups.
	healthRouteGroupOpts []HealthRouteGroupOption

	// --- tracing + error redaction ---

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
// Health-listener fallback: when no HealthListener is declared, /healthz,
// /readyz, and /metrics are mounted on the PrimaryListener instead. This is
// the expected behaviour for tests that inject only primary + internal
// listeners. Production deployments should declare a dedicated HealthListener
// (typically "127.0.0.1:9091") to physically separate health traffic from
// business traffic.
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
	if err := b.expandManagedResources(); err != nil {
		return err
	}

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
		s.addNamedTeardown(td.name, td.fn) // td already returns error; phase10 aggregates via LIFO teardown chain
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
	if err := b.phase5BuildRouters(s); err != nil {
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
