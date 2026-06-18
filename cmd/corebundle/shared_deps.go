package main

import (
	"context"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/cellmodules/celltls"
	"github.com/ghbvf/gocell/cellmodules/eventtransport"
	"github.com/ghbvf/gocell/cellmodules/replaydeps"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	kernellifecycle "github.com/ghbvf/gocell/framework/kernel/lifecycle"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/composition"
)

// SampleVerbosePlaceholder is the literal placeholder shipped in deploy/.env.example so
// `cp deploy/.env.example .env && go run ./cmd/corebundle` works without first
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
	// postgres topology → real broker (RabbitMQ), fail-closed when the broker URL is
	// missing. The in-memory bus is reachable ONLY through eventtransport.Resolve's
	// demo branch — cmd/corebundle must not import runtime/eventbus directly
	// (depguard corebundle-no-direct-eventbus, COREBUNDLE-EVENTBUS-FUNNEL-01).
	//
	// #2152 PR-2: the broker URL is read per cell (GOCELL_<CELLID>_AMQP_URL, falling
	// back to GOCELL_AMQP_URL) for the broker-requiring cells (= the postgres cell
	// set), then deduped by eventtransport. Colocated assemblies share one
	// GOCELL_AMQP_URL → one connection (behavior-preserving); distinct per-cell URLs
	// are fail-closed (egress-only — a single subscriber cannot consume N brokers).
	brokerCells := make(map[string]string, len(generatedPostgresCells()))
	for _, cellID := range generatedPostgresCells() {
		brokerCells[cellID] = LoadBrokerURL(strings.ToUpper(cellID))
	}
	transport, err := eventtransport.Resolve(clk, topo, eventtransport.Config{Cells: brokerCells})
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

	warnLegacyHTTPAddr()

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
	// #2263: resolve split-topology cross-cell mTLS material (client identity for
	// dialing remote peers + server config for the internal listener). Fails
	// closed when the deployment topology has a non-loopback remote cell but no
	// TLS material is provisioned (see cellmodules/celltls).
	// generatedTopologyGroups() is the assembly's complete deployment-partition
	// graph. SpecForRole derives THIS process's placement from GOCELL_CELL_ROLE
	// (#2278): an empty role with 0/1 group selects the all-colocated monolith
	// (zero spec); a declared role selects its colocated cells + the other groups
	// as remote; an empty role with ≥2 groups, or an unknown role, is fail-closed
	// — so a split-deployment misconfiguration is rejected at startup, never
	// silently run as a monolith (12-factor: env is consumed or it errors). The
	// spec flows into SharedDeps.DeploymentTopology (consumed by celltransport +
	// composition.NewForRole's subset mount).
	deployTopoSpec, err := bootstrap.SpecForRole(generatedTopologyGroups(), os.Getenv("GOCELL_CELL_ROLE"))
	if err != nil {
		return nil, nil, err
	}
	celltlsDeps, err := resolveTransportTLSMaterial(deployTopoSpec)
	if err != nil {
		return nil, nil, err
	}

	compShared, err := composition.NewSharedDeps(composition.SharedDeps{
		Clock:                     clk,
		Topology:                  topo,
		DeploymentTopology:        deployTopoSpec,
		JWTIssuer:                 jwt.issuer,
		JWTVerifier:               jwt.verifier,
		MetricsProvider:           metricsDeps.PromStack.metricProvider,
		Publisher:                 transport.Publisher,
		Subscriber:                transport.Subscriber,
		ConfigEventCollector:      metricsDeps.ConfigEventCollector,
		ConsumerClaimer:           replay.ConsumerClaimer,
		InternalServiceKeyring:    internalKeyring,
		NonceStore:                replay.NonceStore,
		RemoteClientTLS:           celltlsDeps.ClientIdentity,
		InternalListenerServerTLS: celltlsDeps.ServerTLS,
		PrimaryHTTPAddr:           primaryAddr,
		InternalHTTPAddr:          internalAddr,
		HealthHTTPAddr:            healthAddr,
		HealthLocalOnly:           healthLocalOnly,
		MetricsToken:              metricsToken,
		VerboseToken:              verboseToken,
		VerboseDisabled:           verboseDisabled,
		ProjectRoot:               os.Getenv("GOCELL_PROJECT_ROOT"),
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

	// Deployment-role placement: lets an operator confirm, per process, which
	// cells this process hosts (colocated) vs reaches remotely. The colocated set
	// IS the selected role's footprint; we log the DERIVED spec (validated by
	// SpecForRole) rather than the raw GOCELL_CELL_ROLE env to avoid log-injection
	// taint (gosec G706). `split` keys on a real cross-process boundary (≥1 remote
	// cell) — a single-group role is colocated-only, NOT a split. remote_cell_endpoints
	// gives the full cellID→endpoint placement (sorted) so operators can audit who
	// each remote peer is, not just the count (#2278 review F3/F4).
	remoteCellEndpoints := make([]string, 0, len(deployTopoSpec.Remote))
	for _, r := range deployTopoSpec.Remote {
		remoteCellEndpoints = append(remoteCellEndpoints, r.CellID+"="+r.Endpoint)
	}
	sort.Strings(remoteCellEndpoints)
	slog.Info("corebundle: deployment role",
		slog.Bool("split", len(deployTopoSpec.Remote) > 0),
		slog.Any("colocated_cells", deployTopoSpec.Colocated),
		slog.Int("remote_cells", len(deployTopoSpec.Remote)),
		slog.Any("remote_cell_endpoints", remoteCellEndpoints))

	loaded = true
	return compShared, locals, nil
}

// warnLegacyHTTPAddr surfaces the pre-PR-A14a GOCELL_HTTP_ADDR rename so an
// operator upgrading from a single-listener binary sees a clear signal when only
// the old var is set. Extracted to keep LoadSharedDepsFromEnv ≤ gocognit 15.
func warnLegacyHTTPAddr() {
	legacy := os.Getenv("GOCELL_HTTP_ADDR")
	if legacy == "" {
		return
	}
	if os.Getenv("GOCELL_HTTP_PRIMARY_ADDR") != "" || os.Getenv("GOCELL_HTTP_INTERNAL_ADDR") != "" {
		return
	}
	slog.Warn("GOCELL_HTTP_ADDR is no longer consumed (PR-A14a dual-listener);"+
		" set GOCELL_HTTP_PRIMARY_ADDR and GOCELL_HTTP_INTERNAL_ADDR instead",
		slog.String("legacy_value", strings.ReplaceAll(legacy, "\n", "")))
}

// resolveTransportTLSMaterial resolves split-topology cross-cell mTLS material
// (client identity + internal-listener server config) from the deployment
// topology spec + env. Fails closed when a non-loopback remote peer is declared
// but no TLS material is provisioned (see cellmodules/celltls). Extracted to keep
// LoadSharedDepsFromEnv ≤ gocognit 15.
func resolveTransportTLSMaterial(spec bootstrap.DeploymentTopologySpec) (celltls.Deps, error) {
	topo, err := bootstrap.NewDeploymentTopology(spec)
	if err != nil {
		return celltls.Deps{}, err
	}
	return celltls.Resolve(topo, celltls.LoadConfigFromEnv())
}
