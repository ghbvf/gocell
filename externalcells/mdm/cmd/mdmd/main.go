// Command mdmd is the hand-written composition root for the external MDM cell
// module (Operator-SDK mode, ADR 202605281200). It dogfoods the public
// runtime/composition API — composition.New(...).With(...).Build(...) — assembling
// the enrollcell in the demo / in-memory topology with zero external infrastructure,
// serving green /healthz + /readyz + the http.deviceidentity.status.v1 endpoint.
//
// PR-1 wiring:
//   - certdeps.Resolve proves the cert-signing bottom layer is live (softca demo CA).
//   - status/mem.New provides the in-memory CertRecord repository.
//   - enrollcell.NewModule(Deps) wires the status repo into the cell.
//   - bootstrap.WithFrameworkHTTPServing mounts http.deviceidentity.status.v1.
//   - bootstrap.PrimaryAuthorizerOption discovers enrollcell.Authorizer() (enrollAuthorizer).
//
// AUTH-PLAN-04: auth-plan construction (NewAuthJWT / NewAuthServiceToken) lives HERE
// in the composition root, never inside runtime/composition or a cell.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	enrollcell "github.com/ghbvf/gocell-mdm/cells/enrollcell"
	"github.com/ghbvf/gocell-mdm/cells/enrollcell/slices/status"
	statusmem "github.com/ghbvf/gocell-mdm/cells/enrollcell/slices/status/mem"
	"github.com/ghbvf/gocell/cellmodules/certdeps"
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

// devServiceSecret backs the internal listener's service-token HMAC ring in the
// demo topology. Development / demo use only — the startup banner Warn flags it,
// and MDM-PR15 (postgres topology) replaces it with an env-sourced per-cell secret.
//
// #nosec G101 -- demo fixture in externalcells/mdm; not a production credential.
const devServiceSecret = "mdm-dev-service-secret-32-bytes!!"

const (
	devJWTIssuer      = "mdm-dev"
	devJWTAudience    = "mdm"
	devAccessTokenTTL = 15 * time.Minute
)

// demoOptInEnv gates the demo daemon. The MDM enrollment daemon is a demo/in-memory
// skeleton (ephemeral JWT keys, a hardcoded service secret, no durable store) and so
// refuses to start unless this env var is "1" — a fail-closed opt-in, not a warn
// banner. The postgres topology (MDM-PR15) replaces the demo deps and drops the gate.
// See run.
const demoOptInEnv = "MDMD_DEMO"

// listenerAddrs are the three listener bind addresses. They are a parameter (not
// hard-coded inside the builder) so the startup smoke test and run can inject
// addresses. In the demo all three bind loopback (the daemon is for local
// validation, behind the MDMD_DEMO opt-in); MDM-PR15's postgres topology sets the
// real device-facing primary bind per docs/ops/listener-topology.md.
type listenerAddrs struct {
	primary  string
	internal string
	health   string
}

// prebuiltListeners optionally carries pre-bound net.Listeners for each listener.
// Production leaves all nil (bootstrap binds the addr itself). The startup smoke
// test injects already-bound loopback listeners so bootstrap adopts them via
// WithListenerNet — eliminating the listen→close→rebind TOCTOU window that a
// "reserve a free port then hand back the string" helper would leave open. Once
// passed to Build, bootstrap owns each listener and closes it on shutdown.
type prebuiltListeners struct {
	primary  net.Listener
	internal net.Listener
	health   net.Listener
}

// defaultAddrs are the demo bind addresses (non-default ports to avoid colliding
// with corebundle / other examples). All three bind loopback: the demo daemon is for
// local validation only, so even behind the MDMD_DEMO opt-in it is never exposed on
// an external interface. MDM-PR15 sets the real device-facing primary bind.
func defaultAddrs() listenerAddrs {
	return listenerAddrs{
		primary:  "127.0.0.1:8085",
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

	if err := run(ctx, defaultAddrs(), prebuiltListeners{}); err != nil {
		slog.Error("mdmd: application failed", slog.Any("error", err))
		os.Exit(1)
	}
}

// run gates the demo daemon behind an explicit opt-in and, when allowed, builds and
// serves the app on addrs.
func run(ctx context.Context, addrs listenerAddrs, lns prebuiltListeners) error {
	if os.Getenv(demoOptInEnv) != "1" {
		return fmt.Errorf("mdmd: refusing to start: demo/in-memory skeleton (NOT production-safe); set %s=1 to run the demo", demoOptInEnv)
	}
	app, err := buildApp(ctx, addrs, lns)
	if err != nil {
		return fmt.Errorf("mdmd: build app: %w", err)
	}
	slog.Info("mdmd: starting",
		slog.String("primary", addrs.primary),
		slog.String("internal", addrs.internal),
		slog.String("health", addrs.health),
		slog.String("status_endpoint", "GET "+addrs.primary+"/api/v1/deviceidentity/status?deviceId=<id>"),
		slog.String("auth", "JWT Bearer; roles: mdm-admin or mdm-operator for device:read"),
	)
	return app.Run(ctx)
}

// buildApp assembles the demo-topology app: memory SharedDeps + enrollcell with
// status slice + framework-serving harness.
//
// PR-1 cert-wiring:
//   - certdeps.Resolve proves the cert-signing bottom layer is live.
//   - proveCertBaseLive smoke-validates the resolved Signer (non-nil TrustBundle).
//   - The Signer/RevStore are NOT injected into enrollcell (PR-1 only reads status;
//     enroll/revoke injection lands in PR-2 when those paths are consumed).
func buildApp(ctx context.Context, addrs listenerAddrs, lns prebuiltListeners) (*composition.App, error) {
	shared, err := buildMemSharedDeps(addrs)
	if err != nil {
		return nil, fmt.Errorf("build shared deps: %w", err)
	}
	return buildAppFromShared(ctx, shared, lns)
}

// buildAppFromShared assembles the app from a pre-built SharedDeps. Extracted so
// the startup smoke test can inject the same SharedDeps (and hence the same JWT
// key pair) that the running server uses — enabling integration tests to issue
// valid tokens without a separate key-exchange mechanism.
func buildAppFromShared(ctx context.Context, shared *composition.SharedDeps, lns prebuiltListeners) (*composition.App, error) {
	// Prove the cert-signing bottom layer is live (CERTDEPS smoke, epic §0).
	// demo topology → softca dev CA (ephemeral trust anchor, in-memory ledger).
	// postgres topology → fail-closed (durable CA not yet wired, by design).
	cd, err := certdeps.Resolve(shared.Clock, shared.Topology)
	if err != nil {
		return nil, fmt.Errorf("certdeps: %w", err)
	}
	if err := proveCertBaseLive(ctx, cd); err != nil {
		return nil, fmt.Errorf("certdeps smoke: %w", err)
	}

	// Build the status in-memory repository and service.
	repo := statusmem.New(shared.Clock)
	statusSvc := status.NewService(repo, shared.Clock)

	// Build the cell module with status repo injected.
	m := enrollcell.NewModule(enrollcell.Deps{StatusRepo: repo})

	app, err := composition.New(m.ID()).With(m).Build(ctx, shared,
		runtimeOptions(shared, lns, statusSvc.FrameworkRoute()))
	if err != nil {
		return nil, fmt.Errorf("composition build: %w", err)
	}
	return app, nil
}

// proveCertBaseLive smoke-validates the resolved CertDeps by calling
// TrustBundle: a non-empty bundle confirms the dev CA is initialized and the
// cert-signing bottom layer is truly live (not a stub). This prevents the
// certdeps.Resolve call in buildApp from being dead code.
func proveCertBaseLive(ctx context.Context, cd certdeps.CertDeps) error {
	bundle, err := cd.Signer.TrustBundle(ctx)
	if err != nil {
		return fmt.Errorf("TrustBundle: %w", err)
	}
	if len(bundle) == 0 {
		return fmt.Errorf("cert base live: TrustBundle returned empty bundle (dev CA not initialized)")
	}
	slog.Debug("mdmd: cert base live", slog.Int("trust_bundle_certs", len(bundle)))
	return nil
}

// mustServeFrameworkContracts returns the must-serve set of framework-owned contracts
// for this composition root. It is the hand-authored single source of the
// FrameworkContracts set passed to assembly.Config; the drift test in
// cmd/mdmd/framework_serving_test.go cross-checks this against the wired
// FrameworkServedRoute ContractIDs (Medium guard, ADR-1939 §AI-robust).
//
// Hard-ization path: replace this with a codegen-derived function (M2, epic #2299).
func mustServeFrameworkContracts() []string {
	return []string{"http.deviceidentity.status.v1"}
}

// buildMemSharedDeps constructs a fully-populated SharedDeps in dev/memory mode.
//
// TOPOLOGY INVARIANT (MDM-PR15 hardening): every dependency below is the demo /
// in-memory / single-pod variant. When this module moves to the postgres topology,
// the Publisher/Subscriber, ConsumerClaimer, and NonceStore MUST be re-resolved via
// the topology-gated resolvers (cellmodules/eventtransport.Resolve +
// replaydeps.Resolve) and devServiceSecret via env.
func buildMemSharedDeps(addrs listenerAddrs) (*composition.SharedDeps, error) {
	clk := clock.Real()

	topo, err := bootstrap.NewTopology("", "memory", false)
	if err != nil {
		return nil, fmt.Errorf("topology: %w", err)
	}

	slog.Warn("mdmd: demo/in-memory topology — NOT production-safe",
		slog.String("jwt", "ephemeral RSA keys; tokens invalid on restart"),
		slog.String("jwt_issuer", devJWTIssuer),
		slog.String("jwt_audience", devJWTAudience),
		slog.String("service_secret", "hardcoded demo secret; production must source per-cell secret from env"),
		slog.String("metrics", "NopProvider (metrics disabled)"),
		slog.String("status", "GET /api/v1/deviceidentity/status?deviceId=<id> requires JWT with mdm-admin or mdm-operator role"),
	)

	eb := eventbus.New(clk)

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
		// VerboseDisabled waives /readyz?verbose. In demo mode SharedDeps.validate
		// only requires "token-gated OR disabled"; in the postgres topology
		// (RequireProductionControlPlane) VerboseDisabled is REJECTED — MDM-PR15 must
		// then set VerboseToken instead of deleting this field.
		VerboseDisabled: true,
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

// netListenerOpt returns a WithListenerNet option for a pre-bound listener, or no
// options when ln is nil (production: bootstrap binds the addr itself).
func netListenerOpt(ln net.Listener) []bootstrap.ListenerOption {
	if ln == nil {
		return nil
	}
	return []bootstrap.ListenerOption{bootstrap.WithListenerNet(ln)}
}

// runtimeOptions builds the bootstrap options: assembly + three listeners +
// framework-serving + primary authorizer.
func runtimeOptions(
	shared *composition.SharedDeps,
	lns prebuiltListeners,
	fwRoute bootstrap.FrameworkServedRoute,
) composition.RuntimeOptionsFunc {
	return func(cells []cell.Cell) ([]bootstrap.Option, error) {
		asm := assembly.New(shared.Clock, assembly.Config{
			ID:                 "mdm",
			DurabilityMode:     outbox.DurabilityDemo,
			FrameworkContracts: mustServeFrameworkContracts(),
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

		// PrimaryAuthorizerOption discovers enrollcell.Authorizer() via duck-type
		// (authorizerProvider interface) and wraps it in WithPrimaryAuthorizer.
		// This wires the enrollAuthorizer PDP into every request context on the
		// primary listener so auth.RequirePermission can call Authorize.
		pdpOpt, err := bootstrap.PrimaryAuthorizerOption(cells)
		if err != nil {
			return nil, fmt.Errorf("primary authorizer: %w", err)
		}

		return []bootstrap.Option{
			bootstrap.WithAssembly(asm),
			bootstrap.WithPublisher(shared.Publisher),
			bootstrap.WithSubscriber(shared.Subscriber),
			bootstrap.WithListener(cell.PrimaryListener, shared.PrimaryHTTPAddr,
				[]kauth.ListenerAuth{primaryAuth}, netListenerOpt(lns.primary)...),
			bootstrap.WithListener(cell.InternalListener, shared.InternalHTTPAddr,
				[]kauth.ListenerAuth{svcTokenAuth}, netListenerOpt(lns.internal)...),
			bootstrap.WithListener(cell.HealthListener, shared.HealthHTTPAddr,
				[]kauth.ListenerAuth{kauth.AuthNone{}}, netListenerOpt(lns.health)...),
			bootstrap.WithHealthRoutes(bootstrap.WithReadyzVerboseDisabled()),
			bootstrap.WithFrameworkHTTPServing([]bootstrap.FrameworkServedRoute{fwRoute}),
			pdpOpt,
		}, nil
	}
}
