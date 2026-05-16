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
	"github.com/ghbvf/gocell/kernel/clock"
	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/metadata"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/config"
	"github.com/ghbvf/gocell/runtime/http/router"
	metricsmiddleware "github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	"github.com/ghbvf/gocell/runtime/shutdown"
	"github.com/ghbvf/gocell/runtime/worker"
)

// Option configures a Bootstrap instance.
type Option func(*Bootstrap)

// readyz probe names; consumed by phases_assembly.go / bootstrap_phases.go.
const (
	configWatcherCheckerName = "config_watcher"
	configDriftCheckerName   = "config_drift"
	eventRouterCheckerName   = "event_router"
)

// Bootstrap orchestrates the GoCell application lifecycle.
//
// Fields are flat: bootstrap is the composition root, and most options
// influence behavior across multiple phases (e.g. WithMetricsProvider feeds
// both default-assembly construction and HTTP metric auto-wiring;
// WithRateLimiter writes both router options and the closer list). Forcing
// a "concern group" sub-struct layout would make those cross-cutting
// consumptions look like boundary violations when in fact they are the
// natural shape of a composition root. fx.App and kratos.App keep their
// state flat for the same reason; controller-runtime only sub-groups state
// when each group is independently start/stop-able as a batch.
//
// File-level decomposition (phases_assembly.go / phases_http.go /
// phases_events.go / phases_workers.go / phases_shutdown.go etc.) is
// orthogonal to struct grouping and intentionally retained: it splits this
// file along phase ordering, not along ownership.
//
// ref: uber-go/fx app.go — App is a flat struct.
// ref: go-kratos/kratos app.go — App is a flat struct.
// ref: sigs.k8s.io/controller-runtime pkg/manager/internal.go — controllerManager
//
//	sub-groups runnables by lifecycle batch (HTTPServers / Webhooks / Caches),
//	not by visual concern.
type Bootstrap struct {
	// --- assembly: config loading + CoreAssembly construction ---
	configPath           string
	envPrefix            string
	assemblyCore         *assembly.CoreAssembly
	assemblyID           string
	configWatcherFactory func(string, clock.Clock, ...config.WatcherOption) (*config.Watcher, error)

	// --- http: listener declarations + router options + health + tracing ---
	listenerConfigs       map[cell.ListenerRef]listenerConfig
	duplicateListenerRefs []cell.ListenerRef
	routerOpts            []router.Option
	healthRouteGroupOpts  []HealthRouteGroupOption
	wrapperTracer         tracing.Tracer
	circuitBreakerNil     bool
	healthCheckers        []namedChecker
	adapterInfo           map[string]string
	readyzDeadline        time.Duration

	// --- events: outbox pubsub + event router + workers ---
	workers                []worker.Worker
	publisher              outbox.Publisher
	subscriber             outbox.Subscriber
	consumerBase           *outbox.ConsumerBase // field-injected into SubscriberWithMiddleware for idempotency
	consumerMiddleware     []outbox.SubscriptionMiddleware
	routerReadyTimeout     time.Duration
	routerReadyTimeoutSet  bool
	subscriptionValidators []cell.SubscriptionValidator

	// --- lifecycle: kernel/cell Lifecycle + ManagedResource + shutdown budgets ---
	lifecycle                Lifecycle
	defaultStartTimeout      time.Duration
	defaultStopTimeout       time.Duration
	lifecycleRegistrars      []func(Lifecycle)
	managedResources         []kernellifecycle.ManagedResource
	managedResourceTeardowns []namedTeardown
	managedResourceNil       bool
	closerNil                bool  // WithManagedCloser(nil) sentinel — phase0 fail-fast
	rateLimiterNil           bool  // WithRateLimiter(nil) sentinel — phase0 fail-fast
	closers                  []any // ContextCloser/io.Closer from any option (e.g. WithRateLimiter); LIFO teardown
	shutdownTimeout          time.Duration
	preShutdownDelay         time.Duration
	terminationGracePeriod   time.Duration // user-declared K8s pod terminationGracePeriodSeconds (advisory only — phase0 sanity check)

	// --- metrics: metrics provider + auto-wired HTTP collector + shutdown metrics ---
	metricsProvider    kernelmetrics.Provider
	httpCollector      metricsmiddleware.Collector
	shutdownMet        *shutdownMetrics
	shutdownMetricsErr error

	// --- devtools catalog endpoint (J1 PR-A37) ---
	// All zero/nil = endpoint not registered.
	devtoolsMeta          *metadata.ProjectMeta      // parsed catalog source
	devtoolsRoot          string                     // displayed in Document.Root
	devtoolsPkgGraph      *kerneldepgraph.Graph      // build-time generated package dep graph (nil = omit packageDeps block)
	devtoolsWireSummaries []metadata.CellWireSummary // optional; nil → wireSummary omitted from all Cell entities

	// --- runtime guard ---
	runOnce sync.Once // Run() single-execution guard

	// --- time source ---
	clock clock.Clock // required: bootstrap.New panics when WithClock is not applied

	// --- owner ctx: long-lived worker context (controller-runtime pattern) ---
	// Derived from runCtx (background-derived assembly runtime ctx) in Run(),
	// before lifecycle.Start. Lifecycle hooks receive this ctx as their OnStart
	// ctx so workers respond to assembly shutdown (ownerCancel) before lifecycle.Stop
	// drains them. ownerCancel is invoked in LIFO teardown BEFORE lifecycle.Stop.
	//
	// ref: kubernetes-sigs/controller-runtime pkg/manager/internal.go —
	//      internalCtx=WithCancel(ctx) passed to Runnable.Start.
	ownerCtx    context.Context
	ownerCancel context.CancelFunc
}

// namedChecker pairs a readiness probe name with its check function.
type namedChecker struct {
	name string                      // unique identifier shown in /readyz?verbose output
	fn   func(context.Context) error // nil return = healthy; non-nil = unhealthy
}

// ---------------------------------------------------------------------------
// Validation helpers (http group fields)
// ---------------------------------------------------------------------------

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
	if ref.IsZero() {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: zero listener ref is invalid; use cell.PrimaryListener, cell.InternalListener, or cell.HealthListener")
	}
	// SEC-FAIL-CLOSED: nil OR empty authChain is rejected at phase0. Empty
	// slices are behaviorally identical to nil — both produce an
	// unauthenticated listener — so requiring `[]cell.ListenerAuth{cell.AuthNone{}}`
	// for genuinely public listeners (HealthListener on a loopback probe path)
	// keeps the explicit no-auth marker visible to grep, archtest SEC-02, and
	// future reviewers.
	if len(cfg.authChain) == 0 {
		return errcode.New(errcode.KindInternal, errcode.ErrListenerAuthChainMissing,
			"bootstrap: listener requires non-empty authChain (use []cell.ListenerAuth{cell.AuthNone{}} for no-auth listeners)",
			errcode.WithInternal(fmt.Sprintf("listener=%q", ref.String())))
	}
	if cfg.net == nil && cfg.addr == "" {
		return fmt.Errorf("bootstrap: listener %q has no address or pre-bound net.Listener;"+
			" use WithListener addr or WithListenerNet", ref.String())
	}
	if cfg.shutGrace < 0 {
		return fmt.Errorf("bootstrap: listener %q has negative shutdownGrace %v;"+
			" use a non-negative duration or zero to inherit the global shutdownTimeout",
			ref.String(), cfg.shutGrace)
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
		return fmt.Errorf("bootstrap: listener %q TLS config has no Certificates / GetCertificate / GetConfigForClient;"+
			" the server cannot perform a TLS handshake", ref.String())
	}
	// Static Certificates must each carry at least a chain or a key. Dynamic
	// sources (GetCertificate / GetConfigForClient) are trusted as opaque
	// callbacks and intentionally not introspected here.
	if len(cfg.Certificates) > 0 {
		for i, c := range cfg.Certificates {
			if len(c.Certificate) == 0 && c.PrivateKey == nil && c.Leaf == nil {
				return fmt.Errorf(
					"bootstrap: listener %q TLS Certificates[%d] is a zero-value tls.Certificate"+
						" (no chain, no private key); load a real key pair via tls.LoadX509KeyPair or set GetCertificate",
					ref.String(), i)
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

// ---------------------------------------------------------------------------
// New / Lifecycle / MetricsProvider / Run
// ---------------------------------------------------------------------------

// New creates a Bootstrap with the given options.
//
// shutdownMetrics are registered against the provider here (plan option B):
// instruments live as long as the Bootstrap, matching the "register at
// start-up" convention used by relay_collector.go and the hook dispatcher.
// On registration failure the error is stored and surfaced by Run() at
// phase0, before any side effects start.
func New(opts ...Option) *Bootstrap {
	b := &Bootstrap{}
	b.shutdownTimeout = shutdown.DefaultTimeout
	b.configWatcherFactory = config.NewWatcher
	b.metricsProvider = kernelmetrics.NopProvider{}

	for _, o := range opts {
		o(b)
	}
	clock.MustHaveClock(b.clock, "bootstrap.New (use bootstrap.WithClock)")
	// Create the Lifecycle after all options are applied so that
	// defaultStartTimeout / defaultStopTimeout are set.
	// Zero values are forwarded as-is; NewLifecycle falls back to the
	// DefaultStartTimeout / DefaultStopTimeout constants internally.
	logger := slog.Default()
	b.lifecycle = NewLifecycle(LifecycleConfig{
		DefaultStartTimeout: b.defaultStartTimeout,
		DefaultStopTimeout:  b.defaultStopTimeout,
		Logger:              logger,
		Clock:               b.clock,
	})
	for _, reg := range b.lifecycleRegistrars {
		reg(b.lifecycle)
	}
	// Register shutdown metrics against the (potentially Nop) provider.
	// newShutdownMetrics returns a disabled metrics object for a nil provider.
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

// Run executes the full startup sequence. It blocks until ctx is canceled
// (or a signal is received), then performs orderly shutdown.
//
// Health-listener fallback: when no HealthListener is declared, /healthz,
// /readyz, and /metrics are mounted on the PrimaryListener instead. This is
// the expected behavior for tests that inject only primary + internal
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
//	phase7: start HTTP server; wire httpErrCh + s.httpDrain (NOT a LIFO teardown)
//	phase8: start worker group on runCtx; wire workerErrCh
//	phase9: block until external ctx cancel, HTTP error, worker error, or router error
//	phase10: explicit shutdown stages — runs in this order:
//	         stage1: readiness flip (/readyz=503 + preShutdownDelay)
//	         stage2: HTTP drain    (s.httpDrain — stop accept + drain in-flight)
//	         stage3: LIFO teardown (workers, event router, assembly, kernel
//	                                lifecycle, closers, managed resources)
//	         stage4: finalize      (cancel runCtx + outcome metric)
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
	if err := b.phase3bDrainLifecycleHooks(s); err != nil {
		return rollback(err)
	}
	// Derive ownerCtx from runCtx (the background-derived assembly runtime ctx).
	// Lifecycle hooks receive ownerCtx as their OnStart ctx: workers can respond
	// to ownerCancel for graceful shutdown before lifecycle.Stop drains them.
	//
	// LIFO teardown registration order (last-registered runs FIRST in LIFO):
	//   1. Register lifecycle.Stop teardown first (runs second in LIFO).
	//   2. Register ownerCancel teardown second (runs first in LIFO).
	// Result: ownerCancel() → lifecycle.Stop() → asm.Stop → ...
	// Workers receive ctx cancellation first, then OnStop drain waits.
	//
	// Registered after the asm.Stop teardown (phase3) so that lifecycle.Stop
	// executes before asm.Stop in the LIFO teardown sequence, letting hooks
	// still access cell resources during shutdown.
	//
	// ref: kubernetes-sigs/controller-runtime pkg/manager/internal.go —
	//      internalCtx=WithCancel(ctx) → Runnable.Start, then cancel before Stop.
	// ref: uber-go/fx internal/lifecycle/lifecycle.go — numStarted LIFO rollback.
	b.ownerCtx, b.ownerCancel = context.WithCancel(runCtx) //nolint:gosec // G118: called on Start failure and LIFO teardown (C.2)

	if err := b.lifecycle.Start(b.ownerCtx); err != nil {
		b.ownerCancel() // release before returning
		return rollback(fmt.Errorf("bootstrap: lifecycle start: %w", err))
	}
	// lifecycle.Stop teardown registered first → runs second in LIFO.
	s.addTeardown(func(stopCtx context.Context) error {
		return b.lifecycle.Stop(stopCtx)
	})
	// ownerCancel teardown registered second → runs first in LIFO (before lifecycle.Stop).
	// This signals workers to exit via ctx cancellation before OnStop drains them.
	s.addTeardown(func(_ context.Context) error {
		b.ownerCancel()
		return nil
	})
	if err := b.phase4WireAuthAndWatcher(s); err != nil {
		return rollback(err)
	}
	if err := b.phase5BuildRouters(ctx, s); err != nil {
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

// ---------------------------------------------------------------------------
// Utility helpers
// ---------------------------------------------------------------------------

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

// filterMapByPrefixes returns a new map containing only entries whose key
// has one of the given prefixes.
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
