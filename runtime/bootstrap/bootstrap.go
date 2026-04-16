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
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/config"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/eventrouter"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/ghbvf/gocell/runtime/http/middleware"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	"github.com/ghbvf/gocell/runtime/shutdown"
	"github.com/ghbvf/gocell/runtime/worker"
)

// authProvider is discovered post-Init from cells that provide a
// session-aware TokenVerifier (e.g. access-core's TokenVerifier()).
type authProvider interface {
	TokenVerifier() auth.TokenVerifier
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
// ref: go-zero — resilience middleware configuration at app level
func WithCircuitBreaker(cb middleware.CircuitBreakerPolicy) Option {
	return func(b *Bootstrap) {
		b.routerOpts = append(b.routerOpts, router.WithCircuitBreaker(cb))
		if cl, ok := cb.(io.Closer); ok {
			b.closers = append(b.closers, cl)
		}
	}
}

// WithAuthMiddleware enables authentication for HTTP business routes. The
// verifier is applied to the router's middleware chain at Run() time via
// router.WithAuthMiddleware.
//
// Deprecated: Use WithPublicEndpoints instead. WithPublicEndpoints discovers
// the auth verifier automatically from cells implementing authProvider,
// eliminating the need for explicit verifier injection at the composition root.
//
// publicEndpoints specifies business-route paths that bypass authentication.
// If nil, no business routes are public (fail-closed). Callers must
// explicitly list paths like login and token refresh that should be
// accessible without a valid JWT.
//
// Infra endpoints (/healthz, /readyz, /metrics) are registered on the
// router's outer mux and naturally bypass business-route middleware, so they
// do not need to be listed in publicEndpoints.
//
// ref: go-kratos/kratos — auth middleware at service level
// ref: go-zero — per-route WithJwt() opt-in auth
func WithAuthMiddleware(verifier auth.TokenVerifier, publicEndpoints []string) Option {
	return func(b *Bootstrap) {
		b.authVerifier = verifier
		b.authPublicEndpoints = publicEndpoints
	}
}

// WithPublicEndpoints sets paths that bypass authentication when an
// AuthProvider cell is discovered post-Init. Unlike WithAuthMiddleware
// (which provides the verifier explicitly), this option defers verifier
// resolution to Run() time.
// WithPublicEndpoints must not be combined with WithAuthMiddleware —
// use one or the other. If both are called, the last one wins for
// publicEndpoints and a warning is logged at startup.
func WithPublicEndpoints(endpoints []string) Option {
	return func(b *Bootstrap) {
		if b.authVerifier != nil {
			slog.Warn("bootstrap: WithPublicEndpoints called after WithAuthMiddleware; publicEndpoints will be overwritten")
		}
		b.authPublicEndpoints = endpoints
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

// namedChecker pairs a readiness probe name with its check function.
type namedChecker struct {
	name string       // unique identifier shown in /readyz?verbose output
	fn   func() error // nil return = healthy; non-nil = unhealthy
}

// Bootstrap orchestrates the GoCell application lifecycle.
type Bootstrap struct {
	configPath                  string
	envPrefix                   string
	httpAddr                    string
	assembly                    *assembly.CoreAssembly
	workers                     []worker.Worker
	publisher                   outbox.Publisher
	subscriber                  outbox.Subscriber
	routerOpts                  []router.Option
	authVerifier                auth.TokenVerifier
	authPublicEndpoints         []string
	authDiscovery               bool // true when WithPublicEndpoints was called
	shutdownTimeout             time.Duration
	preShutdownDelay            time.Duration
	listener                    net.Listener
	healthCheckers              []namedChecker
	adapterInfo                 map[string]string // static adapter metadata for /readyz verbose
	verboseToken                string            // token for /readyz?verbose access control
	closers                     []io.Closer       // middleware dependencies that need shutdown
	disableObservabilityRestore bool
	hookTimeout                 time.Duration // applied when assembly not pre-built
	hookTimeoutSet              bool          // distinguishes zero-value "unset" from explicit zero
	hookObserver                cell.LifecycleHookObserver
	metricsProvider             kernelmetrics.Provider
	runOnce                     sync.Once

	// configWatcherFactory creates a config watcher. Defaults to
	// config.NewWatcher. Override per-instance in tests to inject failures
	// without mutating package-level state (safe for parallel tests).
	configWatcherFactory func(string, ...config.WatcherOption) (*config.Watcher, error)
}

// New creates a Bootstrap with the given options.
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
	return b
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
// Startup sequence (ref: uber-go/fx app.go Run):
//  1. Load config
//  2. Initialise publisher/subscriber (default: InMemoryEventBus for both)
//  3. Initialise assembly (inject config into Dependencies.Config)
//  4. Cell.Init -> Cell.Start (assembly.Start)
//  5. RegisterRoutes for HTTPRegistrar cells
//  6. RegisterSubscriptions for EventRegistrar cells
//  7. Start HTTP server
//  8. Start workers
//  9. Wait for signal (runtime/shutdown)
//  10. Shutdown: stop workers -> drain HTTP -> stop assembly -> close subscriber/publisher
//
// If any step fails, already-started components are rolled back in reverse.
func (b *Bootstrap) Run(ctx context.Context) error {
	// Guard against double-Run. A second call would create duplicate
	// teardowns and race on shared resources.
	// ref: uber-go/fx App.Run — returns immediately if already started
	started := false
	b.runOnce.Do(func() { started = true })
	if !started {
		return fmt.Errorf("bootstrap: Run called more than once")
	}

	// Step 0: Validate inputs before any side effects.
	// Health checker params are pure data — no reason to defer to runtime.
	for _, hc := range b.healthCheckers {
		if hc.name == "" {
			return fmt.Errorf("bootstrap: health checker name must not be empty")
		}
		if hc.fn == nil {
			return fmt.Errorf("bootstrap: health checker %q must not be nil", hc.name)
		}
	}

	// Track teardown functions for rollback (LIFO order).
	var teardowns []func(context.Context) error

	// hh is declared here (not at Step 5) so the rollback closure can
	// mark readyz unhealthy when rolling back after the HTTP server
	// has started. Before Step 5 executes, hh remains nil and the
	// nil check in rollback is a no-op.
	var hh *health.Handler

	rollback := func(cause error) error {
		slog.Error("bootstrap: startup failed, rolling back", slog.Any("error", cause))
		if hh != nil {
			hh.SetShuttingDown()
		}
		rctx, cancel := context.WithTimeout(context.Background(), b.shutdownTimeout)
		defer cancel()
		for i := len(teardowns) - 1; i >= 0; i-- {
			if err := teardowns[i](rctx); err != nil {
				slog.Warn("bootstrap: rollback step failed", slog.Any("error", err))
			}
		}
		return cause
	}

	// Step 1: Load config.
	var cfg config.Config
	if b.configPath != "" {
		var err error
		cfg, err = config.Load(b.configPath, b.envPrefix)
		if err != nil {
			return fmt.Errorf("bootstrap: load config: %w", err)
		}
	} else {
		cfg = config.NewFromMap(make(map[string]any))
	}

	// Step 1.5a: Create config watcher (if config file provided).
	// The watcher is created here but NOT started until Step 4.5, after the
	// OnChange callback is registered. This prevents a startup window where
	// file events are consumed but no callback is bound to handle them.
	var cfgWatcher *config.Watcher
	if b.configPath != "" {
		w, err := b.configWatcherFactory(b.configPath)
		if err != nil {
			return rollback(fmt.Errorf("bootstrap: config watcher: %w", err))
		} else {
			cfgWatcher = w
			teardowns = append(teardowns, func(_ context.Context) error {
				return cfgWatcher.Close()
			})
		}
	}

	// Step 1.5b: Register closable middleware dependencies for teardown.
	// Middleware like ratelimit.Limiter owns background goroutines; if injected
	// via bootstrap convenience options, bootstrap takes ownership of Close.
	for _, cl := range b.closers {
		teardowns = append(teardowns, func(_ context.Context) error {
			return cl.Close()
		})
	}

	// Step 2: Initialise publisher and subscriber.
	// If neither publisher nor subscriber is set, create a default InMemoryEventBus
	// that satisfies both roles — preserving the original single-bus behaviour.
	pub := b.publisher
	sub := b.subscriber
	if pub == nil && sub == nil {
		eb := eventbus.New()
		pub = eb
		sub = eb
	}
	// Register teardown for subscriber (if it implements io.Closer).
	if cl, ok := sub.(io.Closer); ok {
		teardowns = append(teardowns, func(_ context.Context) error {
			return cl.Close()
		})
	}
	// Register teardown for publisher (if it implements io.Closer and is not
	// the same instance as the subscriber — avoid double-close).
	if cl, ok := pub.(io.Closer); ok && any(pub) != any(sub) {
		teardowns = append(teardowns, func(_ context.Context) error {
			return cl.Close()
		})
	}

	// Step 3-4: Initialise and start assembly.
	asm := b.assembly
	if asm == nil {
		cfg := assembly.Config{ID: "default", DurabilityMode: cell.DurabilityDemo}
		if b.hookTimeoutSet {
			cfg.HookTimeout = b.hookTimeout
		}
		if b.hookObserver != nil {
			cfg.HookObserver = b.hookObserver
		}
		asm = assembly.New(cfg)
	} else if b.hookTimeoutSet || b.hookObserver != nil {
		// Pre-built assembly owns its own hook config — WithHookTimeout /
		// WithHookObserver are silently superseded by assembly.Config. Warn
		// so operators don't spend time debugging why the option had no effect.
		slog.Warn("bootstrap: WithHookTimeout/WithHookObserver ignored because WithAssembly was used; configure via assembly.Config")
	}

	// Inject config into assembly dependencies.
	cfgMap := snapshotConfig(cfg)

	if err := asm.StartWithConfig(ctx, cfgMap); err != nil {
		return rollback(fmt.Errorf("bootstrap: assembly start: %w", err))
	}
	// reloads ensures shutdown follows a strict gate-before-drain order:
	//   1. reject new callbacks,
	//   2. wait for in-flight callbacks to leave,
	//   3. stop the assembly.
	//
	// ref: net/http Server.Shutdown — stop accepting + drain active + close.
	reloads := newReloadGate()

	teardowns = append(teardowns, func(c context.Context) error {
		drained := reloads.BeginShutdown()
		select {
		case <-drained:
		case <-c.Done():
			return c.Err()
		}
		return asm.Stop(c)
	})

	// Step 4.5a: Discover auth verifier from cells (post-Init).
	// When no explicit verifier was provided via WithAuthMiddleware but
	// publicEndpoints are configured via WithPublicEndpoints, discover a
	// cell implementing authProvider and use its TokenVerifier.
	if b.authVerifier == nil && b.authDiscovery {
		var discoveredFrom string
		for _, id := range asm.CellIDs() {
			if ap, ok := asm.Cell(id).(authProvider); ok {
				if v := ap.TokenVerifier(); v != nil {
					if discoveredFrom != "" {
						return rollback(fmt.Errorf(
							"bootstrap: multiple auth provider cells discovered: %q and %q; use WithAuthMiddleware to select explicitly",
							discoveredFrom, id))
					}
					b.authVerifier = v
					discoveredFrom = id
				}
			}
		}
		if b.authVerifier == nil {
			return rollback(fmt.Errorf("bootstrap: WithPublicEndpoints requires an auth provider cell, but none was discovered"))
		}
		slog.Info("bootstrap: auth verifier discovered from cell",
			slog.String("cell", discoveredFrom))
	}

	// Step 4.5b: Register config watcher OnChange callback (now that asm is started).
	// Snapshot → Reload → Diff → notify ConfigReloader cells.
	if cfgWatcher != nil {
		yamlPath, envPrefix := b.configPath, b.envPrefix
		cfgWatcher.OnChange(func(evt config.WatchEvent) {
			if !reloads.TryEnter() {
				slog.Warn("bootstrap: config reload rejected during shutdown",
					slog.String("path", evt.Path))
				return
			}
			defer reloads.Leave()

			rc, ok := cfg.(config.Reloader)
			if !ok {
				return
			}

			oldSnap := snapshotConfig(cfg)

			if err := rc.Reload(yamlPath, envPrefix); err != nil {
				slog.Error("bootstrap: config reload failed", slog.Any("error", err))
				return
			}
			slog.Info("bootstrap: config reloaded", slog.String("path", evt.Path))

			newSnap := snapshotConfig(cfg)
			added, updated, removed := config.Diff(oldSnap, newSnap)
			if len(added) == 0 && len(updated) == 0 && len(removed) == 0 {
				slog.Debug("bootstrap: config reloaded but no effective changes")
				// No-op rewrite: generation incremented by Reload, but all cells
				// are already at the latest state. Sync observedGeneration to
				// prevent false drift (HasDrift would otherwise return true).
				if og, ok := cfg.(config.ObservedGenerationer); ok {
					if g, gOK := cfg.(config.Generationer); gOK {
						og.SetObservedGeneration(g.Generation())
					}
				}
				return
			}

			// Read config generation for tracking drift between config and cells.
			var gen int64
			if g, ok := cfg.(config.Generationer); ok {
				gen = g.Generation()
			}

			allCellsOK := true
			for _, id := range asm.CellIDs() {
				c := asm.Cell(id)
				cr, ok := c.(cell.ConfigReloader)
				if !ok {
					continue
				}
				// Clone per cell to guarantee isolation: a misbehaving handler
				// cannot mutate slices/map seen by subsequent handlers.
				event := cell.ConfigChangeEvent{
					Added:      cloneStrings(added),
					Updated:    cloneStrings(updated),
					Removed:    cloneStrings(removed),
					Config:     cloneMap(newSnap),
					Generation: gen,
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							allCellsOK = false
							slog.Error("bootstrap: config reload callback panic",
								slog.String("cell", id),
								slog.String("type", fmt.Sprintf("%T", r)))
							slog.Debug("bootstrap: config reload callback panic detail",
								slog.String("cell", id), slog.Any("panic", r))
						}
					}()
					if err := cr.OnConfigReload(event); err != nil {
						allCellsOK = false
						slog.Error("bootstrap: config reload callback failed",
							slog.String("cell", id),
							slog.Any("error", err),
							slog.Int64("config_generation", gen))
					}
				}()
			}

			// Mark the generation as observed only when all cells applied it
			// successfully. A gap between Generation and ObservedGeneration
			// indicates config drift — surfaced via config.HasDrift().
			if allCellsOK {
				if og, ok := cfg.(config.ObservedGenerationer); ok {
					og.SetObservedGeneration(gen)
				}
			}
		})
		// Start after OnChange is bound so no events are consumed without a handler.
		cfgWatcher.Start()
	}

	// Step 5: Build router with health handler.
	// Use NewE (error-returning) so that configuration errors (e.g. invalid
	// trusted proxies) enter the rollback path instead of panicking past
	// already-started components (assembly, config watcher, pub/sub).
	//
	// ref: uber-go/fx — startup failures return error, trigger rollback
	hh = health.New(asm)
	if b.adapterInfo != nil {
		hh.SetAdapterInfo(b.adapterInfo)
	}
	if b.verboseToken != "" {
		hh.SetVerboseToken(b.verboseToken)
	}
	// registerHealthChecker wraps hh.RegisterChecker with an error return
	// instead of a panic on duplicate names. Since hh is local to Run() and
	// all registrations go through this closure, the panic path in
	// RegisterChecker is effectively unreachable — the map check here
	// catches duplicates first and returns a rollback-safe error.
	registeredCheckerNames := make(map[string]struct{})
	registerHealthChecker := func(name string, fn func() error) error {
		if _, exists := registeredCheckerNames[name]; exists {
			return fmt.Errorf("bootstrap: duplicate health checker %q", name)
		}
		hh.RegisterChecker(name, health.Checker(fn))
		registeredCheckerNames[name] = struct{}{}
		return nil
	}
	// Name/fn already validated in Step 0 (before any side effects).
	for _, hc := range b.healthCheckers {
		if err := registerHealthChecker(hc.name, hc.fn); err != nil {
			return rollback(err)
		}
	}
	// Auto-discover HealthContributor cells and register their probes.
	// This replaces manual WithHealthChecker calls for cell-owned probes
	// (e.g. session-store), aligning with the authProvider discovery pattern.
	for _, id := range asm.CellIDs() {
		if hcc, ok := asm.Cell(id).(cell.HealthContributor); ok {
			for name, fn := range hcc.HealthCheckers() {
				if fn == nil {
					return rollback(fmt.Errorf("bootstrap: cell %q returned nil health checker for %q", id, name))
				}
				if err := registerHealthChecker(name, fn); err != nil {
					return rollback(err)
				}
			}
		}
	}
	if cfgWatcher != nil {
		if err := registerHealthChecker(configWatcherCheckerName, cfgWatcher.Health); err != nil {
			return rollback(err)
		}
	}
	// Register config-drift checker when the config supports generation tracking.
	// Reuses config.HasDrift to avoid duplicating the generation comparison logic.
	// Returns unhealthy when desired generation (config reloaded) differs from
	// observed generation (all cells applied). Transient drift during reload is
	// absorbed by K8s failureThreshold (default 3 consecutive failures).
	if g, gOK := cfg.(config.Generationer); gOK {
		if og, ogOK := cfg.(config.ObservedGenerationer); ogOK {
			if err := registerHealthChecker(configDriftCheckerName, func() error {
				if config.HasDrift(cfg) {
					return fmt.Errorf("config drift: generation %d, observed %d",
						g.Generation(), og.ObservedGeneration())
				}
				return nil
			}); err != nil {
				return rollback(err)
			}
		}
	}
	// Framework health handler is applied LAST so user-supplied router options
	// cannot accidentally override it. WithHealthHandler sets r.healthHandler;
	// the last call wins, so placing the framework handler after user options
	// guarantees the bootstrap-managed handler is always used.
	// Copy to avoid mutating b.routerOpts' backing array.
	routerOpts := make([]router.Option, 0, len(b.routerOpts)+4)
	routerOpts = append(routerOpts, b.routerOpts...)
	// Wire trust-boundary policy for tracing and request_id from public endpoints.
	// Public endpoints (e.g., login, refresh) ignore client-supplied trace context
	// and X-Request-Id headers, preventing untrusted callers from injecting
	// arbitrary observability identifiers.
	if len(b.authPublicEndpoints) > 0 {
		publicSet := make(map[string]bool, len(b.authPublicEndpoints))
		for _, p := range b.authPublicEndpoints {
			publicSet[path.Clean(p)] = true
		}
		isPublic := func(r *http.Request) bool {
			return publicSet[path.Clean(r.URL.Path)]
		}
		routerOpts = append(routerOpts,
			router.WithTracingOptions(middleware.WithPublicEndpointFn(isPublic)),
			router.WithRequestIDOptions(middleware.WithReqIDPublicEndpointFn(isPublic)),
		)
	}
	if b.authVerifier != nil {
		routerOpts = append(routerOpts, router.WithAuthMiddleware(b.authVerifier, b.authPublicEndpoints))
	}
	routerOpts = append(routerOpts, router.WithHealthHandler(hh))
	rtr, err := router.NewE(routerOpts...)
	if err != nil {
		return rollback(fmt.Errorf("bootstrap: %w", err))
	}

	// Step 5 continued: Register HTTP routes for cells implementing HTTPRegistrar.
	for _, id := range asm.CellIDs() {
		c := asm.Cell(id)
		if hr, ok := c.(cell.HTTPRegistrar); ok {
			hr.RegisterRoutes(rtr)
		}
	}

	// Step 6: Register event subscriptions via EventRouter.
	// Cells declare handlers (non-blocking), then Router.Run starts consumption.
	// Setup errors (e.g., missing DLX) abort startup.
	//
	// Invariant: if any cell declares subscriptions, a subscriber must be injected.
	// Without this check, callers who migrate from WithEventBus to WithPublisher
	// but forget WithSubscriber would silently lose all event consumption.
	var routerErrCh chan error // nil channel: never selected in Step 9; assigned only when event router starts
	if sub == nil {
		// Check whether any cell implements EventRegistrar — if so, the missing
		// subscriber is a configuration error, not a valid "no-events" setup.
		for _, id := range asm.CellIDs() {
			if _, ok := asm.Cell(id).(cell.EventRegistrar); ok {
				return rollback(fmt.Errorf(
					"bootstrap: cell %s implements EventRegistrar but no subscriber is configured; "+
						"add WithSubscriber to bootstrap options", id))
			}
		}
	}
	if sub != nil {
		var mws []outbox.TopicHandlerMiddleware
		if !b.disableObservabilityRestore {
			mws = append(mws, outbox.ObservabilityContextMiddleware())
		}
		evtRouter := eventrouter.New(&outbox.SubscriberWithMiddleware{
			Inner:      sub,
			Middleware: mws,
		})
		for _, id := range asm.CellIDs() {
			c := asm.Cell(id)
			if er, ok := c.(cell.EventRegistrar); ok {
				if err := er.RegisterSubscriptions(evtRouter); err != nil {
					return rollback(fmt.Errorf("bootstrap: cell %s subscription setup failed: %w", id, err))
				}
			}
		}
		if evtRouter.HandlerCount() > 0 {
			if err := registerHealthChecker(eventRouterCheckerName, evtRouter.Health); err != nil {
				return rollback(err)
			}
			slog.Info("bootstrap: starting event router",
				slog.Int("handler_count", evtRouter.HandlerCount()))
			routerErrCh = make(chan error, 1)
			go func() {
				routerErrCh <- evtRouter.Run(ctx)
			}()
			// Wait for all subscriptions to start or a setup error.
			select {
			case err := <-routerErrCh:
				return rollback(fmt.Errorf("bootstrap: event router: %w", err))
			case <-evtRouter.Running():
				// All subscriptions consuming.
			}
			teardowns = append(teardowns, func(c context.Context) error {
				return evtRouter.Close(c)
			})
		}
	}

	// Step 7: Start HTTP server.
	srv := &http.Server{
		Addr:              b.httpAddr,
		Handler:           rtr,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln := b.listener
	if ln == nil {
		var err error
		ln, err = net.Listen("tcp", b.httpAddr)
		if err != nil {
			return rollback(fmt.Errorf("bootstrap: listen %s: %w", b.httpAddr, err))
		}
	}

	httpErrCh := make(chan error, 1)
	go func() {
		slog.Info("bootstrap: HTTP server starting", slog.String("addr", ln.Addr().String()))
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			httpErrCh <- err
		}
		close(httpErrCh)
	}()
	teardowns = append(teardowns, func(c context.Context) error {
		slog.Info("bootstrap: draining HTTP server")
		return srv.Shutdown(c)
	})

	// Step 8: Start workers.
	wg := worker.NewWorkerGroup()
	for _, w := range b.workers {
		wg.Add(w)
	}

	workerCtx, workerCancel := context.WithCancel(ctx)
	var workerErrCh chan error // nil channel: never selected in Step 9 when no workers
	if len(b.workers) > 0 {
		workerErrCh = make(chan error, 1)
		go func() {
			workerErrCh <- wg.Start(workerCtx)
			close(workerErrCh)
		}()
		teardowns = append(teardowns, func(c context.Context) error {
			workerCancel()
			return wg.Stop(c)
		})
	} else {
		workerCancel() // no workers, release the context
	}

	// Step 9: Wait for shutdown signal or error.
	// Monitor all background components: HTTP, workers, and event router.
	slog.Info("bootstrap: application started successfully")
	select {
	case <-ctx.Done():
		slog.Info("bootstrap: context cancelled, shutting down")
	case err := <-httpErrCh:
		if err != nil {
			return rollback(fmt.Errorf("bootstrap: http server: %w", err))
		}
	case err := <-workerErrCh:
		if err != nil {
			slog.Error("bootstrap: worker failed, initiating shutdown", slog.Any("error", err))
			return rollback(fmt.Errorf("bootstrap: worker: %w", err))
		}
	case err := <-routerErrCh:
		if err != nil {
			slog.Error("bootstrap: event router failed, initiating shutdown", slog.Any("error", err))
			return rollback(fmt.Errorf("bootstrap: event router: %w", err))
		}
	}

	// Step 10: Orderly shutdown.
	// Sequence: mark readyz unhealthy → wait for LBs to drain → close connections.
	// ref: Kubernetes pod shutdown model
	slog.Info("bootstrap: initiating graceful shutdown")
	reloads.BeginShutdown()
	hh.SetShuttingDown() // Mark readyz unhealthy so LBs drain traffic

	// Single shutdown budget: preShutdownDelay + teardown share shutdownTimeout.
	// ref: Kubernetes — preStop counts toward terminationGracePeriodSeconds
	shutCtx, shutCancel := context.WithTimeout(context.Background(), b.shutdownTimeout)
	defer shutCancel()
	if b.preShutdownDelay > 0 {
		slog.Info("bootstrap: pre-shutdown drain delay",
			slog.Duration("delay", b.preShutdownDelay))
		select {
		case <-time.After(b.preShutdownDelay):
		case <-shutCtx.Done():
		}
	}

	var errs []error
	for i := len(teardowns) - 1; i >= 0; i-- {
		if err := teardowns[i](shutCtx); err != nil {
			slog.Error("bootstrap: shutdown step failed", slog.Any("error", err))
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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
