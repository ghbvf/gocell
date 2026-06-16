package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/ghbvf/gocell/cellmodules/eventtransport"
	"github.com/ghbvf/gocell/cellmodules/replaydeps"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	kernellifecycle "github.com/ghbvf/gocell/framework/kernel/lifecycle"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/composition"
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
	adapterMode := topo.AdapterMode()

	jwt, err := buildJWTDeps(adapterMode, clk)
	if err != nil {
		return nil, nil, err
	}

	metricsDeps, err := buildSharedMetricsDeps()
	if err != nil {
		return nil, nil, err
	}

	replay, err := replaydeps.Resolve(ctx, clk, topo)
	if err != nil {
		return nil, nil, err
	}
	loaded := false
	var brokerResources []kernellifecycle.ManagedResource
	defer func() {
		if !loaded {
			closeRedisClientAfterFailedLoad(ctx, replay.RedisClient)
			closeBrokerResourcesAfterFailedLoad(ctx, brokerResources)
		}
	}()

	// Topology-gated event transport (#1940): demo topology → in-process bus;
	// postgres topology → real broker (RabbitMQ from GOCELL_AMQP_URL), fail-closed
	// when the broker URL is missing. The in-memory bus is reachable ONLY through
	// eventtransport.Resolve's demo branch — cmd/corebundle must not import
	// runtime/eventbus directly (depguard corebundle-no-direct-eventbus,
	// COREBUNDLE-EVENTBUS-FUNNEL-01).
	transport, err := eventtransport.Resolve(clk, topo, eventtransport.Config{
		AMQPURL: os.Getenv("GOCELL_AMQP_URL"),
	})
	if err != nil {
		return nil, nil, err
	}
	brokerResources = transport.Resources

	primaryAddr, internalAddr, healthAddr := resolveListenerAddrs()

	internalKeyring, err := buildInternalServiceKeyring(adapterMode)
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

	// Build cmdLocals for cmd-private wiring (prometheus adapter types, pool MR,
	// metrics handler). The internal-listener guard components (HMAC ring +
	// NonceStore) now live on composition.SharedDeps, not here — control-plane
	// validation introspects them from SharedDeps (#1410). The configcore key
	// provider is now self-built inside cellmodules/configcore.Module.Provide
	// (#1413/#885): cmd no longer needs vaultTransitMetrics or the vault adapter.
	locals := &cmdLocals{
		registry:       metricsDeps.PromStack.registry,
		hookObserver:   metricsDeps.PromStack.hookObserver,
		metricProvider: metricsDeps.PromStack.metricProvider,
		metricsHandler: metricsHandler,
	}
	locals.redisClient = replay.RedisClient
	// Hand the resolved broker resources (the RabbitMQ connection in postgres
	// mode; empty in demo mode) to cmd wiring so runtimeBaseOptions registers them
	// as ManagedResources for LIFO shutdown.
	locals.brokerResources = transport.Resources
	// Hand the sealed broker-kind fact to cmd wiring so runtimeBaseOptions threads
	// it into bootstrap.WithEventTransportKind for the phase0 split-topology gate (#2211).
	locals.eventTransportKind = transport.Kind

	// Build composition.SharedDeps (public, interface-only fields consumed by
	// platform cell modules). The control-plane production checks (verbose /
	// metrics tokens, internal-listener guard, nonce-store kind, claimer kind)
	// now run inside NewSharedDeps → validate (#1410), reading the promoted
	// InternalServiceKeyring + NonceStore + the self-reporting ConsumerClaimer.Kind().
	// Prometheus adapter types stay in cmdLocals.
	compShared, err := composition.NewSharedDeps(composition.SharedDeps{
		Clock:                  clk,
		Topology:               topo,
		DeploymentTopology:     generatedDeploymentTopology(),
		JWTIssuer:              jwt.issuer,
		JWTVerifier:            jwt.verifier,
		MetricsProvider:        metricsDeps.PromStack.metricProvider,
		Publisher:              transport.Publisher,
		Subscriber:             transport.Subscriber,
		ConfigEventCollector:   metricsDeps.ConfigEventCollector,
		ConsumerClaimer:        replay.ConsumerClaimer,
		InternalServiceKeyring: internalKeyring,
		NonceStore:             replay.NonceStore,
		PrimaryHTTPAddr:        primaryAddr,
		InternalHTTPAddr:       internalAddr,
		HealthHTTPAddr:         healthAddr,
		HealthLocalOnly:        healthLocalOnly,
		MetricsToken:           metricsToken,
		VerboseToken:           verboseToken,
		VerboseDisabled:        verboseDisabled,
		ProjectRoot:            os.Getenv("GOCELL_PROJECT_ROOT"),
	})
	if err != nil {
		slog.Warn("corebundle: SharedDeps validation failed",
			slog.String("requested_mode", adapterMode),
			slog.String("effective_mode", topo.AdapterInfo()["mode"]))
		return nil, nil, err
	}

	// Residual cmd-deployment-contract check: reject the .env.example sample
	// verbose token in real mode (a cmd artifact, not a portable composition
	// contract). All other control-plane checks moved into NewSharedDeps (#1410).
	if err := validateCorebundleDeps(compShared); err != nil {
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
