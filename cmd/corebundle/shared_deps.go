package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"

	prom "github.com/prometheus/client_golang/prometheus"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

// SharedDeps holds cross-cutting dependencies required by every Cell module.
// Cell-specific dependencies (KeyProvider, PoolResource, cursor codecs, HMAC key)
// are managed by the corresponding *_module.go file.
//
// SharedDeps is passed directly to BuildApp and each CellModule.Provide,
// giving type-safe access to all cross-cutting fields without type-assertion.
//
// Fields are flat (no concern-grouped sub-structs): SharedDeps is a
// composition-root bag whose fields cross consumer boundaries (Clock /
// Topology / SharedPGPool consumed by every Cell module). Forcing a sub-struct
// layout would make those cross-cutting consumptions look like boundary
// violations when in fact they are the natural shape of a composition root.
// Per-concern *file* split is in shared_deps_build.go (build helpers) and
// shared_deps_validate.go (startup invariants); the struct itself stays flat,
// matching runtime/bootstrap/bootstrap.go which adopted the same trade-off.
//
// ref: uber-go/fx fx.Supply — shared values provided once to all modules.
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/options/validation.go —
// all required fields validated in one place before startup.
// ref: runtime/bootstrap/bootstrap.go:50-71 — flat struct + per-concern file
// split rationale (PR-A66 BOOTSTRAP-STRUCT-DECOMPOSE).
type SharedDeps struct {
	// Clock is the single root clock instance threaded through every adapter,
	// service, and middleware constructed by BuildApp / its module builders.
	// Tests inject a clockmock.FakeClock to drive time deterministically;
	// production wires clock.Real() exactly once at the entry point.
	Clock clock.Clock

	// Topology is the resolved adapter-mode / storage-backend combination.
	Topology bootstrap.Topology

	// JWTDeps holds the JWT issuer and verifier.
	JWTDeps jwtDeps

	// PromStack holds the Prometheus registry, hook observer, and metric provider.
	PromStack promStack

	// EventBus is the in-process event bus used for both publish and subscribe.
	EventBus *eventbus.InMemoryEventBus

	// ConfigEventCollector records config consumer process and settlement
	// metrics for accesscore and configcore. It is registered once against the
	// shared metrics provider and then injected into both Cells plus middleware.
	ConfigEventCollector obmetrics.ConfigEventCollector

	// SharedPGPool is the postgres pool created by ConfigCoreModule when running
	// in StorageBackend == "postgres" mode. AccessCoreModule + AuditCoreModule
	// receive the same pointer to wire their TxManager / OutboxWriter without
	// double-creating a pool.
	//
	// Happens-before contract (load-bearing, do not break):
	//   - The pool is registered as a ManagedResource by ConfigCoreModule.Provide
	//     before any consumer module reads it.
	//   - All consumers of this pool (TxManager, OutboxWriter, OutboxRelay,
	//     EventRouter goroutines, ConsumerBase workers) MUST be registered AFTER
	//     the pool ManagedResource — i.e. as later WithManagedResource /
	//     WithWorkers options — so bootstrap's LIFO shutdown order stops them
	//     before pool.Close() runs.
	//   - main.go::BuildApp module order locks this for cell modules
	//     (MODULE-ORDER-CONFIGCORE-FIRST-01 archtest).
	//   - For new lifecycle hooks added outside cell modules: register them
	//     after the pool, never before.
	//
	// Violating this contract produces use-after-close errors during shutdown
	// that are silent in normal runs (Close() returns first, workers' next DB
	// call fails).
	//
	// Nil in non-postgres modes (in-memory).
	SharedPGPool *adapterpg.Pool

	// RedisClient is configured when distributed replay/idempotency state is
	// required or when the operator explicitly provides Redis env vars.
	RedisClient *adapterredis.Client

	// ConsumerClaimer coordinates outbox consumer idempotency. The separate
	// kind field is corebundle-local metadata; kernel/idempotency.Claimer stays
	// behavior-only and does not grow a topology method.
	ConsumerClaimer     idempotency.Claimer
	ConsumerClaimerKind consumerClaimerKind

	// InternalGuard is the service-token guard protecting /internal/v1/*.
	// Required in every mode; internalGuardFromEnv rejects an empty
	// GOCELL_SERVICE_SECRET before runtime listener wiring.
	//
	// Held as a typed value (rather than a bare middleware closure) so
	// validateControlPlane can inspect the backing NonceStore and reject
	// Noop implementations in production — a middleware func would make the
	// replay-defense class invisible to SharedDeps.Validate.
	InternalGuard *internalGuard

	// PrimaryHTTPAddr is the bind address for the public HTTP listener
	// (/api/v1/*, infra endpoints). Env GOCELL_HTTP_PRIMARY_ADDR; default ":8080".
	PrimaryHTTPAddr string

	// InternalHTTPAddr is the bind address for the internal HTTP listener
	// (/internal/v1/* control-plane). Env GOCELL_HTTP_INTERNAL_ADDR;
	// default "127.0.0.1:9090". Must be bound to an internal network segment in
	// production so service-token / mTLS enforcement is the primary defense.
	InternalHTTPAddr string

	// HealthHTTPAddr is the bind address for the health+metrics listener
	// (/healthz /readyz /metrics). Env GOCELL_HTTP_HEALTH_ADDR;
	// default "127.0.0.1:9091" for local/dev only. Production deployments
	// using kubelet HTTP probes or Prometheus PodIP/Service scrapes must bind a
	// Pod-reachable address such as ":9091"; loopback is allowed in real mode
	// only when HealthLocalOnly explicitly opts into same-pod/exec access.
	HealthHTTPAddr string

	// HealthLocalOnly explicitly waives the real-mode guard that rejects
	// loopback-only HealthHTTPAddr values. Set via GOCELL_HTTP_HEALTH_LOCAL_ONLY=1
	// only for deployments where health/metrics are reached from the same
	// network namespace (local dev, same-pod sidecar, or exec-probe style).
	HealthLocalOnly bool

	// MetricsToken is the token guarding /metrics. Required in production
	// topology; may be empty in dev mode.
	MetricsToken string

	// VerboseToken is the token guarding /readyz?verbose. After PR-A35
	// Validate() requires a non-empty token in every adapter mode unless
	// VerboseDisabled is true — the previous "empty in dev mode = open
	// verbose" backward-compat path was removed so that an unset environment
	// variable never silently exposes internal topology.
	VerboseToken string

	// VerboseDisabled declares that /readyz?verbose must not be served on
	// this deployment. When true, Validate() no longer requires VerboseToken
	// and Bootstrap is wired with WithVerboseDisabled so the handler answers
	// every ?verbose request with the plain aggregate body. Set it via
	// GOCELL_READYZ_VERBOSE_DISABLED=1 for ephemeral deployments that waive
	// the debug channel.
	VerboseDisabled bool

	// ProjectRoot is the directory used by the devtools catalog endpoint to
	// locate cell.yaml / slice.yaml metadata. Read from GOCELL_PROJECT_ROOT;
	// empty when the var is unset (endpoint is disabled gracefully).
	ProjectRoot string

	// metricsHandler is the Prometheus HTTP handler built once in
	// LoadSharedDepsFromEnv and reused by defaultRuntimeOptions.
	metricsHandler http.Handler

	// keyProviderMetricCollectors are the collectors currently registered for
	// the ConfigCore KeyProvider. ConfigCoreModule.Provide may be called more
	// than once against the same SharedDeps in tests/rebuild paths; tracking
	// ownership here lets the module replace provider-bound GaugeFunc collectors
	// instead of leaving stale closures attached to an older provider instance.
	keyProviderMetricCollectors []prom.Collector
}

// SampleVerbosePlaceholder is the literal placeholder shipped in .env.example so
// `cp .env.example .env && go run ./cmd/corebundle` works without first
// minting a secret. validateControlPlane rejects this exact value in
// adapter mode "real" — production deployments must mint their own
// high-entropy token. Exposed (capitalised) so example/test code and the
// regression test in shared_deps_test.go reference one source of truth.
const SampleVerbosePlaceholder = "dev-readyz-verbose-token-change-me"

// LoadSharedDepsFromEnv reads all environment variables and builds a fully
// populated SharedDeps for cross-cutting concerns. Cell-specific dependencies
// (cursor codecs, HMAC key, KeyProvider, PG config) are constructed in each
// CellModule.Provide.
//
// ref: go-zero serviceconf.MustLoad — single parse-validate call at startup.
func LoadSharedDepsFromEnv(ctx context.Context) (*SharedDeps, error) {
	// Single root clock: constructed exactly once here and threaded through
	// every adapter, service, and middleware via SharedDeps.Clock.
	clk := clock.Real()

	topo, err := bootstrap.TopologyFromEnv()
	if err != nil {
		return nil, err
	}
	adapterMode := topo.AdapterMode

	jwt, err := buildJWTDeps(adapterMode, clk)
	if err != nil {
		return nil, err
	}

	metricsDeps, err := buildSharedMetricsDeps()
	if err != nil {
		return nil, err
	}

	replay, err := buildSharedReplayDeps(ctx, topo, clk)
	if err != nil {
		return nil, err
	}
	loaded := false
	defer func() {
		if !loaded {
			closeRedisClientAfterFailedLoad(ctx, replay.RedisClient)
		}
	}()

	eb := eventbus.New(eventbus.WithClock(clk))

	primaryAddr, internalAddr, healthAddr := resolveListenerAddrs()

	internalGuard, err := internalGuardFromEnv(adapterMode, replay.NonceStore, clk)
	if err != nil {
		return nil, err
	}

	verboseToken := os.Getenv("GOCELL_READYZ_VERBOSE_TOKEN")
	verboseDisabled := os.Getenv("GOCELL_READYZ_VERBOSE_DISABLED") == "1"

	healthLocalOnlyRaw := os.Getenv("GOCELL_HTTP_HEALTH_LOCAL_ONLY")
	healthLocalOnly := healthLocalOnlyRaw == "1" || strings.EqualFold(healthLocalOnlyRaw, "true")

	metricsToken := os.Getenv("GOCELL_METRICS_TOKEN")
	metricsHandler := buildMetricsHandler(metricsToken, metricsDeps.PromStack.registry)

	// PR-A14a: surface the pre-PR-A14a env var rename so operators upgrading
	// from a single-listener binary see a clear signal if they have only the
	// old var set. Without this warn the addrs would silently fall through
	// to defaults, binding 8080/9090 instead of whatever the old
	// GOCELL_HTTP_ADDR pointed at.
	if legacy := os.Getenv("GOCELL_HTTP_ADDR"); legacy != "" {
		if os.Getenv("GOCELL_HTTP_PRIMARY_ADDR") == "" && os.Getenv("GOCELL_HTTP_INTERNAL_ADDR") == "" {
			slog.Warn("GOCELL_HTTP_ADDR is no longer consumed (PR-A14a dual-listener);"+
				" set GOCELL_HTTP_PRIMARY_ADDR and GOCELL_HTTP_INTERNAL_ADDR instead",
				slog.String("legacy_value", strings.ReplaceAll(legacy, "\n", "")))
		}
	}

	deps := &SharedDeps{
		Clock:                clk,
		Topology:             topo,
		JWTDeps:              jwt,
		PromStack:            metricsDeps.PromStack,
		EventBus:             eb,
		ConfigEventCollector: metricsDeps.ConfigEventCollector,
		RedisClient:          replay.RedisClient,
		ConsumerClaimer:      replay.ConsumerClaimer,
		ConsumerClaimerKind:  replay.ConsumerClaimerKind,
		InternalGuard:        internalGuard,
		PrimaryHTTPAddr:      primaryAddr,
		InternalHTTPAddr:     internalAddr,
		HealthHTTPAddr:       healthAddr,
		HealthLocalOnly:      healthLocalOnly,
		MetricsToken:         metricsToken,
		VerboseToken:         verboseToken,
		VerboseDisabled:      verboseDisabled,
		metricsHandler:       metricsHandler,
		ProjectRoot:          os.Getenv("GOCELL_PROJECT_ROOT"),
	}

	if err := deps.Validate(); err != nil {
		// Surface adapter mode on the failure path so operators can correlate the
		// validation error with the requested vs. effective topology without
		// chasing the typed Error's structured fields. The success-path Info log
		// below intentionally fires only after Validate passes (see Wave 4 fix).
		slog.Warn("corebundle: SharedDeps validation failed",
			slog.String("requested_mode", adapterMode),
			slog.String("effective_mode", topo.AdapterInfo()["mode"]))
		return nil, err
	}
	slog.Info("adapter mode",
		slog.String("requested", adapterMode),
		slog.String("effective", topo.AdapterInfo()["mode"]))
	loaded = true
	return deps, nil
}
