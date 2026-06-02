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
	"errors"
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
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/metadata"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/projection"
	kwh "github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/config"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/ghbvf/gocell/runtime/observability/correlate"
	metricsmiddleware "github.com/ghbvf/gocell/runtime/observability/metrics"
	runtimeoutbox "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/shutdown"
	"github.com/ghbvf/gocell/runtime/worker"
)

// Option configures a Bootstrap instance.
type Option func(*Bootstrap)

// readyz probe names; consumed by phases_assembly.go / bootstrap_phases.go.
var (
	configWatcherCheckerName = healthz.ConfigWatcherProbeName
	configDriftCheckerName   = healthz.ConfigDriftProbeName
	eventRouterCheckerName   = healthz.EventRouterProbeName
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

	// --- grpc: listener declarations (server lifecycle owned by adapters/grpc,
	// injected via the GRPCServer interface; see grpc_listener.go) ---
	grpcListenerConfigs []grpcListenerConfig
	grpcServerNil       bool // WithGRPCListener(nil) sentinel — phase0 fail-fast

	// --- http: listener declarations + router options + health + tracing ---
	listenerConfigs       map[cell.ListenerRef]listenerConfig
	duplicateListenerRefs []cell.ListenerRef
	routerOpts            []router.Option
	healthRouteGroupOpts  []HealthRouteGroupOption
	wrapperTracer         wrapper.Tracer
	circuitBreakerNil     bool
	healthCheckers        []namedChecker
	adapterInfo           map[string]string
	readyzDeadline        time.Duration

	// healthAggregator is the shared healthz.Aggregator that bootstrap owns.
	// It is passed to the health.Handler (read side) and is available for
	// drainProbes (write side). When WithHealthAggregator is not called,
	// phase0 constructs a default obshealthz.NewAggregator.
	// healthAggregatorNil is set when WithHealthAggregator(nil) or a typed-nil
	// is passed; phase0 rejects it with ERR_VALIDATION_FAILED.
	healthAggregator    healthz.Aggregator
	healthAggregatorNil bool

	// --- events: outbox pubsub + event router + workers ---
	workers                []worker.Worker
	publisher              outbox.Publisher
	subscriber             outbox.Subscriber
	consumerBase           *outbox.ConsumerBase // field-injected into SubscriberWithMiddleware for idempotency
	consumerMiddleware     []outbox.SubscriptionMiddleware
	routerReadyTimeout     time.Duration
	routerReadyTimeoutSet  bool
	subscriptionValidators []cell.SubscriptionValidator
	relay                  *runtimeoutbox.Relay // optional: wired by WithRelay; nil = no relay depth metric

	// --- lifecycle: kernel/cell Lifecycle + ManagedResource + shutdown budgets ---
	lifecycle                Lifecycle
	defaultStartTimeout      time.Duration
	startupTimeout           time.Duration // whole-Start orchestration backstop; 0→DefaultStartupTimeout, <0→disabled (caller-ctx only)
	defaultStopTimeout       time.Duration
	lifecycleRegistrars      []func(Lifecycle)
	managedResources         []kernellifecycle.ManagedResource
	managedResourceTeardowns []namedTeardown
	managedResourceNil       bool
	closerNil                bool  // WithManagedCloser(nil) sentinel — phase0 fail-fast
	rateLimiterNil           bool  // WithRateLimiter(nil) sentinel — phase0 fail-fast
	idempotencyStoreNil      bool  // WithIdempotencyStore(nil) sentinel — phase0 fail-fast
	closers                  []any // ContextCloser/io.Closer from any option (e.g. WithRateLimiter); LIFO teardown
	shutdownTimeout          time.Duration
	preShutdownDelay         time.Duration
	terminationGracePeriod   time.Duration // user-declared K8s pod terminationGracePeriodSeconds (advisory only — phase0 sanity check)

	// --- metrics: metrics provider + auto-wired HTTP collector + shutdown metrics ---
	metricsProvider    kernelmetrics.Provider
	httpCollector      metricsmiddleware.Collector
	shutdownMet        *metricsmiddleware.ShutdownCollector
	shutdownMetricsErr error

	// outboxRejectCollector is cached after first construction so multiple
	// ConsumerBase wirings share the same collector and avoid double-registering
	// outbox_consumer_rejected_total. PendingDepth is per-cell and wired
	// directly by each composition-root module via relay.WithPendingDepthObserver.
	outboxRejectCollector *metricsmiddleware.OutboxRejectCollector

	// eventRouterCollector is cached so multiple Router instances (if a future
	// multi-listener model arrives) share the same collector.
	eventRouterCollector *metricsmiddleware.EventRouterCollector

	// --- webhook: inbound-webhook receiver dependencies ---
	// Both are injected via WithWebhookSourceStore / WithWebhookClaimer.
	// nil means "not configured"; phase5 fail-fasts when any cell registers a
	// webhook receiver but these fields remain nil.
	webhookSourceStore kwh.SourceStore
	webhookClaimer     idempotency.Claimer

	// --- projection: L3 CQRS projection harness dependencies ---
	// Injected via WithProjectionCheckpointStore / WithProjectionTxRunner /
	// WithProjectionReplaySource / WithProjectionCursor. nil means "not
	// configured"; phase6 fail-fasts when any cell registers a projection but a
	// required dep remains nil. These are framework-owned raw infrastructure —
	// they live here, never in cell code, which is the whole point of the
	// reg.RegisterProjection record-only seam.
	projectionStore    projection.CheckpointStore
	projectionTxRunner persistence.TxRunner
	projectionReplay   projection.ReplaySource
	projectionCursor   projection.Cursor
	// projectionCoordinators maps "<cellID>/<projectionID>" → the constructed
	// Coordinator, so the PR-04 HTTP rebuild endpoint can resolve and trigger
	// Coordinator.Rebuild. Populated by phase6 projection drain.
	projectionCoordinators map[string]*projection.Coordinator

	// --- devtools catalog endpoint (J1 PR-A37) ---
	// All zero/nil = endpoint not registered.
	devtoolsMeta          *metadata.ProjectMeta      // parsed catalog source
	devtoolsRoot          string                     // displayed in Document.Root
	devtoolsPkgGraph      *kerneldepgraph.Graph      // build-time generated package dep graph (nil = omit packageDeps block)
	devtoolsWireSummaries []metadata.CellWireSummary // optional; nil → wireSummary omitted from all Cell entities

	// --- correlate reverse-lookup endpoint (#1048) ---
	// nil = endpoint not registered (Batch 3 wires the concrete Service).
	correlateSvc *correlate.Service

	// --- runtime guard ---
	runOnce sync.Once // Run() single-execution guard

	// --- time source ---
	clock clock.Clock // required: first positional param of bootstrap.New(clk, opts...); MustHaveClock guards typed-nil

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
	name healthz.ProbeName           // unique identifier shown in /readyz?verbose output
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
	// #673: framework health routes (/healthz, /readyz, /metrics) belong solely
	// on cell.HealthListener. A missing declaration fails fast — the pre-#673
	// silent remap onto the public PrimaryListener (which collapsed port-level
	// isolation between business traffic and infra probes) is gone, and there is
	// no opt-in escape hatch. This subsumes the former metrics-specific B2 check:
	// metrics-without-health is just one case of health-without-HealthListener.
	if _, ok := b.listenerConfigs[cell.HealthListener]; !ok {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"bootstrap: framework health routes (/healthz, /readyz, /metrics) require a dedicated "+
				"cell.HealthListener; add WithListener(cell.HealthListener, ...) "+
				"(use []auth.ListenerAuth{auth.AuthNone{}} for the loopback probe path)")
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
	// unauthenticated listener — so requiring `[]auth.ListenerAuth{auth.AuthNone{}}`
	// for genuinely public listeners (HealthListener on a loopback probe path)
	// keeps the explicit no-auth marker visible to grep, archtest SEC-02, and
	// future reviewers.
	if len(cfg.authChain) == 0 {
		return errcode.New(errcode.KindInternal, errcode.ErrListenerAuthChainMissing,
			"bootstrap: listener requires non-empty authChain (use []auth.ListenerAuth{auth.AuthNone{}} for no-auth listeners)",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("listener=%q", ref.String()))))
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
					ref.String(), i,
				)
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
// ShutdownCollector metrics are registered against the provider here (plan
// option B): instruments live as long as the Bootstrap, matching the
// "register at start-up" convention used by relay_collector.go and the hook
// dispatcher. On registration failure the error is stored and surfaced by
// Run() at phase0, before any side effects start.
func New(clk clock.Clock, opts ...Option) *Bootstrap {
	b := &Bootstrap{}
	b.shutdownTimeout = shutdown.DefaultTimeout
	b.configWatcherFactory = config.NewWatcher
	b.metricsProvider = kernelmetrics.NopProvider{}
	b.clock = clk

	for _, o := range opts {
		o(b)
	}
	clock.MustHaveClock(clk, "bootstrap.New")

	// Create the Lifecycle after all options are applied so that
	// defaultStartTimeout / defaultStopTimeout are set.
	// Zero values are forwarded as-is; NewLifecycle falls back to the
	// DefaultStartTimeout / DefaultStopTimeout constants internally.
	// slog.Default is already the sink-side redacting handler: production
	// entrypoints (runCorebundle / example runXxx) seal it before bootstrap.New
	// runs (see logging.NewHandler + SLOG-HANDLER-SEALED-FUNNEL-01).
	logger := slog.Default()
	b.lifecycle = NewLifecycle(b.clock, LifecycleConfig{
		DefaultStartTimeout: b.defaultStartTimeout,
		DefaultStopTimeout:  b.defaultStopTimeout,
		Logger:              logger,
	})
	for _, reg := range b.lifecycleRegistrars {
		reg(b.lifecycle)
	}
	// Register shutdown metrics against the (potentially Nop) provider.
	// NewShutdownCollector returns a disabled metrics object for a nil provider.
	m, err := metricsmiddleware.NewShutdownCollector(b.metricsProvider)
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
// Health listener required (#673): framework health routes (/healthz, /readyz,
// /metrics) are mounted only on a dedicated cell.HealthListener. When none is
// declared, phase0 fails fast — there is no silent fallback onto the public
// PrimaryListener. Every deployment (production and tests alike) must declare
// WithListener(cell.HealthListener, ...); tests use an ephemeral
// "127.0.0.1:0" bind. This physically separates health traffic from business
// traffic.
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
//	phase7b: start gRPC servers in parallel; wire grpcErrCh + s.grpcDrain (NOT a LIFO teardown)
//	phase8: start worker group on runCtx; wire workerErrCh
//	phase9: block until external ctx cancel, HTTP/gRPC error, worker error, or router error
//	phase10: explicit shutdown stages — runs in this order:
//	         stage1: readiness flip (/readyz=503 + preShutdownDelay)
//	         stage2: HTTP + gRPC drain (s.httpDrain / s.grpcDrain — stop accept + drain in-flight)
//	         stage3: LIFO teardown (workers, event router, assembly, kernel
//	                                lifecycle, closers, managed resources)
//	         stage4: finalize      (cancel runCtx + outcome metric)
//
// runCtx is derived from context.Background(), NOT from the caller ctx.
// External ctx cancellation only triggers phase9 to return; workers and the
// event router continue until their phase10 teardown functions run.
//
// Exception (review P1-1): the lifecycle.Start step is additionally supervised
// by the caller ctx + a startup budget (WithStartupTimeout). A hook whose
// OnStart never returns would otherwise wedge Run() forever because ownerCtx
// is background-derived and OnStart carries no per-hook deadline (ADR
// 202605170000 §D-B). superviseLifecycleStart cancels ownerCtx and rolls back
// on caller-cancel / budget-exceeded so Run() always makes progress.
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
	b.ownerCtx, b.ownerCancel = context.WithCancel(runCtx)

	// Supervise lifecycle.Start with the caller ctx + startup budget so a hook
	// whose OnStart never returns cannot wedge Run() (review P1-1). On abort
	// superviseLifecycleStart cancels ownerCtx (unblocking the wedged OnStart)
	// and waits for Start to fully unwind before returning.
	if err := b.superviseLifecycleStart(ctx); err != nil {
		// On the caller-cancel and budget abort paths superviseLifecycleStart
		// already called ownerCancel; this call covers the hook-error path where
		// Start returned a hook error directly. context.CancelFunc is safe to
		// call repeatedly (idempotent by contract).
		b.ownerCancel()
		return rollback(err)
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
	// phase7b: start gRPC servers in parallel to HTTP (serve on runCtx; drained
	// explicitly in phase10 stage2 alongside HTTP, BEFORE LIFO teardown). On a
	// bind failure it drains the already-serving HTTP before returning so
	// rollback does not leak it.
	if err := b.phase7bStartGRPCServers(runCtx, s); err != nil {
		return rollback(err)
	}
	b.phase8StartWorkers(runCtx, s)

	sig := b.phase9AwaitShutdownSignal(ctx, s)
	return b.phase10OrchestrateShutdown(s, sig)
}

// startupUnwindGraceTimeout bounds the post-ownerCancel wait for
// lifecycle.Start to unwind. A ctx-respecting OnStart observes the cancel and
// returns in microseconds; this window only has to cover a respecting-but-slow
// unwind (e.g. an in-flight DB ping draining its own deadline). If it elapses
// the in-flight lifecycle.Start goroutine is abandoned: a ctx-IGNORING OnStart
// cannot be force-killed in Go — that leaked goroutine is an irreducible
// residual (see docs/ops/startup-timeout.md). Abandoning it lets Bootstrap.Run
// make deterministic progress (rollback + return) so the process exits and the
// orchestrator restarts it, instead of Run() wedging forever on a bare channel
// receive (the pre-A1-1-amendment behavior). Fixed internal constant, not an
// operator SLA: it is a safety detach window, not a tuning knob.
const startupUnwindGraceTimeout = 5 * time.Second

// superviseLifecycleStart runs b.lifecycle.Start(b.ownerCtx) under supervision
// so a hook whose OnStart never returns cannot wedge Run() (review P1-1).
//
// lifecycle.Start runs in its own goroutine; this method selects on:
//
//   - start completed → propagate its (possibly nil) error verbatim;
//   - caller ctx canceled → abortStartupAndUnwind (cancel ownerCtx, bounded
//     unwind wait, join caller cause);
//   - startup budget elapsed → same teardown path, cause = ErrBootstrapStartupTimeout.
//
// The owner-ctx single-truth contract (ADR 202605170000 §D-B) is preserved:
// hooks still see exactly one ctx; the bound lives at the orchestration layer,
// mirroring controller-runtime mgr.Start which is unblocked by its caller ctx.
func (b *Bootstrap) superviseLifecycleStart(callerCtx context.Context) error {
	startErr := make(chan error, 1)
	go func() { startErr <- b.lifecycle.Start(b.ownerCtx) }()

	budgetCh, stopBudget := b.startupBudget()
	defer stopBudget()

	select {
	case err := <-startErr:
		if err != nil {
			return fmt.Errorf("bootstrap: lifecycle start: %w", err)
		}
		return nil
	case <-callerCtx.Done():
		return b.abortStartupAndUnwind(
			fmt.Errorf("bootstrap: startup aborted by caller: %w", callerCtx.Err()),
			"caller_canceled", startErr,
		)
	case <-budgetCh:
		return b.abortStartupAndUnwind(ErrBootstrapStartupTimeout,
			"startup_budget_exceeded", startErr)
	}
}

// abortStartupAndUnwind is the shared teardown for both abort paths
// (caller-cancel, budget-elapsed). It cancels ownerCtx to unblock a
// ctx-respecting wedged OnStart, then waits up to startupUnwindGraceTimeout
// for lifecycle.Start to unwind. cause is joined into the returned error
// (context cause on the caller path, ErrBootstrapStartupTimeout on the budget
// path); reason feeds the structured abort log.
//
// Clean path (hook respected ctx): returns errors.Join(cause, unwind) —
// identical to the pre-amendment behavior, zero observable change.
//
// Detach path (hook ignored ctx, grace elapsed): the lifecycle.Start
// goroutine is abandoned (unkillable in Go) and the method returns so Run()
// can roll back and exit; the returned error carries cause + an explicit
// "abandoned" wrap so operators see WHY the process is exiting.
func (b *Bootstrap) abortStartupAndUnwind(cause error, reason string, startErr <-chan error) error {
	b.ownerCancel()
	slog.Default().Error("bootstrap: lifecycle startup aborted",
		slog.String("reason", reason),
		slog.Duration("budget", b.startupBudgetDuration()),
		slog.String("hint", "the last hook.start Info log line identifies the in-flight hook"))

	timer := b.clock.NewTimerAt(b.clock.Now().Add(startupUnwindGraceTimeout))
	defer timer.Stop()
	select {
	case unwind := <-startErr: // Start observed ownerCtx cancel, unwound + rolled back
		return errors.Join(cause, unwind)
	case <-timer.C():
		slog.Default().Error("bootstrap: lifecycle start goroutine abandoned",
			slog.String("reason", "onstart_ignored_ctx"),
			slog.Duration("unwind_grace", startupUnwindGraceTimeout),
			slog.String("hint", "the last hook.start Info log line identifies the ctx-ignoring hook; "+
				"Run() returns so the process exits and the orchestrator restarts it"))
		return errors.Join(cause,
			fmt.Errorf("bootstrap: lifecycle start goroutine abandoned after %s unwind grace (OnStart ignored ctx)", startupUnwindGraceTimeout))
	}
}

// startupBudget returns the startup-budget fire channel and a stop func.
// b.startupTimeout: 0 → DefaultStartupTimeout, <0 → disabled (nil channel
// never fires; caller ctx remains the sole abort path). Uses the injected
// clock so tests drive the budget deterministically with a fake clock.
func (b *Bootstrap) startupBudget() (<-chan time.Time, func()) {
	d := b.startupBudgetDuration()
	if d < 0 {
		// Budget disabled: a nil channel never fires (caller ctx is the sole
		// abort path).
		return nil, func() {
			// Intentional no-op, not an incomplete implementation: no timer
			// was created when the budget is disabled, so there is nothing
			// to stop. The stop func exists only to keep one call shape.
		}
	}
	timer := b.clock.NewTimerAt(b.clock.Now().Add(d))
	return timer.C(), func() { timer.Stop() }
}

// startupBudgetDuration returns the resolved startup budget duration:
// 0 → DefaultStartupTimeout, <0 → disabled.
func (b *Bootstrap) startupBudgetDuration() time.Duration {
	d := b.startupTimeout
	if d == 0 {
		d = DefaultStartupTimeout
	}
	return d
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
