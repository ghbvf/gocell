// run.go is the hand-written composition root for the corebundlestarter example.
// It demonstrates the public runtime/composition API: constructing a
// *composition.SharedDeps in dev/memory mode and assembling the three platform
// cells via cellmodules/<cell>.Module() — with zero external infrastructure.
//
// This is the M11 dogfood target for issue #1085
// (CellModule / SharedDeps / Builder / App public API).
//
// AUTH-PLAN-04: auth construction (NewAuthJWTFromAssembly, NewAuthServiceToken)
// lives HERE in examples/ — not inside runtime/composition or cellmodules/.
//
// ref: uber-go/fx fx.New — single assembly entry point used by both production
// and tests.
// ref: kubernetes-sigs/controller-runtime pkg/manager/manager.go — Manager
// accumulates options and starts via Start(ctx) error.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	cellmodulesaccesscore "github.com/ghbvf/gocell/cellmodules/accesscore"
	cellmodulesauditcore "github.com/ghbvf/gocell/cellmodules/auditcore"
	cellmodulesconfigcore "github.com/ghbvf/gocell/cellmodules/configcore"
	"github.com/ghbvf/gocell/cellmodules/grpclistener"
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
	"github.com/ghbvf/gocell/framework/runtime/grpc/interceptor"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

// devServiceSecret is a hard-coded demo secret for the internal listener's
// service-token guard. Development / example use only.
//
// #nosec G101 -- demo fixture in examples/corebundlestarter; production is env-driven.
const devServiceSecret = "starter-dev-secret-32-bytes-ok!!"

// devJWTIssuer and devJWTAudience are the default dev JWT claims used when
// no env vars override them.
const (
	devJWTIssuer   = "starter-dev"
	devJWTAudience = "starter"
)

// devAccessTokenTTL is the JWT access-token TTL for this example.
const devAccessTokenTTL = 15 * time.Minute

// starterPrimaryAddr / InternalAddr / HealthAddr are the default listener
// bind addresses.  Pick non-default ports to avoid conflicts with corebundle.
// Only the primary listener is all-interfaces; the internal cell→cell control
// plane binds loopback per docs/ops/listener-topology.md.
const (
	starterPrimaryAddr  = ":8088"
	starterInternalAddr = "127.0.0.1:9088"
	starterHealthAddr   = "127.0.0.1:9098"
)

// runStarter is the composition root: builds memory-mode SharedDeps, assembles
// the three platform cells, and starts the bootstrap lifecycle.
func runStarter(ctx context.Context) error {
	shared, err := buildStarterMemSharedDeps(ctx)
	if err != nil {
		return fmt.Errorf("corebundlestarter: build shared deps: %w", err)
	}

	// composition.New().With(...).Build(...) is the public API under test (#1085).
	// Module order mirrors corebundle assembly.yaml cell order purely for
	// convention/determinism — it is NOT runtime-significant. The former
	// auditcore→accesscore BootstrapLedgerStore handoff was removed in #1423
	// (bootstrap auth-fail is now event-driven via event.auth.bootstrap-failed.v1),
	// so module Provide order carries no cross-cell dependency.
	// composition.New(<cell ids>) seals the assembly's cell-id closed set (M12a
	// #1093): Build fail-fasts if the composed modules drift from this declared
	// set. corebundlestarter mirrors the corebundle cell set.
	app, err := composition.New("auditcore", "accesscore", "configcore").
		With(
			cellmodulesauditcore.Module(),
			cellmodulesaccesscore.Module(),
			cellmodulesconfigcore.Module(),
		).
		Build(ctx, shared, starterRuntimeOptions(shared))
	if err != nil {
		return fmt.Errorf("corebundlestarter: Build: %w", err)
	}

	slog.Info("corebundlestarter: starting",
		slog.String("primary", shared.PrimaryHTTPAddr),
		slog.String("internal", shared.InternalHTTPAddr),
		slog.String("health", shared.HealthHTTPAddr),
	)
	return app.Run(ctx)
}

// buildStarterMemSharedDeps constructs a fully-populated *composition.SharedDeps
// in dev/memory mode: no postgres, no redis, ephemeral in-process JWT keys.
func buildStarterMemSharedDeps(_ context.Context) (*composition.SharedDeps, error) {
	clk := clock.Real()

	// Topology: dev adapter mode (empty string), in-memory storage backend.
	topo, err := bootstrap.NewTopology("", "memory", false)
	if err != nil {
		return nil, fmt.Errorf("topology: %w", err)
	}

	eb := eventbus.New(clk)

	// JWT: ephemeral in-process RSA key pair (tokens invalidated on restart).
	slog.Warn("corebundlestarter: generating ephemeral JWT keys — tokens invalid on restart")
	jwtIssuer, jwtVerifier, err := buildStarterJWT(clk)
	if err != nil {
		return nil, fmt.Errorf("JWT deps: %w", err)
	}

	// MetricsProvider: kernel NopProvider — avoids prometheus import.
	mp := kernelmetrics.NopProvider{}

	// Noop collector satisfies SharedDeps.Validate without prometheus.
	cfgEventCollector := obmetrics.NoopConfigEventCollector{}

	// ConsumerClaimer: in-memory idempotency claimer (single-process only).
	claimer := idempotency.NewInMemClaimer(clk)

	// InternalServiceKeyring: dev master-derived keyring for /internal/v1/* service
	// tokens (monolith; per-cell subkeys derived in-process via HKDF, #2153).
	ring, err := auth.NewHMACKeyRing([]byte(devServiceSecret), nil)
	if err != nil {
		return nil, fmt.Errorf("HMAC key ring: %w", err)
	}

	// NonceStore: in-memory replay-defense store for /internal/v1/* service
	// tokens. It lives on SharedDeps (alongside InternalServiceKeyring) so the same
	// validated instance backs the internal-listener auth plan in
	// starterRuntimeOptions — the runtime store and the control-plane-validated
	// store are one, not two. Building a separate store in the RuntimeOptionsFunc
	// would decouple what validate() introspects from what actually guards the
	// listener (mirrors cmd/corebundle; #1410 review F5).
	nonceStore, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clk)
	if err != nil {
		return nil, fmt.Errorf("nonce store: %w", err)
	}

	shared, err := composition.NewSharedDeps(composition.SharedDeps{
		Clock:                  clk,
		Topology:               topo,
		JWTIssuer:              jwtIssuer,
		JWTVerifier:            jwtVerifier,
		MetricsProvider:        mp,
		Publisher:              eb,
		Subscriber:             eb,
		ConfigEventCollector:   cfgEventCollector,
		ConsumerClaimer:        claimer,
		InternalServiceKeyring: ring,
		NonceStore:             nonceStore,
		PrimaryHTTPAddr:        starterPrimaryAddr,
		InternalHTTPAddr:       starterInternalAddr,
		HealthHTTPAddr:         starterHealthAddr,
		VerboseDisabled:        true,
		// EventbusCacheCollector / ConfigStaleCipherInc removed (#1413): configcore
		// self-builds them from MetricsProvider (NopProvider here).
		// PG / Redis are nil → all platform modules take the in-memory path.
	})
	if err != nil {
		return nil, fmt.Errorf("composition.NewSharedDeps: %w", err)
	}
	return shared, nil
}

// buildStarterJWT creates an ephemeral JWT issuer and verifier backed by a
// freshly generated RSA key pair.
func buildStarterJWT(clk clock.Clock) (*auth.JWTIssuer, *auth.JWTVerifier, error) {
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

// starterRuntimeOptions returns the composition.RuntimeOptionsFunc that builds
// the assembly and the three HTTP listeners with appropriate auth plans.
//
// AUTH-PLAN-04 allows examples/ to construct auth plans.
func starterRuntimeOptions(shared *composition.SharedDeps) composition.RuntimeOptionsFunc {
	return func(cells []cell.Cell) ([]bootstrap.Option, error) {
		return buildStarterBootstrapOpts(shared, cells)
	}
}

// buildStarterBootstrapOpts constructs the ordered bootstrap.Option slice.
// Extracted from the closure to keep cognitive complexity within limit.
func buildStarterBootstrapOpts(
	shared *composition.SharedDeps,
	cells []cell.Cell,
) ([]bootstrap.Option, error) {
	// Assembly: memory mode → DurabilityDemo.
	asm, err := buildStarterAssembly(shared.Clock, cells)
	if err != nil {
		return nil, fmt.Errorf("build assembly: %w", err)
	}

	// ConsumerBase: memory idempotency claimer.
	cb, err := outbox.NewConsumerBase(shared.ConsumerClaimer, outbox.ConsumerBaseConfig{}, shared.Clock)
	if err != nil {
		return nil, fmt.Errorf("consumer base: %w", err)
	}

	// Primary listener: JWT auth (phase4 discovers verifier from accesscore cell).
	primaryAuth, err := kauth.NewAuthJWTFromAssembly(asm)
	if err != nil {
		return nil, fmt.Errorf("primary listener auth: %w", err)
	}

	// Internal listener: HMAC service token reusing the SharedDeps nonce store
	// (the same validated instance built in buildStarterMemSharedDeps, not a fresh
	// one) so the runtime auth plan and the control-plane-validated store cannot
	// diverge (#1410 review F5).
	svcTokenAuth, err := kauth.NewAuthServiceToken(shared.NonceStore, shared.InternalServiceKeyring)
	if err != nil {
		return nil, fmt.Errorf("internal listener auth: %w", err)
	}

	opts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(shared.Publisher),
		bootstrap.WithSubscriber(shared.Subscriber),
		bootstrap.WithConsumerBase(cb),
		bootstrap.WithListener(cell.PrimaryListener, shared.PrimaryHTTPAddr,
			[]kauth.ListenerAuth{primaryAuth}),
		bootstrap.WithListener(cell.InternalListener, shared.InternalHTTPAddr,
			[]kauth.ListenerAuth{svcTokenAuth}),
		bootstrap.WithListener(cell.HealthListener, shared.HealthHTTPAddr,
			[]kauth.ListenerAuth{kauth.AuthNone{}}),
		bootstrap.WithHealthRoutes(bootstrap.WithReadyzVerboseDisabled()),
	}

	// Wire the ABAC PDP (accesscore provides it). Any assembly serving the
	// auditquery endpoint must wire it: post #1348 PR-10a the route gate is
	// permission-based, so an empty-actorId/cross-actor audit read fails closed
	// without a PDP in context. One lazy authorizer feeds BOTH the HTTP primary
	// listener and the gRPC gate (shared discovery in bootstrap.AuthorizerFromCells,
	// also used by cmd/corebundle, ssobff).
	authorizer, err := bootstrap.AuthorizerFromCells(cells)
	if err != nil {
		return nil, fmt.Errorf("primary authorizer wiring: %w", err)
	}
	opts = append(opts, bootstrap.WithPrimaryAuthorizer(authorizer))

	// gRPC listener: mandatory because accesscore registers
	// grpc.auth.session.verify.v1 unconditionally (cell_gen.go, PR-11 #1154); without
	// a gRPC listener bootstrap fail-fasts (checkOrphanGRPCServices). Demo topology
	// → plaintext; shared env-driven builder lives in cellmodules/grpclistener.
	grpcCollector, err := obmetrics.NewGRPCProviderCollector(shared.MetricsProvider, obmetrics.ProviderCollectorConfig{})
	if err != nil {
		return nil, fmt.Errorf("grpc metrics collector: %w", err)
	}
	grpcAddr := grpclistener.AddrFromEnv()
	grpcServer, err := grpclistener.ServerFromEnv(outbox.DurabilityDemo, grpcAddr, interceptor.Deps{
		Verifier:        shared.JWTVerifier,
		Clock:           shared.Clock,
		Collector:       grpcCollector,
		Authorizer:      authorizer,
		MetricsProvider: shared.MetricsProvider,
		CellIDClosedSet: asm.CellIDs(),
	})
	if err != nil {
		return nil, fmt.Errorf("grpc server: %w", err)
	}
	opts = append(opts, bootstrap.WithGRPCListener(cell.PrimaryListener, grpcServer, grpcAddr))
	return opts, nil
}

// buildStarterAssembly creates a CoreAssembly with DurabilityDemo (memory mode).
func buildStarterAssembly(clk clock.Clock, cells []cell.Cell) (*assembly.CoreAssembly, error) {
	asm := assembly.New(clk, assembly.Config{
		ID:             "corebundlestarter",
		DurabilityMode: outbox.DurabilityDemo,
	})
	for _, c := range cells {
		if err := asm.Register(c); err != nil {
			return nil, fmt.Errorf("register %s: %w", c.ID(), err)
		}
	}
	return asm, nil
}
