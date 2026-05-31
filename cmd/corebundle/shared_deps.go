package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/platform/platformshared"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/composition"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

// SampleVerbosePlaceholder is the literal placeholder shipped in .env.example so
// `cp .env.example .env && go run ./cmd/corebundle` works without first
// minting a secret. validateControlPlane rejects this exact value in
// adapter mode "real" — production deployments must mint their own
// high-entropy token. Exposed (capitalised) so example/test code and the
// regression test in shared_deps_test.go reference one source of truth.
const SampleVerbosePlaceholder = "dev-readyz-verbose-token-change-me"

// LoadSharedDepsFromEnv reads all environment variables and builds a fully
// populated composition.SharedDeps (for platform cell modules) and cmdLocals
// (for cmd-private wiring: prometheus adapter types, internal guard, claimer
// kind, pool MR, metrics handler).
//
// ref: go-zero serviceconf.MustLoad — single parse-validate call at startup.
func LoadSharedDepsFromEnv(ctx context.Context) (*composition.SharedDeps, *cmdLocals, error) {
	// Single root clock: constructed exactly once here and threaded through
	// every adapter, service, and middleware.
	clk := clock.Real()

	topo, err := bootstrap.TopologyFromEnv()
	if err != nil {
		return nil, nil, err
	}
	adapterMode := topo.AdapterMode

	jwt, err := buildJWTDeps(adapterMode, clk)
	if err != nil {
		return nil, nil, err
	}

	metricsDeps, err := buildSharedMetricsDeps()
	if err != nil {
		return nil, nil, err
	}

	replay, err := buildSharedReplayDeps(ctx, topo, clk)
	if err != nil {
		return nil, nil, err
	}
	loaded := false
	defer func() {
		if !loaded {
			closeRedisClientAfterFailedLoad(ctx, replay.RedisClient)
		}
	}()

	eb := eventbus.New(clk)

	primaryAddr, internalAddr, healthAddr := resolveListenerAddrs()

	guard, err := internalGuardFromEnv(adapterMode, replay.NonceStore, clk)
	if err != nil {
		return nil, nil, err
	}

	verboseToken := os.Getenv("GOCELL_READYZ_VERBOSE_TOKEN")
	verboseDisabled := os.Getenv("GOCELL_READYZ_VERBOSE_DISABLED") == "1"

	healthLocalOnlyRaw := os.Getenv("GOCELL_HTTP_HEALTH_LOCAL_ONLY")
	healthLocalOnly := healthLocalOnlyRaw == "1" || strings.EqualFold(healthLocalOnlyRaw, "true")

	metricsToken := os.Getenv("GOCELL_METRICS_TOKEN")
	metricsHandler := buildMetricsHandler(metricsToken, metricsDeps.PromStack.registry)

	// PR-A14a: surface the pre-PR-A14a env var rename so operators upgrading
	// from a single-listener binary see a clear signal if they have only the
	// old var set.
	if legacy := os.Getenv("GOCELL_HTTP_ADDR"); legacy != "" {
		if os.Getenv("GOCELL_HTTP_PRIMARY_ADDR") == "" && os.Getenv("GOCELL_HTTP_INTERNAL_ADDR") == "" {
			slog.Warn("GOCELL_HTTP_ADDR is no longer consumed (PR-A14a dual-listener);"+
				" set GOCELL_HTTP_PRIMARY_ADDR and GOCELL_HTTP_INTERNAL_ADDR instead",
				slog.String("legacy_value", strings.ReplaceAll(legacy, "\n", "")))
		}
	}

	// Build cmdLocals for cmd-private wiring (prometheus adapter types,
	// vault-metrics factory, internalGuard, consumerClaimerKind, etc.).
	// locals must be built before the configcore key-provider so that
	// locals.vaultTransitMetrics (the once-guarded factory) is available.
	locals := &cmdLocals{
		registry:            metricsDeps.PromStack.registry,
		hookObserver:        metricsDeps.PromStack.hookObserver,
		metricProvider:      metricsDeps.PromStack.metricProvider,
		internalGuard:       guard,
		consumerClaimerKind: replay.ConsumerClaimerKind,
		metricsHandler:      metricsHandler,
	}
	locals.redisClient = replay.RedisClient
	locals.initVaultMetricsFactory()

	// Build configcore key provider + stale-cipher counter callback.
	// These live in cmd because they import adapters/vault + prometheus which
	// must not reach runtime/composition or platform/configcore.
	cfgProviderName, cfgMasterKey, cfgPrevMasterKey := platformshared.LoadConfigCoreKeyProvider()
	cfgKeyProvider, cfgStaleCipherInc, err := buildConfigCoreKeyProvider(
		topo.StorageBackend, adapterMode,
		cfgProviderName, cfgMasterKey, cfgPrevMasterKey,
		clk,
		metricsDeps.PromStack.registry,
		locals.vaultTransitMetrics,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("configcore key provider: %w", err)
	}

	// Build composition.SharedDeps (public, interface-only fields consumed by
	// platform cell modules). Prometheus adapter types, internalGuard, and
	// consumerClaimerKind stay in cmdLocals.
	compShared := &composition.SharedDeps{
		Clock:                  clk,
		Topology:               topo,
		JWTIssuer:              jwt.issuer,
		JWTVerifier:            jwt.verifier,
		MetricsProvider:        metricsDeps.PromStack.metricProvider,
		EventBus:               eb,
		ConfigEventCollector:   metricsDeps.ConfigEventCollector,
		EventbusCacheCollector: metricsDeps.EventbusCacheCollector,
		ConsumerClaimer:        replay.ConsumerClaimer,
		InternalHMACRing:       guard.ring,
		PrimaryHTTPAddr:        primaryAddr,
		InternalHTTPAddr:       internalAddr,
		HealthHTTPAddr:         healthAddr,
		HealthLocalOnly:        healthLocalOnly,
		MetricsToken:           metricsToken,
		VerboseToken:           verboseToken,
		VerboseDisabled:        verboseDisabled,
		ProjectRoot:            os.Getenv("GOCELL_PROJECT_ROOT"),
		ConfigKeyProvider:      cfgKeyProvider,
		ConfigStaleCipherInc:   cfgStaleCipherInc,
	}

	// Validate composition.SharedDeps (cross-cutting interface-level fields).
	if err := compShared.Validate(); err != nil {
		slog.Warn("corebundle: SharedDeps validation failed",
			slog.String("requested_mode", adapterMode),
			slog.String("effective_mode", topo.AdapterInfo()["mode"]))
		return nil, nil, err
	}

	// Cmd-side production validation (nonce store kind, claimer kind, health
	// reachability, control-plane tokens) that depends on cmd-private types.
	if err := validateCorebundleDeps(compShared, locals); err != nil {
		slog.Warn("corebundle: cmd-side validation failed",
			slog.String("requested_mode", adapterMode),
			slog.String("effective_mode", topo.AdapterInfo()["mode"]))
		return nil, nil, err
	}

	slog.Info("adapter mode",
		slog.String("requested", adapterMode),
		slog.String("effective", topo.AdapterInfo()["mode"]))

	loaded = true
	return compShared, locals, nil
}
