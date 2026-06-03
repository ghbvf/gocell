package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/ghbvf/gocell/kernel/auth"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/composition"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

func runtimeBaseOptions(
	shared *composition.SharedDeps,
	locals *cmdLocals,
	asm *assembly.CoreAssembly,
	consumerBase *outbox.ConsumerBase,
	metricsHandler http.Handler,
	adapterInfo map[string]string,
) []bootstrap.Option {
	healthRouteOpts := []bootstrap.HealthRouteGroupOption{
		bootstrap.WithMetricsHandler(metricsHandler),
	}
	if shared.VerboseToken != "" {
		// PR269 round-3: verbose-mode gating is a disclosure concern owned by
		// the health handler, not an authentication scheme. WithReadyzVerboseToken
		// plumbs the token to health.Handler.SetVerboseToken, which on mismatch
		// produces the canonical 401 ErrReadyzVerboseDenied envelope.
		healthRouteOpts = append(healthRouteOpts,
			bootstrap.WithReadyzVerboseToken(shared.VerboseToken),
		)
	}
	if shared.VerboseDisabled {
		healthRouteOpts = append(healthRouteOpts, bootstrap.WithReadyzVerboseDisabled())
	}

	opts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(shared.EventBus),
		bootstrap.WithSubscriber(shared.EventBus),
		// ConsumerBase is field-injected into SubscriberWithMiddleware (not a
		// middleware list entry). It is the explicit EntryHandler→SubscriberHandler
		// conversion boundary: idempotency Claim/Commit/Release + retry live here,
		// after the business middleware chain.
		bootstrap.WithConsumerBase(consumerBase),
		bootstrap.WithConsumerMiddleware(consumerMiddlewares(shared)...),
		bootstrap.WithSubscriptionValidator(obmetrics.ConfigEventOwnerValidator),
		bootstrap.WithAdapterInfo(adapterInfo),
		bootstrap.WithHealthRoutes(healthRouteOpts...),
		bootstrap.WithMetricsProvider(locals.metricProvider),
	}
	// Register the assembly's shared infrastructure as the FIRST ManagedResources
	// so bootstrap's LIFO teardown closes them LAST — after every consumer
	// registered later via cell opts (relay, EventRouter goroutines, ConsumerBase
	// workers, cell tx). Provisioned in provisionCapabilities (cap_wiring.go).
	if locals.poolMR != nil {
		opts = append(opts, bootstrap.WithManagedResource(locals.poolMR))
	}
	if shared.Redis != nil {
		if mr, ok := shared.Redis.Client().(kernellifecycle.ManagedResource); ok {
			opts = append(opts, bootstrap.WithManagedResource(mr))
		}
	}
	return opts
}

func consumerMiddlewares(shared *composition.SharedDeps) []outbox.SubscriptionMiddleware {
	return []outbox.SubscriptionMiddleware{
		configEventConsumerMiddleware(shared.ConfigEventCollector),
	}
}

func configEventConsumerMiddleware(collector obmetrics.ConfigEventCollector) outbox.SubscriptionMiddleware {
	return obmetrics.ConfigEventMiddleware(collector)
}

// newBootstrapFromOptions creates a bootstrap.Bootstrap from a pre-built option
// slice. Tests that need a bootstrap instance must route through this wrapper
// (it is a production file, not a test file). run.go also calls bootstrap.New
// directly; startup intentionally lives in run.go for grep-locality.
// clk is the single composition-root clock; the same instance must be passed
// to assembly.New(clk, ...) to satisfy bootstrap's clock-alignment check.
func newBootstrapFromOptions(clk clock.Clock, opts []bootstrap.Option) *bootstrap.Bootstrap {
	return bootstrap.New(clk, opts...)
}

// defaultRuntimeOptions constructs the ordered bootstrap.Option slice from the
// shared cross-cutting deps, a pre-built assembly, a ConsumerBase, a metrics
// handler, and the adapter info map. Invoked from the RuntimeOptionsFunc passed
// to composition.Builder.Build.
//
// PoolResource options are contributed per-Cell by CellModule.Provide (and
// threaded into bootstrap by the Builder). This function covers only the
// cross-cutting concerns:
// HTTP addr, publisher/subscriber, public/exempt endpoints, metrics, etc.
func defaultRuntimeOptions(
	shared *composition.SharedDeps,
	locals *cmdLocals,
	asm *assembly.CoreAssembly,
	consumerBase *outbox.ConsumerBase,
	metricsHandler http.Handler,
	adapterInfo map[string]string,
) ([]bootstrap.Option, error) {
	// PR-A14b: three-listener topology — primary (business routes + JWT auth),
	// internal (/internal/v1/* + service-token auth), health (/healthz /readyz
	// /metrics on a dedicated port).
	//
	// Primary listener: AuthJWTFromAssembly discovers IntentTokenVerifier from
	// accesscore post-Init (lazy phase4 resolution, fail-closed).
	// Internal listener: AuthServiceToken from InternalGuard. The listener is
	// always registered in the runtime path; SharedDeps.Validate requires both
	// InternalHTTPAddr and InternalGuard before runCorebundle reaches this point.
	// Health listener: framework-owned /healthz, /readyz, /metrics route groups;
	// when shared.VerboseToken is set, the health handler's strict-gate path
	// (WithReadyzVerboseToken → SetVerboseToken) requires a matching X-Readyz-Token
	// for ?verbose=true requests; mismatches return 401 ErrReadyzVerboseDenied.
	//
	// ref: go-kratos/kratos app.go — per-server option pattern.
	opts := runtimeBaseOptions(shared, locals, asm, consumerBase, metricsHandler, adapterInfo)
	projOpts, err := projectionRuntimeOptions(shared)
	if err != nil {
		return nil, fmt.Errorf("projection harness wiring: %w", err)
	}
	opts = append(opts, projOpts...)
	// HTTP idempotency replay store: wired when Redis is present (default-ON,
	// no env toggle). When Redis is absent (memory/single-pod mode) the store
	// is nil and WithIdempotencyStore is intentionally not called — single-pod
	// deployments have no cross-pod replay need and the middleware is safely
	// inactive. Mirrors the buildConsumerClaimer / buildServiceNonceStore
	// topology pattern.
	if locals.redisClient != nil {
		idemStore, err := buildHTTPIdempotencyStore(locals.redisClient)
		if err != nil {
			return nil, fmt.Errorf("http idempotency store wiring: %w", err)
		}
		// Fail-closed: Redis is present, so idempotency is default-on and MUST be
		// wired. A nil store here (factory returned nil without error) would
		// silently leave idempotency inactive in production — refuse to start
		// rather than fail open. The (nil,nil) factory return is reserved for the
		// nil-client (memory/single-pod) path, which this branch already excludes.
		if idemStore == nil {
			return nil, fmt.Errorf("http idempotency store wiring: Redis is configured but the " +
				"store factory returned nil; refusing to start with idempotency silently disabled (fail-closed)")
		}
		opts = append(opts, bootstrap.WithIdempotencyStore(idemStore))
		// Capability-level readiness: a bare PING (redis_ready) cannot detect an
		// ACL that permits PING but denies EVAL/SET, yet default-on idempotency
		// depends on the Claim/Record Lua scripts. Register a probe that runs a
		// real EVAL when the store supports it (the redis store does; the
		// in-memory test store does not).
		if rc, ok := idemStore.(interface {
			ReadyCheck(context.Context) error
		}); ok {
			opts = append(opts, bootstrap.WithHealthChecker(
				adapterredis.ProbeHTTPIdempotencyStoreReady, rc.ReadyCheck))
		}
	}
	if shared.PrimaryHTTPAddr != "" {
		primaryAuth, err := auth.NewAuthJWTFromAssembly(asm)
		if err != nil {
			return nil, fmt.Errorf("primary listener auth: %w", err)
		}
		opts = append(opts, bootstrap.WithListener(
			cell.PrimaryListener, shared.PrimaryHTTPAddr,
			[]auth.ListenerAuth{primaryAuth},
		))
	}
	internalChain, err := buildInternalAuthChain(locals.internalGuard)
	if err != nil {
		return nil, fmt.Errorf("internal listener auth: %w", err)
	}
	// #673: the HealthListener is mandatory (unlike PrimaryListener, which a
	// worker-only binary may omit). resolveListenerAddrs always defaults
	// HealthHTTPAddr to 127.0.0.1:9091, so it is never empty on the env path;
	// declaring it unconditionally removes the dead `!= ""` gate and the
	// "Validate passes but the listener is silently skipped" gap. A directly
	// constructed SharedDeps with an empty addr now fails fast at bootstrap
	// phase0 (validateListenerConfig: no address) instead of being skipped.
	opts = append(opts,
		bootstrap.WithListener(cell.InternalListener, shared.InternalHTTPAddr, internalChain),
		bootstrap.WithListener(cell.HealthListener, shared.HealthHTTPAddr, []auth.ListenerAuth{auth.AuthNone{}}),
		devtoolsOption(shared),
	)
	return opts, nil
}

// buildInternalAuthChain constructs the auth chain for the internal listener.
// guard is always non-nil after SEC-FAIL-CLOSED: internalGuardFromEnv now
// returns an error rather than a nil guard in all adapter modes when
// GOCELL_SERVICE_SECRET is unset, so SharedDeps.Validate fails fast before
// this function is reached with a nil guard.
//
// See docs/ops/listener-topology.md for the deployment topology, threat boundaries,
// and single-listener migration guide that frame this auth-chain composition.
func buildInternalAuthChain(guard *internalGuard) ([]auth.ListenerAuth, error) {
	plan, err := auth.NewAuthServiceToken(guard.NonceStore(), guard.ring)
	if err != nil {
		return nil, fmt.Errorf("build internal auth chain: %w", err)
	}
	return []auth.ListenerAuth{plan}, nil
}

// projectionRuntimeOptions builds the four L3 CQRS projection-harness dependency
// options (#1368) from the shared postgres capability: the PG-backed
// CheckpointStore, the pool-bound TxRunner, and the journal-backed ReplaySource +
// Cursor (the cursor delegates to the replay source — single seq-SQL holder).
//
// PG mode only. In memory mode (shared.PG == nil) it returns no options: the
// projection harness requires a durable checkpoint store, not an in-memory fake,
// so a projection declared without PG fails fast in the bootstrap phase6 drain
// (checkProjectionDeps), which is the correct outcome. corebundle ships no
// projection cell today, so these options are dormant until one is added —
// forward-provisioning per #1368.
// envProjectionPGJournalPreview opts into wiring the PG journal-backed projection
// reader. It defaults OFF (the hard gate): the reader resolves stream positions
// from the TRANSIENT outbox relay (CleanupPublished/CleanupDead delete rows), so
// it is NOT a durable projection journal — an event whose row is cleaned before
// the projection consumes it resolves to a permanent error (dropped on the live
// path, aborts a rebuild). Until a durable append-only projection journal lands
// (#1504), the reader is dev/preview only. When OFF, a projection declared in PG
// mode fails fast in the bootstrap phase6 drain (checkProjectionDeps) — wiring no
// replay source is the gate, not a silent production-unsafe reader.
const envProjectionPGJournalPreview = "GOCELL_PROJECTION_PG_JOURNAL_PREVIEW"

func projectionPGJournalPreviewEnabled() bool {
	v, _ := strconv.ParseBool(os.Getenv(envProjectionPGJournalPreview))
	return v
}

func projectionRuntimeOptions(shared *composition.SharedDeps) ([]bootstrap.Option, error) {
	if shared.PG == nil {
		return nil, nil
	}
	if !projectionPGJournalPreviewEnabled() {
		// Hard gate (C1, review #1509): do NOT silently wire a production-unsafe
		// reader. A projection declared in PG mode without the opt-in fails fast at
		// bootstrap; durable journal tracked in #1504.
		slog.Warn("projection: PG journal-backed reader NOT wired — the outbox relay is transient " +
			"(CleanupPublished/CleanupDead), so it is not a production-safe projection journal/position " +
			"source; a projection declared in PG mode will fail fast at bootstrap. Durable journal tracked " +
			"in gh #1504. Set " + envProjectionPGJournalPreview + "=true to opt in for dev/preview only.")
		return nil, nil
	}
	slog.Warn("projection: PG journal-backed reader wired in PREVIEW mode (" + envProjectionPGJournalPreview +
		"=true) — NOT production-safe: positions come from the transient outbox relay; events cleaned " +
		"before consume are dropped. Dev/preview only; durable journal tracked in gh #1504.")
	pool, err := cellsecrets.PgxPoolFromProvider(shared.PG)
	if err != nil {
		return nil, fmt.Errorf("projection pg pool: %w", err)
	}
	checkpointStore, err := adapterpg.NewProjectionCheckpointStore(pool)
	if err != nil {
		return nil, fmt.Errorf("projection checkpoint store: %w", err)
	}
	replaySource, err := adapterpg.NewProjectionReplaySource(pool)
	if err != nil {
		return nil, fmt.Errorf("projection replay source: %w", err)
	}
	cursor, err := adapterpg.NewProjectionCursor(replaySource)
	if err != nil {
		return nil, fmt.Errorf("projection cursor: %w", err)
	}
	return []bootstrap.Option{
		bootstrap.WithProjectionCheckpointStore(checkpointStore),
		bootstrap.WithProjectionTxRunner(shared.PG.TxManager()),
		bootstrap.WithProjectionReplaySource(replaySource),
		bootstrap.WithProjectionCursor(cursor),
	}, nil
}
