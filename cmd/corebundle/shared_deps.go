package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
	prom "github.com/prometheus/client_golang/prometheus"
)

// SharedDeps holds cross-cutting dependencies required by every Cell module.
// Cell-specific dependencies (KeyProvider, PGResource, cursor codecs, HMAC key)
// are managed by the corresponding *_module.go file.
//
// SharedDeps is passed directly to BuildApp and each CellModule.Provide,
// giving type-safe access to all cross-cutting fields without type-assertion.
//
// ref: uber-go/fx fx.Supply — shared values provided once to all modules.
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/options/validation.go —
// all required fields validated in one place before startup.
type SharedDeps struct {
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

type sharedReplayDeps struct {
	RedisClient         *adapterredis.Client
	NonceStore          auth.NonceStore
	ConsumerClaimer     idempotency.Claimer
	ConsumerClaimerKind consumerClaimerKind
}

type sharedMetricsDeps struct {
	PromStack            promStack
	ConfigEventCollector obmetrics.ConfigEventCollector
}

func buildSharedMetricsDeps() (sharedMetricsDeps, error) {
	ps, err := buildPromStack()
	if err != nil {
		return sharedMetricsDeps{}, err
	}
	configEventCollector, err := obmetrics.NewProviderConfigEventCollector(ps.metricProvider)
	if err != nil {
		return sharedMetricsDeps{}, fmt.Errorf("build config event metrics collector: %w", err)
	}
	return sharedMetricsDeps{
		PromStack:            ps,
		ConfigEventCollector: configEventCollector,
	}, nil
}

func buildSharedReplayDeps(ctx context.Context, topo bootstrap.Topology) (sharedReplayDeps, error) {
	redisResult, err := buildRedisClient(ctx, topo)
	if err != nil {
		return sharedReplayDeps{}, err
	}
	redisClient := redisResult.Client
	loaded := false
	defer func() {
		if !loaded {
			closeRedisClientAfterFailedLoad(ctx, redisClient)
		}
	}()

	nonceStore, err := buildServiceNonceStore(topo, redisClient)
	if err != nil {
		return sharedReplayDeps{}, err
	}
	claimer, claimerKind, err := buildConsumerClaimer(topo, redisClient)
	if err != nil {
		return sharedReplayDeps{}, err
	}

	loaded = true
	return sharedReplayDeps{
		RedisClient:         redisClient,
		NonceStore:          nonceStore,
		ConsumerClaimer:     claimer,
		ConsumerClaimerKind: claimerKind,
	}, nil
}

// resolveListenerAddrs returns primary / internal / health bind addresses,
// applying default ports when the matching env var is unset:
//
//   - primary  → `:8080`
//   - internal → `127.0.0.1:9090` (loopback by default; service-token gated
//     in every mode; operators binding to a VPC interface must set
//     GOCELL_HTTP_INTERNAL_ADDR explicitly)
//   - health   → `127.0.0.1:9091` (separate loopback port; real-mode
//     PodIP/Service probes must set a Pod-reachable bind such as `:9091`,
//     or explicitly opt into same-netns access with GOCELL_HTTP_HEALTH_LOCAL_ONLY=1)
func resolveListenerAddrs() (primary, internal, health string) {
	primary = os.Getenv("GOCELL_HTTP_PRIMARY_ADDR")
	if primary == "" {
		primary = ":8080"
	}
	internal = os.Getenv("GOCELL_HTTP_INTERNAL_ADDR")
	if internal == "" {
		internal = "127.0.0.1:9090"
	}
	health = os.Getenv("GOCELL_HTTP_HEALTH_ADDR")
	if health == "" {
		health = "127.0.0.1:9091"
	}
	return
}

// closeRedisClientAfterFailedLoad is the single source of truth for "close
// Redis with nil-safe + slog warn". Two callers, both following the same
// `if !loaded { close }` defer pattern (one inside buildSharedReplayDeps for
// inner-construction failure, one in buildSharedDeps for outer-composition
// failure after replay deps are already attached). The structure is mirrored
// at both sites so the two scopes can be visually compared in one read.
func closeRedisClientAfterFailedLoad(ctx context.Context, client *adapterredis.Client) {
	if client == nil {
		return
	}
	if closeErr := client.Close(ctx); closeErr != nil {
		slog.Warn("corebundle: failed to close Redis client after startup validation failure",
			slog.String("error", closeErr.Error()))
	}
}

func adapterInfoForSharedDeps(shared *SharedDeps) map[string]string {
	info := shared.Topology.AdapterInfo()
	redisState := "not-configured"
	if shared.RedisClient != nil {
		redisState = "configured"
	}
	nonceStoreKind := string(auth.NonceStoreKindNoop)
	if shared.InternalGuard != nil && shared.InternalGuard.NonceStore() != nil {
		nonceStoreKind = string(shared.InternalGuard.NonceStore().Kind())
	}
	claimerKind := string(shared.ConsumerClaimerKind)
	if claimerKind == "" {
		claimerKind = string(consumerClaimerKindUnknown)
	}
	info["redis"] = redisState
	info["service_token_nonce_store"] = nonceStoreKind
	info["outbox_consumer_claimer"] = claimerKind
	return info
}

// SampleVerboseToken is the literal placeholder shipped in .env.example so
// `cp .env.example .env && go run ./cmd/corebundle` works without first
// minting a secret. validateControlPlane rejects this exact value in
// adapter mode "real" — production deployments must mint their own
// high-entropy token. Exposed (capitalised) so example/test code and the
// regression test in shared_deps_test.go reference one source of truth.
// production deployments must mint their own high-entropy token.
//
//nolint:gosec // G101: SampleVerboseToken is the constant name (not a credential value);
const SampleVerboseToken = "dev-readyz-verbose-token-change-me"

// Validate is the startup invariant check for all cross-cutting dependencies.
// Storage-specific invariants (PGResource, cursor codecs, HMAC key) are checked
// inside the corresponding CellModule.Provide, not here.
//
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/options/validation.go —
// validates all fields before any component is constructed.
func (d *SharedDeps) Validate() error {
	if d == nil {
		return errcode.New(errcode.ErrValidationFailed, "SharedDeps: nil receiver")
	}
	errs := d.validateCore()
	errs = append(errs, d.validateVerboseEndpoint()...)
	errs = append(errs, d.validateHealthReachability()...)
	errs = append(errs, d.validateInternalListenerGuard()...)
	errs = append(errs, d.validateControlPlane()...)
	return errors.Join(errs...)
}

// validateVerboseEndpoint enforces that every adapter mode either configures
// a verbose token or explicitly waives the endpoint. The previous dev-mode
// fallback (unset env var => verbose open) was removed in PR-A35 so a
// forgotten GOCELL_READYZ_VERBOSE_TOKEN in dev cannot silently expose cell
// topology to anyone who can reach the port.
func (d *SharedDeps) validateVerboseEndpoint() []error {
	if d.VerboseDisabled {
		// Both set is not a hard validation failure — VerboseDisabled
		// wins, Handler will serve the plain aggregate body regardless of
		// the token. But it is almost certainly a misconfiguration: the
		// operator either wanted token-gated access (drop the DISABLED
		// flag) or wanted to waive verbose entirely (unset the TOKEN).
		// Surface it as a Warn so operators can spot it in startup logs.
		if d.VerboseToken != "" {
			slog.Warn("GOCELL_READYZ_VERBOSE_TOKEN is set but GOCELL_READYZ_VERBOSE_DISABLED=1 overrides it; " +
				"the token will not be enforced. Drop one of the two env vars to remove the ambiguity.")
		}
		return nil
	}
	if d.VerboseToken != "" {
		return nil
	}
	return []error{errcode.New(errcode.ErrControlplaneVerboseTokenMissing,
		"GOCELL_READYZ_VERBOSE_TOKEN must be set (or GOCELL_READYZ_VERBOSE_DISABLED=1 "+
			"to waive the verbose endpoint) so /readyz?verbose is never anonymous")}
}

// validateCore collects missing-field errors for dependencies required in
// every topology.
func (d *SharedDeps) validateCore() []error {
	var errs []error
	missing := func(field string) {
		errs = append(errs, errcode.New(errcode.ErrValidationFailed,
			"SharedDeps."+field+" must be set"))
	}
	if d.JWTDeps.issuer == nil {
		missing("JWTDeps.issuer")
	}
	if d.JWTDeps.verifier == nil {
		missing("JWTDeps.verifier")
	}
	if d.PromStack.registry == nil {
		missing("PromStack.registry")
	}
	if d.PromStack.hookObserver == nil {
		missing("PromStack.hookObserver")
	}
	if d.PromStack.metricProvider == nil {
		missing("PromStack.metricProvider")
	}
	if d.EventBus == nil {
		missing("EventBus")
	}
	if d.ConfigEventCollector == nil {
		missing("ConfigEventCollector")
	}
	if d.ConsumerClaimer == nil {
		missing("ConsumerClaimer")
	}
	return errs
}

// validateControlPlane collects errors for the production control-plane gate
// (tokens + guard required whenever real keys are in use).
func (d *SharedDeps) validateControlPlane() []error {
	if !d.Topology.RequireProductionControlPlane() {
		return nil
	}
	var errs []error
	// The unconditional /readyz?verbose invariant is now enforced by
	// validateVerboseEndpoint in every mode. Production additionally forbids
	// waiving the endpoint: a "real" deployment that still sets
	// GOCELL_READYZ_VERBOSE_DISABLED=1 is almost certainly a misconfiguration
	// and would leave operators without a token-gated diagnostic path.
	if d.VerboseDisabled {
		errs = append(errs, errcode.New(errcode.ErrControlplaneVerboseTokenMissing,
			"GOCELL_READYZ_VERBOSE_DISABLED=1 is not allowed in adapter mode \"real\"; "+
				"production must keep the token-gated verbose endpoint available for "+
				"on-call diagnostics"))
	}
	if d.VerboseToken == SampleVerboseToken {
		errs = append(errs, errcode.New(errcode.ErrControlplaneVerboseTokenSample,
			"GOCELL_READYZ_VERBOSE_TOKEN is set to the .env.example placeholder ("+
				SampleVerboseToken+"); a production deploy must mint its own "+
				"high-entropy secret. This exact value is publicly known via the repo "+
				"sample and would expose /readyz?verbose topology to anyone who has "+
				"read the source tree."))
	}
	if d.MetricsToken == "" {
		errs = append(errs, errcode.New(errcode.ErrValidationFailed,
			"GOCELL_METRICS_TOKEN must be set in adapter mode \"real\" "+
				"to prevent anonymous /metrics exposure; scrapers must send X-Metrics-Token header"))
	}
	if d.InternalGuard == nil {
		errs = append(errs, errcode.New(errcode.ErrControlplaneServiceSecretMissing,
			"GOCELL_SERVICE_SECRET must be set in adapter mode \"real\" "+
				"to protect /internal/v1/*"))
	} else if ns := d.InternalGuard.NonceStore(); ns == nil {
		errs = append(errs, errcode.New(errcode.ErrControlplaneNonceStoreMissing,
			"internalGuard.nonceStore is nil; guard constructed without WithServiceTokenNonceStore"))
	} else if kind := ns.Kind(); kind == auth.NonceStoreKindNoop {
		errs = append(errs, errcode.New(errcode.ErrControlplaneNonceStoreMissing,
			"control-plane NonceStore must be a replay-safe implementation in "+
				"adapter mode \"real\"; NoopNonceStore detected — inject "+
				"InMemoryNonceStore (single pod) or a shared store (multi-pod) "+
				"via WithServiceTokenNonceStore"))
	} else if kind == auth.NonceStoreKindInMemory && !d.Topology.SinglePodReplayProtection && d.Topology.RequireProductionControlPlane() {
		slog.Warn("controlplane: in-memory nonce store rejected for multi-pod deployment",
			slog.String("nonce_store_kind", string(kind)),
			slog.String("hint", "set GOCELL_SINGLE_POD=1 for single-pod deployments or configure a distributed NonceStore"))
		errs = append(errs, errcode.New(errcode.ErrControlplaneNonceStoreMissing,
			"in-memory nonce store requires GOCELL_SINGLE_POD=1 (single-pod deployments) "+
				"or a distributed store via WithServiceTokenNonceStore (multi-pod); "+
				"refuse fail-open"))
	}
	if requiresDistributedReplay(d.Topology) && d.ConsumerClaimerKind != consumerClaimerKindDistributed {
		errs = append(errs, errcode.New(errcode.ErrControlplaneClaimerNotDistributed,
			"ERR_CONTROLPLANE_CLAIMER_NOT_DISTRIBUTED: real multi-pod deployments require Redis-backed "+
				"outbox idempotency claimer; set "+envRedisAddr+" or run with GOCELL_SINGLE_POD=1"))
	}
	return errs
}

func (d *SharedDeps) validateInternalListenerGuard() []error {
	if d.InternalHTTPAddr == "" {
		return []error{errcode.New(errcode.ErrValidationFailed,
			"SharedDeps.InternalHTTPAddr must be set; the internal listener is always enabled and protected by GOCELL_SERVICE_SECRET")}
	}
	if d.InternalGuard != nil {
		return nil
	}
	return []error{errcode.New(errcode.ErrControlplaneServiceSecretMissing,
		"SharedDeps.InternalGuard must be set to protect /internal/v1/*; set GOCELL_SERVICE_SECRET")}
}

func (d *SharedDeps) validateHealthReachability() []error {
	if !d.Topology.RequireProductionControlPlane() || d.HealthLocalOnly {
		return nil
	}
	if d.HealthHTTPAddr == "" || !isLoopbackBindAddr(d.HealthHTTPAddr) {
		return nil
	}
	return []error{errcode.New(errcode.ErrValidationFailed,
		"GOCELL_HTTP_HEALTH_ADDR is loopback-only in adapter mode \"real\"; "+
			"kubelet HTTP probes and Prometheus PodIP/Service scrapes cannot reach "+
			"container loopback. Set GOCELL_HTTP_HEALTH_ADDR=:9091 (or a Pod-reachable "+
			"address), or set GOCELL_HTTP_HEALTH_LOCAL_ONLY=1 only for same-pod sidecar "+
			"or exec-probe deployments.")}
}

func isLoopbackBindAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	host = strings.Trim(host, "[]")
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// LoadSharedDepsFromEnv reads all environment variables and builds a fully
// populated SharedDeps for cross-cutting concerns. Cell-specific dependencies
// (cursor codecs, HMAC key, KeyProvider, PG config) are constructed in each
// CellModule.Provide.
//
// ref: go-zero serviceconf.MustLoad — single parse-validate call at startup.
func LoadSharedDepsFromEnv(ctx context.Context) (*SharedDeps, error) {
	topo, err := bootstrap.TopologyFromEnv()
	if err != nil {
		return nil, err
	}
	adapterMode := topo.AdapterMode

	jwt, err := buildJWTDeps(adapterMode)
	if err != nil {
		return nil, err
	}

	metricsDeps, err := buildSharedMetricsDeps()
	if err != nil {
		return nil, err
	}

	replay, err := buildSharedReplayDeps(ctx, topo)
	if err != nil {
		return nil, err
	}
	loaded := false
	defer func() {
		if !loaded {
			closeRedisClientAfterFailedLoad(ctx, replay.RedisClient)
		}
	}()

	eb := eventbus.New()

	primaryAddr, internalAddr, healthAddr := resolveListenerAddrs()

	internalGuard, err := internalGuardFromEnv(adapterMode, replay.NonceStore)
	if err != nil {
		return nil, err
	}

	verboseToken := os.Getenv("GOCELL_READYZ_VERBOSE_TOKEN")
	verboseDisabled := os.Getenv("GOCELL_READYZ_VERBOSE_DISABLED") == "1"

	healthLocalOnlyRaw := os.Getenv("GOCELL_HTTP_HEALTH_LOCAL_ONLY")
	healthLocalOnly := healthLocalOnlyRaw == "1" || strings.EqualFold(healthLocalOnlyRaw, "true")

	metricsToken := os.Getenv("GOCELL_METRICS_TOKEN")
	metricsHandler := buildMetricsHandler(metricsToken, metricsDeps.PromStack.registry)

	slog.Info("adapter mode",
		slog.String("requested", adapterMode),
		slog.String("effective", topo.AdapterInfo()["mode"]))

	// PR-A14a: surface the pre-PR-A14a env var rename so operators upgrading
	// from a single-listener binary see a clear signal if they have only the
	// old var set. Without this warn the addrs would silently fall through
	// to defaults, binding 8080/9090 instead of whatever the old
	// GOCELL_HTTP_ADDR pointed at.
	if legacy := os.Getenv("GOCELL_HTTP_ADDR"); legacy != "" {
		if os.Getenv("GOCELL_HTTP_PRIMARY_ADDR") == "" && os.Getenv("GOCELL_HTTP_INTERNAL_ADDR") == "" {
			//nolint:gosec // G706: structured slog field, not string concatenation
			slog.Warn("GOCELL_HTTP_ADDR is no longer consumed (PR-A14a dual-listener);"+
				" set GOCELL_HTTP_PRIMARY_ADDR and GOCELL_HTTP_INTERNAL_ADDR instead",
				slog.String("legacy_value", legacy))
		}
	}

	deps := &SharedDeps{
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
	}

	if err := deps.Validate(); err != nil {
		return nil, err
	}
	loaded = true
	return deps, nil
}
