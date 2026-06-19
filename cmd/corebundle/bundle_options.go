package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/ghbvf/gocell/framework/kernel/auth"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/cellmodules/cellsecrets"
	"github.com/ghbvf/gocell/cellmodules/celltls"
	"github.com/ghbvf/gocell/framework/kernel/assembly"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	kernellifecycle "github.com/ghbvf/gocell/framework/kernel/lifecycle"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/composition"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
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
		bootstrap.WithPublisher(shared.Publisher),
		bootstrap.WithSubscriber(shared.Subscriber),
		// Sealed broker-kind fact (from eventtransport.Resolve via cmdLocals): the
		// phase0 split-topology gate requires a real broker, not an in-process bus (#2211).
		bootstrap.WithEventTransportKind(locals.eventTransportKind),
		// ConsumerBase is field-injected into SubscriberWithMiddleware (not a
		// middleware list entry). It is the explicit EntryHandler→SubscriberHandler
		// conversion boundary: idempotency Claim/Commit/Release + retry live here,
		// after the business middleware chain.
		bootstrap.WithConsumerBase(consumerBase),
		bootstrap.WithConsumerMiddleware(consumerMiddlewares()...),
		bootstrap.WithConfigEventCollector(shared.ConfigEventCollector),
		bootstrap.WithSubscriptionValidator(obmetrics.ConfigEventOwnerValidator),
		bootstrap.WithAdapterInfo(adapterInfo),
		bootstrap.WithHealthRoutes(healthRouteOpts...),
		bootstrap.WithMetricsProvider(locals.metricProvider),
	}
	// Register the assembly's shared infrastructure as the FIRST ManagedResources
	// so bootstrap's LIFO teardown closes them LAST — after every consumer
	// registered later via cell opts (EventRouter goroutines, ConsumerBase workers,
	// cell tx). Provisioned in provisionCapabilities (cap_wiring.go). #2341: N pools
	// in split topology, 1 in colocated. Pools register via the keyed WithPoolInstance
	// option (built in provisionPGInstance) so split pools' readiness probes are
	// namespaced per instance and do not collide on the global probe-name namespace.
	opts = append(opts, locals.poolOpts...)
	// Event-transport broker resources (the RabbitMQ connection in postgres mode;
	// empty in demo mode) register among the FIRST ManagedResources (right after the
	// pools), so LIFO teardown closes the broker LATE — after the relays and every
	// consumer that publishes/subscribes through it drain (those register later →
	// close first), and before the PG pools (registered first → close truly last) (#1940).
	for _, mr := range locals.brokerResources {
		opts = append(opts, bootstrap.WithManagedResource(mr))
	}
	// Per-pool outbox relays (#2341), registered AFTER the broker so LIFO teardown
	// stops each relay BEFORE the broker it publishes to closes and BEFORE the pool it
	// drains closes. Each WithRelay is keyed by its pool's InfraInstanceKey (built in
	// provisionPGInstance). Empty in memory mode (no pools → no relays).
	opts = append(opts, locals.relayOpts...)
	if shared.Redis != nil {
		if mr, ok := shared.Redis.Client().(kernellifecycle.ManagedResource); ok {
			opts = append(opts, bootstrap.WithManagedResource(mr))
		}
	}
	return opts
}

func consumerMiddlewares() []outbox.SubscriptionMiddleware {
	return []outbox.SubscriptionMiddleware{
		obmetrics.ConfigEventMiddleware(),
	}
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
	// inactive. Mirrors the replaydeps.Resolve topology-gated pattern.
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
	internalBase, err := buildInternalAuthChain(shared)
	if err != nil {
		return nil, fmt.Errorf("internal listener auth: %w", err)
	}
	// #2263: layer transport-level mTLS over the service-token chain when split
	// TLS material is provisioned (celltls.Resolve set InternalListenerServerTLS).
	// celltls.InternalListenerSecurity is the single chain-shape source shared with
	// examples/ssobff. Nil server TLS → chain unchanged (demo / loopback).
	internalChain, internalTLSOpts := celltls.InternalListenerSecurity(shared.InternalListenerServerTLS, internalBase)
	// #673: the HealthListener is mandatory (unlike PrimaryListener, which a
	// worker-only binary may omit). resolveListenerAddrs always defaults
	// HealthHTTPAddr to 127.0.0.1:9091, so it is never empty on the env path;
	// declaring it unconditionally removes the dead `!= ""` gate and the
	// "Validate passes but the listener is silently skipped" gap. A directly
	// constructed SharedDeps with an empty addr now fails fast at bootstrap
	// phase0 (validateListenerConfig: no address) instead of being skipped.
	opts = append(opts,
		bootstrap.WithListener(cell.InternalListener, shared.InternalHTTPAddr, internalChain, internalTLSOpts...),
		bootstrap.WithListener(cell.HealthListener, shared.HealthHTTPAddr, []auth.ListenerAuth{auth.AuthNone{}}),
		devtoolsOption(shared),
	)
	// Operator control-plane (AdminListener) — declared ONLY when operator
	// credentials are present, so the default deployment stays AdminListener-free
	// and the #1755 audit chain verify endpoint stays dormant (like projection
	// rebuild). The verify endpoint is enabled in the SAME block, so admin-pool
	// presence (which enables #1810 super-admin reads) never forces the admin plane
	// on — the verifier is injected by the auditcore module regardless, but stays
	// unserved here without operator credentials (no #1810 regression).
	operatorAuth, operatorEnabled, opErr := operatorAuthFromEnv(shared.Clock)
	if opErr != nil {
		return nil, fmt.Errorf("operator admin auth: %w", opErr)
	}
	if operatorEnabled {
		opts = append(opts,
			bootstrap.WithListener(cell.AdminListener, adminHTTPAddr(), []auth.ListenerAuth{operatorAuth}),
			bootstrap.WithAuditChainVerifyEndpoint(),
		)
	}
	return opts, nil
}

// buildInternalAuthChain constructs the auth chain for the internal listener
// from the two service-token guard components on SharedDeps: the NonceStore and
// the InternalServiceKeyring. Both are always non-nil after SEC-FAIL-CLOSED:
// buildInternalServiceKeyring returns an error rather than a nil keyring in all
// adapter modes when neither master nor provisioned env is set, and
// SharedDeps.validate rejects a nil InternalServiceKeyring / (in real mode) a
// missing NonceStore before this function is reached.
//
// See docs/ops/listener-topology.md for the deployment topology, threat boundaries,
// and single-listener migration guide that frame this auth-chain composition.
func buildInternalAuthChain(shared *composition.SharedDeps) ([]auth.ListenerAuth, error) {
	plan, err := auth.NewAuthServiceToken(shared.NonceStore, shared.InternalServiceKeyring)
	if err != nil {
		return nil, fmt.Errorf("build internal auth chain: %w", err)
	}
	return []auth.ListenerAuth{plan}, nil
}

// projectionRuntimeOptions builds the L3 CQRS projection-harness dependency
// options from the shared postgres capability: the PG-backed CheckpointStore, the
// pool-bound TxRunner, and the durable append-only projection_events journal source
// (PGProjectionEventSource — EPIC #1504 PR-03) wired as BOTH the ReplaySource and
// the LiveCursor (one instance, so the global_seq encoding is consistent across
// rebuild and live). It also registers the journal's readyz probe.
//
// PG mode only. In memory mode (shared.PG == nil) it returns no options: the
// projection harness requires a durable checkpoint store, not an in-memory fake,
// so a projection declared without PG fails fast in the bootstrap phase6 drain
// (checkProjectionDeps), which is the correct outcome. corebundle ships no
// projection cell today, so these options are dormant until one is added —
// forward-provisioning per #1368.
//
// envProjectionPGJournalPreview gates wiring of the durable projection source. It
// defaults OFF: the production posture stays FAIL-CLOSED (a projection declared in
// PG mode without the opt-in fails fast in the phase6 drain — wiring no source is
// the gate, never a silent unsafe reader). Unlike before #1504, the gated source is
// now the durable, production-safe projection_events journal (append-only, migration
// 058 REVOKE; live carriers resolved by id against the never-cleaned journal), so
// gate-on is no longer "preview/unsafe" — it is the e2e proving ground (T-06-2).
// The gate itself is removed (production-default flip) in #1771 PR-04, gated on
// T-06-2 e2e + PR-05 no-DELETE per ADR 202606071600-1504 §9 / D9.
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
		// Fail-closed gate: do NOT silently wire the projection source. A projection
		// declared in PG mode without the opt-in fails fast at bootstrap (phase6
		// checkProjectionDeps). The gated source IS production-safe (durable journal);
		// the gate only defers the production-default flip (gate removal) until T-06-2
		// e2e + the PR-05 no-DELETE guardrail — #1771 PR-04, per ADR 202606071600-1504 §9/D9.
		logArgs := []any{
			slog.String("gate_env", envProjectionPGJournalPreview),
			slog.Bool("wired", false),
			slog.String("gated_source", "projection_events (durable, production-safe)"),
			slog.String("production_default_flip", "gh #1771 PR-04 (gated on T-06-2 e2e)"),
		}
		if len(generatedProjectionSourceTopics()) == 0 {
			// No projection declared (corebundle's default today): gate-off is a benign
			// empty-workload default, not actionable — log at Info to avoid startup noise
			// on every deployment that ships no projection (controller-runtime posture:
			// an empty workload does not warn).
			slog.Info("projection: no durable journal source wired (no projection declared; gate off)", logArgs...)
		} else {
			// A projection IS declared but the gate is off, so it will fail fast in the
			// phase6 drain. Actionable: warn so the operator sets the gate to wire it.
			slog.Warn("projection: a projection is declared but its durable journal source is gated off; bootstrap will fail fast",
				logArgs...)
		}
		return nil, nil
	}
	// Info (not Warn): wiring the durable, production-safe journal source under the gate is
	// a deliberate lifecycle opt-in, not a degraded mode — positions come from the
	// append-only projection_events journal (never cleaned). The gate remains until PR-04.
	slog.Info("projection: durable journal source wired under gate (positions from append-only projection_events; production-safe)",
		slog.String("gate_env", envProjectionPGJournalPreview),
		slog.Bool("wired", true),
		slog.String("source", "projection_events"),
		slog.String("production_default_flip", "gh #1771 PR-04 (gated on T-06-2 e2e)"))
	// The projection journal's global_seq is per-pool. Sole() is the SANCTIONED single
	// provider accessor for this assembly-wide harness: it returns the lone provider
	// only in colocated topology. In split topology (#2341, N pools) it returns
	// ok=false and we FAIL CLOSED — there is no single comparable global_seq across N
	// independent projection_events journals, so silently picking one pool would
	// corrupt the read model. Cross-pool projection under split topology is future
	// work (backlog) — corebundle ships no projection today, so this is dormant.
	prov, ok := shared.PG.Sole()
	if !ok {
		return nil, fmt.Errorf("projection: durable journal requires colocated postgres (single pool); " +
			"split topology has per-pool projection_events with incomparable global_seq — refusing to wire a single source")
	}
	pool, err := cellsecrets.PgxPoolFromProvider(prov)
	if err != nil {
		return nil, fmt.Errorf("projection pg pool: %w", err)
	}
	checkpointStore, err := adapterpg.NewProjectionCheckpointStore(pool)
	if err != nil {
		return nil, fmt.Errorf("projection checkpoint store: %w", err)
	}
	// One PGProjectionEventSource instance is wired as BOTH the ReplaySource and the
	// LiveCursor (it implements projection.LiveCursor: Position reads global_seq off
	// the carrier; ResolveCarrier resolves a bare live entry by id against the durable
	// journal). Sharing the instance keeps the global_seq encoding identical across
	// the rebuild and live paths.
	source, err := adapterpg.NewProjectionEventSource(pool)
	if err != nil {
		return nil, fmt.Errorf("projection event source: %w", err)
	}
	return []bootstrap.Option{
		bootstrap.WithProjectionCheckpointStore(checkpointStore),
		bootstrap.WithProjectionTxRunner(prov.TxManager()),
		bootstrap.WithProjectionReplaySource(source),
		bootstrap.WithProjectionCursor(source),
		// Differentiated repo-readiness probe for the journal (schema/migration drift
		// + table-permission loss), distinct from the pool-level postgres_ready probe.
		bootstrap.WithHealthChecker(adapterpg.ProbeProjectionJournalReady, source.RepoReady),
	}, nil
}
