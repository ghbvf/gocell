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
	"time"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/adapters/ratelimit"
	cellsecrets "github.com/ghbvf/gocell/cellmodules/cellsecrets"
	celltls "github.com/ghbvf/gocell/cellmodules/celltls"
	eventtransport "github.com/ghbvf/gocell/cellmodules/eventtransport"
	grpclistener "github.com/ghbvf/gocell/cellmodules/grpclistener"
	replaydeps "github.com/ghbvf/gocell/cellmodules/replaydeps"
	accesscore "github.com/ghbvf/gocell/corecells/accesscore"
	accesspg "github.com/ghbvf/gocell/corecells/accesscore/postgres"
	auditcore "github.com/ghbvf/gocell/corecells/auditcore"
	configcore "github.com/ghbvf/gocell/corecells/configcore"
	configpg "github.com/ghbvf/gocell/corecells/configcore/postgres"
	"github.com/ghbvf/gocell/framework/kernel/assembly"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/idempotency"
	"github.com/ghbvf/gocell/framework/kernel/lifecycle"
	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/ctxutil"
	"github.com/ghbvf/gocell/framework/pkg/migration"
	"github.com/ghbvf/gocell/framework/pkg/redaction"
	"github.com/ghbvf/gocell/framework/runtime/audit"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/auth/session"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/grpc/interceptor"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
	outboxruntime "github.com/ghbvf/gocell/framework/runtime/outbox"
	"github.com/ghbvf/gocell/framework/runtime/state/cas"
)

// ssobffBootstrapRateLimitPerSec / Burst mirror cmd/corebundle defaults
// (5 req/min sustained, burst 10). The demo path keeps the same posture so
// integration tests can exercise the 429 path against the same parameters.
const (
	ssobffBootstrapRateLimitPerSec = 5.0 / 60.0
	ssobffBootstrapRateLimitBurst  = 10
)

// ssobffBootstrapUsername / Password are the demo operator credentials
// protecting POST /api/v1/access/setup/admin in the ssobff example.
//
// examples/ssobff is a demo binary, not platform code, so these constants
// are package-local and are reused by walkthrough_test.go (same package
// main) as the single source of truth for the demo Basic Auth header.
// Production deployments inject credentials via GOCELL_BOOTSTRAP_ADMIN_*
// env (see cellmodules/accesscore/module.go); the demo path never reads
// the env in order to keep `go run ./examples/ssobff` self-contained.
const (
	ssobffBootstrapUsername = "ssobff-ops"
	// #nosec G101 -- demo fixture in examples/ssobff binary; production is env-driven (cmd/corebundle).
	ssobffBootstrapPassword = "ssobff-bootstrap-pass-1!"
)

// ssobffJWTIssuer / ssobffJWTAudience are the fixed JWT issuer and audience for
// the ssobff example. They are package consts (not inline literals) so the issuer
// and verifier wiring in newSSOBFFJWT cannot drift between callsites — a mismatch
// would 401 every token, with no compile error. They are identical across
// replicas, so (unlike the signing key, #2052) they were never the multi-pod
// hazard. ssobff is a self-contained JWT domain: unlike cmd/corebundle (which
// reads GOCELL_JWT_ISSUER / GOCELL_JWT_AUDIENCE), the demo BFF is not federated
// with the platform binary, so a fixed issuer is correct even in real multi-pod
// mode.
const (
	ssobffJWTIssuer   = "ssobff-dev"
	ssobffJWTAudience = "gocell"
)

// ssobffBootstrapAdminUserEnv / ssobffBootstrapAdminPassEnv name the env vars
// that supply the setup/admin Basic Auth credentials in real topology. Reuses
// the cmd/corebundle + cellmodules/accesscore convention
// (GOCELL_BOOTSTRAP_ADMIN_*) rather than an ssobff-specific name, so a real
// ssobff deployment configures the same way as the production binary.
const (
	ssobffBootstrapAdminUserEnv = "GOCELL_BOOTSTRAP_ADMIN_USERNAME"
	ssobffBootstrapAdminPassEnv = "GOCELL_BOOTSTRAP_ADMIN_PASSWORD"
)

// ssobffDatabaseURLEnv is the environment variable holding the PostgreSQL DSN.
// Required: NewSSOBFFApp fails fast when absent.
const ssobffDatabaseURLEnv = "DATABASE_URL"

// Duration constants for ssobff composition-root timeouts and token TTL.
// Each name expresses the semantic rather than the bare value so callers
// never need to look up what "5s" means in context.
const (
	// infraCleanupTimeout bounds the deferred close of managed infra
	// resources (Redis/RabbitMQ) when NewSSOBFFApp fails before fully loading.
	infraCleanupTimeout = 5 * time.Second
	// poolCleanupTimeout bounds the deferred pool.Close call when
	// NewSSOBFFApp fails after the PG pool is opened but before the app
	// finishes loading.
	poolCleanupTimeout = 5 * time.Second
	// bootstrapAuditAppendTimeout is the detached context deadline given to
	// the bootstrap auth-failure audit append (fire-and-forget relative to
	// the request context).
	bootstrapAuditAppendTimeout = 2 * time.Second
	// ssobffJWTTokenTTL is the lifetime of tokens issued by the ssobff JWT
	// issuer. 15 min matches the platform default in cmd/corebundle.
	ssobffJWTTokenTTL = 15 * time.Minute
)

// SSOBFFApp is the shared ssobff composition root used by main and tests.
//
// Topology-gated: demo topology uses in-memory event bus + in-memory idempotency
// claimer + in-memory nonce store (no infra required). Real multi-pod topology
// (GOCELL_ADAPTER_MODE=real + GOCELL_CELL_ADAPTER_MODE=postgres) uses a
// RabbitMQ-backed event transport and Redis-backed claimer + nonce store; missing
// Redis or AMQP URL is a fail-closed startup error (never a silent in-memory
// fallback). The fail-closed gate runs BEFORE the PostgreSQL pool is opened.
type SSOBFFApp struct {
	bootstrap          *bootstrap.Bootstrap
	primaryListenAddr  string
	internalListenAddr string
	healthListenAddr   string
}

// Run executes the underlying bootstrap lifecycle.
func (a *SSOBFFApp) Run(ctx context.Context) error {
	if a == nil || a.bootstrap == nil {
		return fmt.Errorf("ssobff: nil app")
	}
	return a.bootstrap.Run(ctx)
}

// PrimaryListenAddr returns the configured public listener address.
func (a *SSOBFFApp) PrimaryListenAddr() string {
	if a == nil {
		return ""
	}
	return a.primaryListenAddr
}

// InternalListenAddr returns the configured internal listener address.
func (a *SSOBFFApp) InternalListenAddr() string {
	if a == nil {
		return ""
	}
	return a.internalListenAddr
}

// HealthListenAddr returns the configured health listener address.
func (a *SSOBFFApp) HealthListenAddr() string {
	if a == nil {
		return ""
	}
	return a.healthListenAddr
}

// SSOBFFAppOption configures NewSSOBFFApp.
type SSOBFFAppOption func(*ssobffAppConfig) error

type listenerBinding struct {
	addr string
	ln   net.Listener
}

type ssobffAppConfig struct {
	logger                *slog.Logger
	internalServiceSecret string
	databaseURL           string
	primary               listenerBinding
	internal              listenerBinding
	health                listenerBinding
}

// WithSSOBFFLogger sets the logger used by the example cells.
func WithSSOBFFLogger(logger *slog.Logger) SSOBFFAppOption {
	return func(cfg *ssobffAppConfig) error {
		if logger == nil {
			return fmt.Errorf("ssobff: logger must not be nil")
		}
		cfg.logger = logger
		return nil
	}
}

// WithSSOBFFInternalServiceSecret injects the service-token secret protecting
// the internal listener.
func WithSSOBFFInternalServiceSecret(secret string) SSOBFFAppOption {
	return func(cfg *ssobffAppConfig) error {
		cfg.internalServiceSecret = secret
		return nil
	}
}

// WithSSOBFFDatabaseURL overrides the DATABASE_URL env var for tests.
func WithSSOBFFDatabaseURL(dsn string) SSOBFFAppOption {
	return func(cfg *ssobffAppConfig) error {
		cfg.databaseURL = dsn
		return nil
	}
}

// WithSSOBFFListener injects a pre-bound listener for tests.
func WithSSOBFFListener(ref cell.ListenerRef, ln net.Listener) SSOBFFAppOption {
	return func(cfg *ssobffAppConfig) error {
		if ln == nil {
			return fmt.Errorf("ssobff: listener %q must not be nil", ref.String())
		}
		b := listenerBinding{addr: ln.Addr().String(), ln: ln}
		switch ref {
		case cell.PrimaryListener:
			cfg.primary = b
		case cell.InternalListener:
			cfg.internal = b
		case cell.HealthListener:
			cfg.health = b
		default:
			return fmt.Errorf("ssobff: unsupported listener ref %q", ref.String())
		}
		return nil
	}
}

// ssobffInfra bundles the topology-gated infrastructure resolved before the DB pool.
type ssobffInfra struct {
	rd        replaydeps.ReplayDeps
	transport eventtransport.Transport
	topo      bootstrap.Topology
}

// resolveSSOBFFInfra resolves topology-gated infrastructure (replay deps +
// event transport) BEFORE the PG pool so fail-closed errors (missing Redis /
// AMQP URL in real multi-pod mode) surface before any network dial.
// On success, callers own rd.Resources and transport.Resources and must close
// them on failure. Extracted to keep NewSSOBFFApp ≤ gocognit 15.
func resolveSSOBFFInfra(ctx context.Context, clk clock.Clock) (ssobffInfra, error) {
	topo, err := bootstrap.TopologyFromEnv()
	if err != nil {
		return ssobffInfra{}, fmt.Errorf("ssobff: resolve topology: %w", err)
	}
	rd, err := replaydeps.Resolve(ctx, clk, topo)
	if err != nil {
		return ssobffInfra{}, fmt.Errorf("ssobff: resolve replay deps: %w", err)
	}
	// ssobff is a colocated single-broker example: its one broker cell shares the
	// assembly-wide GOCELL_AMQP_URL. eventtransport.Config.Cells is per-cell
	// (#2152 PR-2), so map the sole cell to that URL — dedup collapses it to one
	// connection (the same behavior as the previous single-URL wiring).
	transport, err := eventtransport.Resolve(clk, topo, eventtransport.Config{
		Cells: map[string]string{"ssobff": os.Getenv("GOCELL_AMQP_URL")},
	})
	if err != nil {
		closeManagedResources(ctx, rd.Resources)
		return ssobffInfra{}, fmt.Errorf("ssobff: resolve event transport: %w", err)
	}
	return ssobffInfra{rd: rd, transport: transport, topo: topo}, nil
}

// buildSSOBFFBootstrapOptions assembles the bootstrap option slice with LIFO-
// correct resource registration. Extracted to keep NewSSOBFFApp ≤ gocognit 15.
func buildSSOBFFBootstrapOptions(
	infra ssobffInfra,
	cfg *ssobffAppConfig,
	asm *assembly.CoreAssembly,
	cb *outbox.ConsumerBase,
	primaryAuth kauth.ListenerAuth,
	authGRPCOpts []bootstrap.Option,
	internalAuthChain []kauth.ListenerAuth,
	internalTLSOpts []bootstrap.ListenerOption,
	relayWorker *outboxruntime.Relay,
	pool *adapterpg.Pool,
) []bootstrap.Option {
	// Pool registered first → closes last; relay registered last → closes
	// first (must stop before pool closes). Broker + Redis resources between.
	// Full LIFO teardown: ownerCancel → lifecycle.Stop (consumers) → relay → redis → broker → pool.
	opts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(infra.transport.Publisher),
		bootstrap.WithSubscriber(infra.transport.Subscriber),
		// Sealed broker-kind fact from eventtransport.Resolve: the phase0
		// split-topology gate requires a real broker, not an in-process bus (#2211).
		bootstrap.WithEventTransportKind(infra.transport.Kind),
		bootstrap.WithConsumerBase(cb),
		bootstrap.WithManagedResource(pool),
		// Defense-in-depth: validate NonceStore/ConsumerClaimer kind against topology
		// (mirrors composition.Builder.Build; ssobff hand-assembles so must opt in explicitly).
		bootstrap.WithControlPlaneTopology(infra.topo),
	}
	for _, mr := range infra.transport.Resources {
		opts = append(opts, bootstrap.WithManagedResource(mr))
	}
	for _, mr := range infra.rd.Resources {
		opts = append(opts, bootstrap.WithManagedResource(mr))
	}
	// LIFO close: relay registered last → stopped first; relay must stop before pool closes.
	// Colocated single-pod: one relay under the default infra instance (#2152 PR-1).
	opts = append(opts, bootstrap.WithRelay(bootstrap.DefaultInstanceKey(), relayWorker))
	// ABAC PDP injector for the primary listener (#1348 PR-10a) + the mandatory gRPC
	// listener (accesscore registers grpc.auth.session.verify.v1 unconditionally, #1154).
	opts = append(opts, authGRPCOpts...)
	return append(opts,
		listenerOption(cell.PrimaryListener, cfg.primary, []kauth.ListenerAuth{primaryAuth}),
		// internal defaults to loopback (see defaultSSOBFFAppConfig); the cell→cell
		// control plane is never all-interfaces. Override GOCELL_SSOBFF_INTERNAL_ADDR
		// + add a NetworkPolicy for a VPC deployment (docs/ops/listener-topology.md).
		listenerOption(cell.InternalListener, cfg.internal, internalAuthChain, internalTLSOpts...),
		listenerOption(cell.HealthListener, cfg.health, []kauth.ListenerAuth{kauth.AuthNone{}}),
		bootstrap.WithHealthRoutes(healthRouteOptions()...),
	)
}

// applySSOBFFOptions applies each option in order; returns the first error.
// Extracted to reduce NewSSOBFFApp cognitive complexity.
func applySSOBFFOptions(cfg *ssobffAppConfig, opts []SSOBFFAppOption) error {
	for _, opt := range opts {
		if opt == nil {
			return fmt.Errorf("ssobff: nil app option")
		}
		if err := opt(cfg); err != nil {
			return err
		}
	}
	return nil
}

// NewSSOBFFApp builds the ssobff bootstrap app backed by a real PostgreSQL
// database. DATABASE_URL (or WithSSOBFFDatabaseURL option) must be set.
//
// On startup, all pending migrations are applied automatically so the schema
// is always up to date before any cell initializes.
//
// Topology-gated infra resolution runs BEFORE the PostgreSQL pool is opened:
// real multi-pod topology with missing Redis or AMQP URL returns an error
// before any network dial to the database, never silently degrading to
// in-memory backends.
//
// ref: uber-go/fx app.go — single app factory shared by production and tests.
// Deviates by keeping explicit typed construction instead of DI reflection.
func NewSSOBFFApp(opts ...SSOBFFAppOption) (*SSOBFFApp, error) {
	cfg := defaultSSOBFFAppConfig()
	if err := applySSOBFFOptions(cfg, opts); err != nil {
		return nil, err
	}

	// Single root clock for the entire composition root; all sub-functions
	// receive clk as a parameter (ADR docs/architecture/202605270000 §Decision #4).
	clk := clock.Real()
	ctx := context.Background()

	// Resolve topology-gated infra BEFORE the DB pool so missing Redis / AMQP
	// URL in real multi-pod mode fails closed before any network dial.
	infra, err := resolveSSOBFFInfra(ctx, clk)
	if err != nil {
		return nil, err
	}
	loaded := false
	defer func() {
		if !loaded {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), infraCleanupTimeout)
			defer cancel()
			closeManagedResources(cleanupCtx, infra.rd.Resources)
			closeManagedResources(cleanupCtx, infra.transport.Resources)
		}
	}()

	// Validate and build internal auth chain BEFORE opening the DB pool (F-S2):
	// a 1–31 byte secret is rejected here rather than after migrations run.
	internalAuthChain, err := newInternalAuthChain(cfg.internalServiceSecret, infra.rd.NonceStore)
	if err != nil {
		return nil, fmt.Errorf("ssobff: configure internal listener auth: %w", err)
	}
	// #2263: layer transport-level mTLS over the service-token chain when split
	// TLS material is provisioned (operator opt-in via GOCELL_TRANSPORT_TLS_*).
	// ssobff declares no remote cells; passing an empty spec (all-colocated) means
	// celltls.Resolve never fires the non-loopback fail-closed gate, so the only
	// effect of setting TLS material is wiring ServerTLS onto the internal listener
	// (server-side mTLS only — no client identity is used since ssobff has no
	// remote peers to dial). No material → chain unchanged (service-token-only).
	emptyTopo, err := bootstrap.NewDeploymentTopology(bootstrap.DeploymentTopologySpec{})
	if err != nil {
		return nil, fmt.Errorf("ssobff: build deployment topology: %w", err)
	}
	celltlsDeps, err := celltls.Resolve(emptyTopo, celltls.LoadConfigFromEnv())
	if err != nil {
		return nil, fmt.Errorf("ssobff: resolve transport TLS material: %w", err)
	}
	internalAuthChain, internalTLSOpts := celltls.InternalListenerSecurity(celltlsDeps.ServerTLS, internalAuthChain)

	// Resolve setup/admin bootstrap credentials BEFORE the DB pool (same
	// fail-fast posture as the internal auth chain): real topology must supply
	// production credentials via env — the public demo credentials never protect
	// a real setup endpoint (F1).
	bootstrapCreds, err := resolveSSOBFFBootstrapCreds(infra.topo)
	if err != nil {
		return nil, err
	}

	// JWT signing keys are topology-gated (mirrors resolveSSOBFFBootstrapCreds
	// above): demo mints an ephemeral pair, real mode loads the shared key env
	// pair and fails closed when missing. Resolved BEFORE the DB pool so a
	// real-mode missing-key misconfig fails fast — never a per-pod ephemeral key
	// that would 401 across replicas (#2052).
	jwtIssuer, jwtVerifier, err := newSSOBFFJWT(infra.topo, clk)
	if err != nil {
		return nil, err
	}

	if cfg.databaseURL == "" {
		return nil, fmt.Errorf("ssobff: %s must be set", ssobffDatabaseURLEnv)
	}

	pool, err := newSSOBFFPool(ctx, cfg.databaseURL)
	if err != nil {
		return nil, err
	}
	defer func() {
		if !loaded {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), poolCleanupTimeout)
			defer cancel()
			_ = pool.Close(cleanupCtx)
		}
	}()

	txMgr := adapterpg.NewTxManager(pool)

	// Build pgOutboxWriter + auditcore. After Wave-1 #1423 the bootstrap
	// chain is wired internally into auditcore (WithBootstrapStore); auditcore
	// now subscribes to event.auth.bootstrap-failed.v1 and writes the chain.
	pgOutboxWriter := adapterpg.NewOutboxWriter(clk)
	auc, err := buildSSOBFFAuditCore(ctx, ssobffAuditParams{
		clk: clk, logger: cfg.logger, eb: infra.transport.Publisher,
		outboxWriter: pgOutboxWriter, pool: pool, txMgr: txMgr,
		adapterMode: infra.topo.AdapterMode(),
	})
	if err != nil {
		return nil, err
	}

	asm, cb, primaryAuth, authGRPCOpts, err := buildSSOBFFCore(ssobffCoreParams{
		clk: clk, cfg: cfg, infra: infra, pool: pool, txMgr: txMgr,
		pgOutboxWriter: pgOutboxWriter, jwtIssuer: jwtIssuer, jwtVerifier: jwtVerifier,
		auc: auc, bootstrapCreds: bootstrapCreds,
	})
	if err != nil {
		return nil, err
	}

	// P1.5 fix (PR-CFG-L2-DIVERGENCE review): durable mode requires an outbox relay.
	pgOutboxStore := adapterpg.NewOutboxStore(pool.DB(), clk)
	relayWorker := outboxruntime.NewRelay(clk, pgOutboxStore, infra.transport.Publisher, outboxruntime.DefaultRelayConfig())

	b := bootstrap.New(clk, buildSSOBFFBootstrapOptions(
		infra, cfg, asm, cb, primaryAuth, authGRPCOpts, internalAuthChain, internalTLSOpts, relayWorker, pool,
	)...)

	loaded = true
	return &SSOBFFApp{
		bootstrap:          b,
		primaryListenAddr:  cfg.primary.addr,
		internalListenAddr: cfg.internal.addr,
		healthListenAddr:   cfg.health.addr,
	}, nil
}

// ssobffCoreParams groups the dependencies passed to buildSSOBFFCore, keeping
// the parameter count ≤ 7 (go:S107).
type ssobffCoreParams struct {
	clk            clock.Clock
	cfg            *ssobffAppConfig
	infra          ssobffInfra
	pool           *adapterpg.Pool
	txMgr          *adapterpg.TxManager
	pgOutboxWriter *adapterpg.OutboxWriter
	jwtIssuer      *auth.JWTIssuer
	jwtVerifier    *auth.JWTVerifier
	auc            *auditcore.AuditCore
	bootstrapCreds auth.BootstrapCredentials
}

// buildSSOBFFCore wires the session protocol, bootstrap middleware, and assembly.
// Extracted to keep NewSSOBFFApp ≤ gocognit 15.
func buildSSOBFFCore(p ssobffCoreParams) (*assembly.CoreAssembly, *outbox.ConsumerBase, kauth.ListenerAuth, []bootstrap.Option, error) {
	// Bootstrap auth-fail observer (Wave-1 #1423 event-based decoupling).
	var acPtr *accesscore.AccessCore
	// IP-hash salt is topology-gated (mirrors cellmodules/accesscore): demo falls
	// back to the denylisted dev default; real mode requires GOCELL_ACCESSCORE_IP_HASH_SALT
	// (fail-closed + demo-key reject + ≥32B) so the keyed client-IP hash is never reversible (#2052 F1).
	ipHashSalt, err := cellsecrets.BuildHMACKey(cellsecrets.HMACKeyConfig{
		AdapterMode: p.infra.topo.AdapterMode(),
		EnvName:     "GOCELL_ACCESSCORE_IP_HASH_SALT",
		Primary:     os.Getenv("GOCELL_ACCESSCORE_IP_HASH_SALT"),
		DevDefault:  ssobffIPHashSaltDefault,
	})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("ssobff: IP-hash salt: %w", err)
	}
	if len(ipHashSalt) < redaction.MinIPHashSaltBytes {
		return nil, nil, nil, nil, fmt.Errorf("ssobff: GOCELL_ACCESSCORE_IP_HASH_SALT must be at least %d bytes", redaction.MinIPHashSaltBytes)
	}
	authFailObserver := newSSOBFFAuthFailObserver(p.cfg.logger, &acPtr, ipHashSalt)

	bootstrapMW := auth.NewBootstrapMiddleware(
		p.bootstrapCreds,
		ratelimit.New(ratelimit.Config{
			Rate:  ssobffBootstrapRateLimitPerSec,
			Burst: ssobffBootstrapRateLimitBurst,
		}, p.clk),
		authFailObserver,
	)
	sessionProto, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("ssobff: session.NewProtocol: %w", err)
	}

	asm, cb, primaryAuth, authGRPCOpts, err := buildSSOBFFAssembly(p.clk, ssobffBuildParams{
		pool: p.pool, txMgr: p.txMgr, eb: p.infra.transport.Publisher, pgOutboxWriter: p.pgOutboxWriter,
		jwtIssuer: p.jwtIssuer, jwtVerifier: p.jwtVerifier,
		bootstrapMW: bootstrapMW, sessionProto: sessionProto, logger: p.cfg.logger,
		auc: p.auc, acRef: &acPtr, claimer: p.infra.rd.ConsumerClaimer,
		adapterMode: p.infra.topo.AdapterMode(),
	})
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return asm, cb, primaryAuth, authGRPCOpts, nil
}

// resolveSSOBFFBootstrapCreds selects the setup/admin Basic Auth credentials by
// topology (F1). Demo topology uses the package-local demo constants so
// `go run ./examples/ssobff` stays self-contained. Real topology
// (RequireProductionControlPlane) must supply credentials via
// GOCELL_BOOTSTRAP_ADMIN_USERNAME / GOCELL_BOOTSTRAP_ADMIN_PASSWORD and fails
// fast on missing / weak / demo-default values — the public demo credentials
// (documented in README + source) must never protect a real setup/admin
// endpoint. Mirrors cellmodules/accesscore.loadBootstrapCredentials validation.
func resolveSSOBFFBootstrapCreds(topo bootstrap.Topology) (auth.BootstrapCredentials, error) {
	if !topo.RequireProductionControlPlane() {
		return auth.BootstrapCredentials{
			Username: []byte(ssobffBootstrapUsername),
			Password: []byte(ssobffBootstrapPassword),
		}, nil
	}
	username := strings.TrimSpace(os.Getenv(ssobffBootstrapAdminUserEnv))
	password := strings.TrimSpace(os.Getenv(ssobffBootstrapAdminPassEnv))
	if username == "" || password == "" {
		return auth.BootstrapCredentials{}, fmt.Errorf(
			"ssobff: real topology requires %s and %s for the setup/admin endpoint "+
				"(the demo credentials must not protect a production bootstrap path)",
			ssobffBootstrapAdminUserEnv, ssobffBootstrapAdminPassEnv)
	}
	if username == ssobffBootstrapUsername || password == ssobffBootstrapPassword {
		return auth.BootstrapCredentials{}, fmt.Errorf(
			"ssobff: %s / %s must not reuse the public demo bootstrap credentials in real topology",
			ssobffBootstrapAdminUserEnv, ssobffBootstrapAdminPassEnv)
	}
	if len(password) < 8 {
		return auth.BootstrapCredentials{}, fmt.Errorf(
			"ssobff: %s must be at least 8 bytes", ssobffBootstrapAdminPassEnv)
	}
	return auth.BootstrapCredentials{Username: []byte(username), Password: []byte(password)}, nil
}

// closeManagedResources is a best-effort cleanup helper: it closes each
// managed resource in order. Nil resources are skipped. Non-nil close errors
// are logged as warnings (mirrors runtime/bootstrap/managed_resource.go teardown).
// Used by staged deferred cleanups when NewSSOBFFApp fails after opening infrastructure.
func closeManagedResources(ctx context.Context, rs []lifecycle.ManagedResource) {
	for _, r := range rs {
		if r == nil {
			continue
		}
		if err := r.Close(ctx); err != nil {
			slog.WarnContext(ctx, "ssobff: startup cleanup: managed resource Close failed",
				slog.String("resource", fmt.Sprintf("%T", r)),
				slog.Any("error", err))
		}
	}
}

// ssobffIPHashSaltDefault is the dev-only fallback for the keyed client-IP hash
// salt (#1488). ssobff is real-capable (topology-gated JWT/replay/transport since
// #2044/#2052), so the salt — like every other ssobff secret — routes through
// cellsecrets.BuildHMACKey (real-mode demo-key fail-fast + ≥32B). This value is
// registered in cellsecrets.wellKnownDemoKeys so real mode rejects it; real
// deployments must set GOCELL_ACCESSCORE_IP_HASH_SALT.
const ssobffIPHashSaltDefault = "dev-ip-hash-salt-ssobff-32-byte!"

// newSSOBFFAuthFailObserver returns an auth.BootstrapAuthFailObserver that logs
// the bootstrap auth failure with a hashed client IP and then (lazily) calls
// RecordBootstrapAuthFail on the accesscore cell via the *acPtr forward pointer.
//
// salt keys the single redaction.HashIP used for BOTH the slog field and the
// wire payload — no plaintext IP leaves this closure (#1488). *acPtr is set by
// the caller immediately after the accesscore cell is constructed (see
// buildSSOBFFAssembly). The observer fires only after HTTP servers start
// (post-Init), so *acPtr is always non-nil by then.
func newSSOBFFAuthFailObserver(logger *slog.Logger, acPtr **accesscore.AccessCore, salt []byte) auth.BootstrapAuthFailObserver {
	return func(ctx context.Context, reason string) {
		ip, _ := ctxkeys.RealIPFrom(ctx)
		ipHash := redaction.HashIP(salt, ip)
		logger.ErrorContext(ctx, "bootstrap_auth_failed",
			slog.String("namespace", "bootstrap"),
			slog.String("reason", reason),
			slog.String("client_ip_hash", ipHash.String()))
		if *acPtr == nil {
			// Symmetric with cellmodules/accesscore: surface the cell-not-ready
			// path so it is not a silent observability hole during debugging.
			logger.ErrorContext(ctx, "bootstrap_audit_append_failed",
				slog.String("namespace", "bootstrap"),
				slog.String("auth_reason", reason),
				slog.String("failure", "cell not yet initialized"),
				slog.String("client_ip_hash", ipHash.String()))
			return
		}
		appendCtx, cancel := ctxutil.WithDetachedTimeout(ctx, bootstrapAuditAppendTimeout)
		defer cancel()
		if err := (*acPtr).RecordBootstrapAuthFail(appendCtx, reason, ipHash); err != nil {
			logger.ErrorContext(ctx, "bootstrap_audit_append_failed",
				slog.String("namespace", "bootstrap"),
				slog.String("auth_reason", reason),
				slog.String("client_ip_hash", ipHash.String()),
				slog.Bool("timeout", errors.Is(err, context.DeadlineExceeded)),
				slog.Any("error", err))
		}
	}
}

// ssobffAuditParams groups buildSSOBFFAuditCore dependencies. ctx stays a
// positional first arg; the rest are bundled so adding adapterMode keeps the
// signature within go:S107 (mirrors the ssobffCoreParams pattern).
type ssobffAuditParams struct {
	clk          clock.Clock
	logger       *slog.Logger
	eb           outbox.Publisher
	outboxWriter *adapterpg.OutboxWriter
	pool         *adapterpg.Pool
	txMgr        *adapterpg.TxManager
	adapterMode  string
}

// buildSSOBFFAuditProtocol builds a ledger.Protocol with a topology-gated HMAC
// key (mirrors cellmodules/auditcore.buildAuditProtocol): demo falls back to the
// denylisted dev default, real mode requires envName and rejects demo keys.
func buildSSOBFFAuditProtocol(adapterMode, envName, primary, devDefault string, ns ledger.NamespaceID) (*ledger.Protocol, error) {
	hmacKey, err := cellsecrets.BuildHMACKey(cellsecrets.HMACKeyConfig{
		AdapterMode: adapterMode,
		EnvName:     envName,
		Primary:     primary,
		DevDefault:  devDefault,
	})
	if err != nil {
		return nil, err
	}
	defer clear(hmacKey)
	return ledger.NewProtocol(
		ns,
		hmacKey,
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
}

// buildSSOBFFAuditCore wires the ssobff auditcore Cell backed by PostgreSQL —
// ledger.Protocol is owned by the composition root; cells never hold the raw
// HMAC key. Mirrors cellmodules/auditcore/module.go durable path: cursor codec +
// relay/bootstrap HMAC keys are topology-gated via cellsecrets (real-mode
// demo-key fail-fast), demo falls back to denylisted dev keys, real requires the
// GOCELL_AUDITCORE_* / GOCELL_AUDIT_BOOTSTRAP_* env vars (#2052 F1).
//
// Since issue #1121 (ADR 202605270230) the bootstrap auth-fail chain is
// physically isolated from the auditcore relay chain: two independent
// (Protocol, Store) pairs are built, each with its own NamespaceID and HMAC
// key. auditquery reads from both via ledger.MultiStore. The bootstrap store
// is now wired into auditcore.WithBootstrapStore (Wave-1 #1423 event-based
// decoupling) and is no longer returned to the caller.
func buildSSOBFFAuditCore(ctx context.Context, p ssobffAuditParams) (*auditcore.AuditCore, error) {
	auditPrimary, auditPrevious := cellsecrets.LoadCursorKeys("AUDITCORE")
	cursorCodec, err := cellsecrets.BuildCursorCodec(cellsecrets.CursorCodecConfig{
		AdapterMode: p.adapterMode,
		EnvName:     "GOCELL_AUDITCORE_CURSOR_KEY",
		PrevEnvName: "GOCELL_AUDITCORE_CURSOR_PREVIOUS_KEY",
		Primary:     auditPrimary,
		Previous:    auditPrevious,
		DevDefault:  "corebundle-audit-cursor-key-32b!",
		Label:       "ssobff audit",
	})
	if err != nil {
		return nil, fmt.Errorf("ssobff: create audit cursor codec: %w", err)
	}
	auditNS, err := ledger.ParseNamespaceID("auditcore")
	if err != nil {
		return nil, fmt.Errorf("ssobff: parse audit namespace: %w", err)
	}
	relayProtocol, err := buildSSOBFFAuditProtocol(p.adapterMode, "GOCELL_AUDITCORE_HMAC_KEY",
		cellsecrets.LoadCellHMACKey("AUDITCORE"), "dev-hmac-key-replace-in-prod!!!!", auditNS)
	if err != nil {
		return nil, fmt.Errorf("ssobff: build audit protocol: %w", err)
	}
	// Independent HMAC key for the bootstrap chain (ref: hashicorp/vault per-
	// device Salt) so compromise of one chain's key cannot forge entries in
	// the other.
	bootstrapProtocol, err := buildSSOBFFAuditProtocol(p.adapterMode, "GOCELL_AUDIT_BOOTSTRAP_HMAC_KEY",
		cellsecrets.LoadCellHMACKey("AUDIT_BOOTSTRAP"), "dev-hmac-bootstrap-replace-32b!!", audit.BootstrapNamespace())
	if err != nil {
		return nil, fmt.Errorf("ssobff: build bootstrap audit protocol: %w", err)
	}
	relayStore, err := adapterpg.NewLedgerStore(p.pool.DB(), p.txMgr, relayProtocol, p.clk)
	if err != nil {
		return nil, fmt.Errorf("ssobff: adapterpg.NewLedgerStore (relay): %w", err)
	}
	bootstrapStore, err := adapterpg.NewLedgerStore(p.pool.DB(), p.txMgr, bootstrapProtocol, p.clk)
	if err != nil {
		return nil, fmt.Errorf("ssobff: adapterpg.NewLedgerStore (bootstrap): %w", err)
	}
	bootstrapWrapped, err := audit.NewBootstrapLedgerStore(bootstrapStore)
	if err != nil {
		return nil, fmt.Errorf("ssobff: wrap bootstrap ledger store: %w", err)
	}
	if err := audit.VerifyBootstrapTailOnStartup(ctx, bootstrapWrapped, p.logger); err != nil {
		return nil, fmt.Errorf("ssobff: bootstrap audit tail verify: %w", err)
	}
	multiStore, err := ledger.NewMultiStore(relayStore, bootstrapStore)
	if err != nil {
		return nil, fmt.Errorf("ssobff: build audit multi-store: %w", err)
	}
	auc := auditcore.NewAuditCore(
		p.clk,
		auditcore.WithLedgerProtocol(relayProtocol),
		auditcore.WithLedgerStore(relayStore),
		auditcore.WithQueryStore(multiStore),
		auditcore.WithOutboxDeps(outbox.WrapPublisherForCell(p.eb), outbox.WrapWriterForCell(p.outboxWriter)),
		auditcore.WithTxManager(persistence.WrapForCell(p.txMgr)),
		auditcore.WithCursorCodec(cursorCodec),
		auditcore.WithLogger(p.logger),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
		// Wave-1 #1423: bootstrap store wired internally; auditappendbootstrap
		// subscriber slice writes to it when event.auth.bootstrap-failed.v1 arrives.
		auditcore.WithBootstrapStore(bootstrapWrapped),
	)
	return auc, nil
}

// registerSSOBFFCells registers all three platform cells into the assembly.
// Returns the first registration error encountered.
func registerSSOBFFCells(asm *assembly.CoreAssembly, cells ...cell.Cell) error {
	for _, c := range cells {
		if err := asm.Register(c); err != nil {
			return fmt.Errorf("ssobff: register cell: %w", err)
		}
	}
	return nil
}

// ssobffBuildParams groups dependencies passed to buildSSOBFFAssembly, keeping
// the parameter count ≤ 7 (go:S107).
type ssobffBuildParams struct {
	pool           *adapterpg.Pool
	txMgr          *adapterpg.TxManager
	eb             outbox.Publisher
	pgOutboxWriter *adapterpg.OutboxWriter
	jwtIssuer      *auth.JWTIssuer
	jwtVerifier    *auth.JWTVerifier
	bootstrapMW    func(http.Handler) http.Handler
	sessionProto   *session.Protocol
	logger         *slog.Logger
	auc            *auditcore.AuditCore
	// acRef is a pointer to a pointer that will be set to the constructed
	// accesscore cell after construction, allowing the bootstrap auth-fail
	// observer closure (Wave-1 #1423) to call RecordBootstrapAuthFail.
	acRef **accesscore.AccessCore
	// claimer is the topology-gated idempotency claimer: in-memory for demo,
	// Redis-backed for real multi-pod (resolved by replaydeps.Resolve).
	claimer idempotency.Claimer
	// adapterMode is infra.topo.AdapterMode(): "" (dev) or "real". Threads the
	// topology gate to access/config cursor secrets built in buildSSOBFFAssembly.
	adapterMode string
}

// buildSSOBFFAssembly wires all three platform cells, registers them in a new
// CoreAssembly, and constructs the ConsumerBase and primary listener auth.
// Extracted from NewSSOBFFApp to reduce cognitive complexity.
func buildSSOBFFAssembly(clk clock.Clock, p ssobffBuildParams) (
	*assembly.CoreAssembly, *outbox.ConsumerBase, kauth.ListenerAuth, []bootstrap.Option, error,
) {
	accessStorageOpts, err := buildSSOBFFAccessCoreStorageOpts(clk, p.pool, p.txMgr, p.sessionProto)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	accessCAS, err := cas.NewProtocol(cas.WithVersionField(accesscore.PasswordVersionField))
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("ssobff: cas.NewProtocol (accesscore): %w", err)
	}
	// accesscore paginates (session/identity list endpoints) so durable mode
	// requires a cursor codec — topology-gated via cellsecrets (mirror
	// cellmodules/accesscore): demo dev default, real requires GOCELL_ACCESSCORE_CURSOR_KEY.
	accessPrimary, accessPrevious := cellsecrets.LoadCursorKeys("ACCESSCORE")
	accessCursorCodec, err := cellsecrets.BuildCursorCodec(cellsecrets.CursorCodecConfig{
		AdapterMode: p.adapterMode,
		EnvName:     "GOCELL_ACCESSCORE_CURSOR_KEY",
		PrevEnvName: "GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY",
		Primary:     accessPrimary,
		Previous:    accessPrevious,
		DevDefault:  "corebundle-access-cursor-key32!!",
		Label:       "ssobff access",
	})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("ssobff: create access cursor codec: %w", err)
	}
	ac := accesscore.NewAccessCore(clk, append(
		accessStorageOpts,
		accesscore.WithBootstrapAuth(p.bootstrapMW),
		accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(p.eb), outbox.WrapWriterForCell(p.pgOutboxWriter)),
		accesscore.WithJWTIssuer(p.jwtIssuer),
		accesscore.WithJWTVerifier(p.jwtVerifier),
		accesscore.WithCASProtocol(accessCAS),
		accesscore.WithCursorCodec(accessCursorCodec),
		accesscore.WithLogger(p.logger),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
	)...)
	// Set acRef so the bootstrap auth-fail observer closure can call
	// ac.RecordBootstrapAuthFail (Wave-1 #1423 event-based decoupling).
	if p.acRef != nil {
		*p.acRef = ac
	}

	// auditcore is built by NewSSOBFFApp (Wave-1 #1423: bootstrap store now wired
	// internally into auditcore via WithBootstrapStore; auditcore subscribes to
	// event.auth.bootstrap-failed.v1 and writes the chain asynchronously).
	auc := p.auc

	configStorageOpts, err := buildSSOBFFConfigCoreStorageOpts(p.adapterMode, clk, p.pool)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	configCAS, err := cas.NewProtocol(cas.WithVersionField(configcore.VersionField))
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("ssobff: cas.NewProtocol (configcore): %w", err)
	}
	cc := configcore.NewConfigCore(clk, append(
		configStorageOpts,
		configcore.WithOutboxDeps(outbox.WrapPublisherForCell(p.eb), outbox.WrapWriterForCell(p.pgOutboxWriter)),
		configcore.WithTxManager(persistence.WrapForCell(p.txMgr)),
		configcore.WithCASProtocol(configCAS),
		configcore.WithLogger(p.logger),
		configcore.WithMetricsProvider(metrics.NopProvider{}),
	)...)

	asm := assembly.New(clk, assembly.Config{ID: "ssobff", DurabilityMode: outbox.DurabilityDurable})
	if err := registerSSOBFFCells(asm, ac, auc, cc); err != nil {
		return nil, nil, nil, nil, err
	}
	// Wire the ABAC PDP (accesscore provides it) + the mandatory gRPC listener. Post
	// #1348 PR-10a the auditquery route gate is permission-based, so an
	// empty-actorId/cross-actor audit read fails closed without a PDP in context;
	// every auditquery-serving assembly must wire it. One lazy authorizer feeds BOTH
	// the HTTP primary listener and the gRPC gate (shared discovery in
	// bootstrap.AuthorizerFromCells, also used by cmd/corebundle, corebundlestarter).
	authorizer, err := bootstrap.AuthorizerFromCells([]cell.Cell{ac, auc, cc})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("ssobff: primary authorizer wiring: %w", err)
	}
	// gRPC listener: mandatory because accesscore registers grpc.auth.session.verify.v1
	// unconditionally (cell_gen.go, PR-11 #1154); without it bootstrap fail-fasts
	// (checkOrphanGRPCServices). Durability mode is topology-derived: real/postgres
	// topology passes DurabilityDurable (TLS enforced by grpclistener.ServerFromEnv),
	// demo/dev topology passes DurabilityDemo (plaintext default; set GOCELL_GRPC_TLS_*
	// for TLS in demo). This mirrors the assembly's own DurabilityDurable posture and
	// ensures real-mode TLS fail-closed gate is not bypassed.
	grpcCollector, err := obmetrics.NewGRPCProviderCollector(metrics.NopProvider{}, obmetrics.ProviderCollectorConfig{})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("ssobff: grpc metrics collector: %w", err)
	}
	grpcAddr := grpclistener.AddrFromEnv()
	grpcServer, err := grpclistener.ServerFromEnv(ssobffGRPCDurability(p.adapterMode), grpcAddr, interceptor.Deps{
		Verifier:        p.jwtVerifier,
		Clock:           clk,
		Collector:       grpcCollector,
		Authorizer:      authorizer,
		MetricsProvider: metrics.NopProvider{},
		CellIDClosedSet: asm.CellIDs(),
	})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("ssobff: grpc server: %w", err)
	}
	authGRPCOpts := []bootstrap.Option{
		bootstrap.WithPrimaryAuthorizer(authorizer),
		bootstrap.WithGRPCListener(cell.PrimaryListener, grpcServer, grpcAddr),
	}
	// Use the topology-gated claimer from replaydeps: in-memory for demo,
	// Redis-backed for real multi-pod (guards #825 at-most-once across replicas).
	cb, err := outbox.NewConsumerBase(
		p.claimer,
		outbox.ConsumerBaseConfig{},
		clk,
	)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("ssobff: create consumer base: %w", err)
	}
	primaryAuth, err := kauth.NewAuthJWTFromAssembly(asm)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("ssobff: primary listener auth plan: %w", err)
	}
	return asm, cb, primaryAuth, authGRPCOpts, nil
}

// newSSOBFFPool opens a PG pool and runs all pending migrations. Callers own
// the pool; pool.Close must be called if any subsequent step fails.
// Extracted to keep NewSSOBFFApp below gocognit ≤ 15.
func newSSOBFFPool(ctx context.Context, databaseURL string) (*adapterpg.Pool, error) {
	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: databaseURL})
	if err != nil {
		return nil, fmt.Errorf("ssobff: create PG pool: %w", err)
	}
	// Apply all pending migrations before cells initialize. Fail-fast on error
	// so schema drift is visible at startup rather than deep in request paths.
	migrationsFS, err := adapterpg.MigrationsFS()
	if err != nil {
		_ = pool.Close(ctx)
		return nil, fmt.Errorf("ssobff: get migrations FS: %w", err)
	}
	migrator, err := adapterpg.NewMigrator(pool, migrationsFS, migration.PlatformNamespace)
	if err != nil {
		_ = pool.Close(ctx)
		return nil, fmt.Errorf("ssobff: create migrator: %w", err)
	}
	if err := migrator.Up(ctx); err != nil {
		_ = pool.Close(ctx)
		return nil, fmt.Errorf("ssobff: run migrations: %w", err)
	}
	return pool, nil
}

// buildSSOBFFConfigCoreStorageOpts constructs the PG storage option and cursor
// codec for configcore. Extracted to keep NewSSOBFFApp below gocognit ≤ 15.
// The cursor codec is topology-gated via cellsecrets (mirror cellmodules/configcore):
// demo dev default, real requires GOCELL_CONFIGCORE_CURSOR_KEY (#2052 F1).
func buildSSOBFFConfigCoreStorageOpts(adapterMode string, clk clock.Clock, pool *adapterpg.Pool) ([]configcore.Option, error) {
	cfgPrimary, cfgPrevious := cellsecrets.LoadCursorKeys("CONFIGCORE")
	configCursorCodec, err := cellsecrets.BuildCursorCodec(cellsecrets.CursorCodecConfig{
		AdapterMode: adapterMode,
		EnvName:     "GOCELL_CONFIGCORE_CURSOR_KEY",
		PrevEnvName: "GOCELL_CONFIGCORE_CURSOR_PREVIOUS_KEY",
		Primary:     cfgPrimary,
		Previous:    cfgPrevious,
		DevDefault:  "corebundle-cfg-cursor-key--32bb!",
		Label:       "ssobff config",
	})
	if err != nil {
		return nil, fmt.Errorf("ssobff: create config cursor codec: %w", err)
	}
	storageOpt, err := configpg.WithPool(pool.DB(), clk)
	if err != nil {
		return nil, fmt.Errorf("ssobff: configpg.WithPool: %w", err)
	}
	return []configcore.Option{storageOpt, configcore.WithCursorCodec(configCursorCodec)}, nil
}

// buildSSOBFFAccessCoreStorageOpts constructs all PG-backed repository/store
// options required by accesscore. Extracted to keep NewSSOBFFApp below the
// gocognit ≤ 15 limit. WithPGBundle bundles
// (UserRepository, RoleRepository, SetupLock, TxRunner) into a single typed
// funnel so the four primitives provably share the same (pool, txMgr, clk).
func buildSSOBFFAccessCoreStorageOpts(
	clk clock.Clock,
	pool *adapterpg.Pool,
	txMgr *adapterpg.TxManager,
	sessionProto *session.Protocol,
) ([]accesscore.Option, error) {
	pgBundle, err := accesspg.NewBundle(pool.DB(), txMgr, clk)
	if err != nil {
		return nil, fmt.Errorf("ssobff: accesspg.NewBundle: %w", err)
	}
	sessionStore, err := adapterpg.NewSessionStore(pool.DB(), txMgr, sessionProto, clk)
	if err != nil {
		return nil, fmt.Errorf("ssobff: adapterpg.NewSessionStore: %w", err)
	}
	refreshStore, err := adapterpg.NewRefreshStore(pool.DB(), txMgr, accesscore.DefaultRefreshPolicy(), clk, nil)
	if err != nil {
		return nil, fmt.Errorf("ssobff: adapterpg.NewRefreshStore: %w", err)
	}
	return []accesscore.Option{
		accesscore.WithPGBundle(pgBundle),
		accesscore.WithSessionStore(sessionStore),
		accesscore.WithRefreshStore(refreshStore),
	}, nil
}

func defaultSSOBFFAppConfig() *ssobffAppConfig {
	return &ssobffAppConfig{
		logger:                slog.Default(),
		internalServiceSecret: os.Getenv(ssobffServiceKeyEnv),
		databaseURL:           os.Getenv(ssobffDatabaseURLEnv),
		primary:               listenerBinding{addr: envOr("GOCELL_SSOBFF_PRIMARY_ADDR", ":8081")},
		internal:              listenerBinding{addr: envOr("GOCELL_SSOBFF_INTERNAL_ADDR", "127.0.0.1:9081")},
		health:                listenerBinding{addr: envOr("GOCELL_SSOBFF_HEALTH_ADDR", "127.0.0.1:9091")},
	}
}

// ssobffGRPCDurability derives the gRPC listener durability mode from adapterMode.
// Real topology (cellsecrets.RealAdapterMode) uses DurabilityDurable so that
// grpclistener.ServerFromEnv enforces TLS fail-closed. Demo/dev uses DurabilityDemo
// (plaintext default). This mirrors the assembly's own DurabilityDurable posture and
// prevents real-mode gRPC from silently running plaintext (F3 security fix).
func ssobffGRPCDurability(adapterMode string) outbox.DurabilityMode {
	if adapterMode == cellsecrets.RealAdapterMode {
		return outbox.DurabilityDurable
	}
	return outbox.DurabilityDemo
}

// newSSOBFFJWT builds the JWT issuer and verifier from a topology-gated key set.
//
// The key source is resolved by cellsecrets.LoadKeySet (the same shared resolver
// cmd/corebundle's buildJWTDeps uses): demo topology mints an ephemeral in-process
// RSA pair (tokens invalidated on restart, single-pod only); real adapter mode
// loads the shared pair from GOCELL_JWT_PRIVATE_KEY / GOCELL_JWT_PUBLIC_KEY and
// fails closed when missing — a per-pod ephemeral key would make one replica's
// tokens verify as 401 against another (#2052). The issuer / audience stay fixed
// (ssobffJWTIssuer / ssobffJWTAudience): they are identical across replicas, so
// only the signing key was the multi-pod hazard.
func newSSOBFFJWT(topo bootstrap.Topology, clk clock.Clock) (*auth.JWTIssuer, *auth.JWTVerifier, error) {
	keySet, err := cellsecrets.LoadKeySet(topo.AdapterMode(), clk)
	if err != nil {
		return nil, nil, fmt.Errorf("ssobff: load JWT key set: %w", err)
	}
	jwtIssuer, err := auth.NewJWTIssuer(keySet, ssobffJWTIssuer, ssobffJWTTokenTTL, clk,
		auth.WithIssuerAudiencesFromSlice([]string{ssobffJWTAudience}))
	if err != nil {
		return nil, nil, fmt.Errorf("ssobff: create JWT issuer: %w", err)
	}
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clk,
		auth.WithExpectedAudiences(ssobffJWTAudience),
		auth.WithExpectedIssuer(ssobffJWTIssuer))
	if err != nil {
		return nil, nil, fmt.Errorf("ssobff: create JWT verifier: %w", err)
	}
	return jwtIssuer, jwtVerifier, nil
}

func listenerOption(
	ref cell.ListenerRef, binding listenerBinding, authChain []kauth.ListenerAuth,
	extra ...bootstrap.ListenerOption,
) bootstrap.Option {
	opts := append([]bootstrap.ListenerOption{}, extra...)
	if binding.ln != nil {
		opts = append(opts, bootstrap.WithListenerNet(binding.ln))
	}
	return bootstrap.WithListener(ref, binding.addr, authChain, opts...)
}

func healthRouteOptions() []bootstrap.HealthRouteGroupOption {
	if tok := os.Getenv("GOCELL_READYZ_VERBOSE_TOKEN"); tok != "" {
		return []bootstrap.HealthRouteGroupOption{bootstrap.WithReadyzVerboseToken(tok)}
	}
	return []bootstrap.HealthRouteGroupOption{bootstrap.WithReadyzVerboseDisabled()}
}

// envOr returns os.Getenv(key) when set; otherwise the fallback.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
