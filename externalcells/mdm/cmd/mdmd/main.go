// Command mdmd is the hand-written composition root for the external MDM cell
// module (Operator-SDK mode, ADR 202605281200). It dogfoods the public
// runtime/composition API — composition.New(...).With(...).Build(...) — assembling
// the (PR-0 empty) enrollcell in the demo / in-memory topology with zero external
// infrastructure, serving green /healthz + /readyz.
//
// This module is NOT in the gocell root go.work; it consumes gocell via go.mod
// replace directives, so all commands run with GOWORK=off (the in-repo cost of
// living outside the root workspace; gone once #1722 splits it into its own repo).
//
// AUTH-PLAN-04: auth-plan construction (NewAuthJWT / NewAuthServiceToken) lives HERE
// in the composition root, never inside runtime/composition or a cell.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	enrollcell "github.com/ghbvf/gocell-mdm/cells/enrollcell"
	"github.com/ghbvf/gocell/framework/kernel/assembly"
	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/idempotency"
	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/composition"
	"github.com/ghbvf/gocell/framework/runtime/eventbus"
	"github.com/ghbvf/gocell/framework/runtime/observability/logging"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
	"github.com/ghbvf/gocell/framework/runtime/shutdown"
)

// devServiceSecret backs the internal listener's service-token HMAC ring.
// Development / demo use only; production is env-driven.
//
// #nosec G101 -- demo fixture in externalcells/mdm; not a production credential.
const devServiceSecret = "mdm-dev-service-secret-32-bytes!!"

const (
	devJWTIssuer      = "mdm-dev"
	devJWTAudience    = "mdm"
	devAccessTokenTTL = 15 * time.Minute
)

// listenerAddrs are the three listener bind addresses. They are a parameter (not
// hard-coded inside the builder) so the startup smoke test can inject OS-assigned
// free loopback ports. Only the primary listener is all-interfaces; internal and
// health bind loopback per docs/ops/listener-topology.md.
type listenerAddrs struct {
	primary  string
	internal string
	health   string
}

// defaultAddrs are the production demo bind addresses (non-default ports to avoid
// colliding with corebundle / other examples).
func defaultAddrs() listenerAddrs {
	return listenerAddrs{
		primary:  ":8085",
		internal: "127.0.0.1:9085",
		health:   "127.0.0.1:9095",
	}
}

func main() {
	// Seal the process-global slog default with the redacting handler FIRST, before
	// any work that may log, so every slog.Default() call is scrubbed.
	slog.SetDefault(slog.New(logging.NewHandler(logging.Options{Format: logging.FormatJSON})))

	ctx, cancel := shutdown.NotifyContext(context.Background())
	defer cancel()

	if err := run(ctx); err != nil {
		slog.Error("mdmd: application failed", slog.Any("error", err))
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	addrs := defaultAddrs()
	app, err := buildApp(ctx, addrs)
	if err != nil {
		return fmt.Errorf("mdmd: build app: %w", err)
	}
	slog.Info("mdmd: starting",
		slog.String("primary", addrs.primary),
		slog.String("internal", addrs.internal),
		slog.String("health", addrs.health),
	)
	return app.Run(ctx)
}

// buildApp assembles the demo-topology app: memory SharedDeps + the empty enrollcell
// via the public composition API. Extracted from run so the startup smoke test can
// drive it with injected loopback ports.
func buildApp(ctx context.Context, addrs listenerAddrs) (*composition.App, error) {
	shared, err := buildMemSharedDeps(addrs)
	if err != nil {
		return nil, fmt.Errorf("build shared deps: %w", err)
	}
	m := enrollcell.Module()
	app, err := composition.New(m.ID()).With(m).Build(ctx, shared, runtimeOptions(shared))
	if err != nil {
		return nil, fmt.Errorf("composition build: %w", err)
	}
	return app, nil
}

// buildMemSharedDeps constructs a fully-populated SharedDeps in dev/memory mode: no
// postgres, no redis, ephemeral in-process JWT keys. Every field SharedDeps.validate
// requires in demo mode is populated even though the empty cell uses little of it.
func buildMemSharedDeps(addrs listenerAddrs) (*composition.SharedDeps, error) {
	clk := clock.Real()

	topo, err := bootstrap.NewTopology("", "memory", false)
	if err != nil {
		return nil, fmt.Errorf("topology: %w", err)
	}

	eb := eventbus.New(clk)

	slog.Warn("mdmd: generating ephemeral JWT keys — tokens invalid on restart")
	jwtIssuer, jwtVerifier, err := buildDevJWT(clk)
	if err != nil {
		return nil, fmt.Errorf("JWT deps: %w", err)
	}

	ring, err := auth.NewHMACKeyRing([]byte(devServiceSecret), nil)
	if err != nil {
		return nil, fmt.Errorf("HMAC key ring: %w", err)
	}
	nonceStore, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clk)
	if err != nil {
		return nil, fmt.Errorf("nonce store: %w", err)
	}

	shared, err := composition.NewSharedDeps(composition.SharedDeps{
		Clock:                  clk,
		Topology:               topo,
		JWTIssuer:              jwtIssuer,
		JWTVerifier:            jwtVerifier,
		MetricsProvider:        kernelmetrics.NopProvider{},
		Publisher:              eb,
		Subscriber:             eb,
		ConfigEventCollector:   obmetrics.NoopConfigEventCollector{},
		ConsumerClaimer:        idempotency.NewInMemClaimer(clk),
		InternalServiceKeyring: ring,
		NonceStore:             nonceStore,
		PrimaryHTTPAddr:        addrs.primary,
		InternalHTTPAddr:       addrs.internal,
		HealthHTTPAddr:         addrs.health,
		VerboseDisabled:        true,
		// PG / Redis nil → in-memory paths. Demo mode skips the real-adapter control
		// plane guards (MetricsToken / nonce-kind / health-reachability).
	})
	if err != nil {
		return nil, fmt.Errorf("composition.NewSharedDeps: %w", err)
	}
	return shared, nil
}

// buildDevJWT creates an ephemeral JWT issuer + verifier backed by a freshly
// generated RSA key pair (tokens invalidated on restart).
func buildDevJWT(clk clock.Clock) (*auth.JWTIssuer, *auth.JWTVerifier, error) {
	privKey, pubKey, err := auth.GenerateRSAKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("generate RSA key pair: %w", err)
	}
	keySet, err := auth.NewKeySet(privKey, pubKey, clk)
	if err != nil {
		return nil, nil, fmt.Errorf("create key set: %w", err)
	}
	issuer, err := auth.NewJWTIssuer(keySet, devJWTIssuer, devAccessTokenTTL, clk,
		auth.WithIssuerAudiencesFromSlice([]string{devJWTAudience}))
	if err != nil {
		return nil, nil, fmt.Errorf("create JWT issuer: %w", err)
	}
	verifier, err := auth.NewJWTVerifier(keySet, clk,
		auth.WithExpectedAudiences(devJWTAudience),
		auth.WithExpectedIssuer(devJWTIssuer))
	if err != nil {
		return nil, nil, fmt.Errorf("create JWT verifier: %w", err)
	}
	return issuer, verifier, nil
}

// runtimeOptions builds the bootstrap options: assembly + three listeners. An empty
// cell needs no gRPC listener (no gRPC service), no PrimaryAuthorizer (no endpoints /
// PDP), and no ConsumerBase (no subscribers) — only what makes health/readyz serve.
func runtimeOptions(shared *composition.SharedDeps) composition.RuntimeOptionsFunc {
	return func(cells []cell.Cell) ([]bootstrap.Option, error) {
		asm := assembly.New(shared.Clock, assembly.Config{
			ID:             "mdm",
			DurabilityMode: outbox.DurabilityDemo,
		})
		for _, c := range cells {
			if err := asm.Register(c); err != nil {
				return nil, fmt.Errorf("register %s: %w", c.ID(), err)
			}
		}

		// Primary listener: JWT. mdm has no accesscore providing the verifier via the
		// assembly, so build the plan directly from the SharedDeps verifier (the
		// standalone-cell pattern, cf. examples/iotdevice) rather than NewAuthJWTFromAssembly.
		primaryAuth, err := kauth.NewAuthJWT(shared.JWTVerifier)
		if err != nil {
			return nil, fmt.Errorf("primary listener auth: %w", err)
		}
		// Internal listener: HMAC service token reusing the SharedDeps-validated
		// nonce store + keyring (one instance, not a fresh one).
		svcTokenAuth, err := kauth.NewAuthServiceToken(shared.NonceStore, shared.InternalServiceKeyring)
		if err != nil {
			return nil, fmt.Errorf("internal listener auth: %w", err)
		}

		return []bootstrap.Option{
			bootstrap.WithAssembly(asm),
			bootstrap.WithPublisher(shared.Publisher),
			bootstrap.WithSubscriber(shared.Subscriber),
			bootstrap.WithListener(cell.PrimaryListener, shared.PrimaryHTTPAddr,
				[]kauth.ListenerAuth{primaryAuth}),
			bootstrap.WithListener(cell.InternalListener, shared.InternalHTTPAddr,
				[]kauth.ListenerAuth{svcTokenAuth}),
			bootstrap.WithListener(cell.HealthListener, shared.HealthHTTPAddr,
				[]kauth.ListenerAuth{kauth.AuthNone{}}),
			bootstrap.WithHealthRoutes(bootstrap.WithReadyzVerboseDisabled()),
		}, nil
	}
}
