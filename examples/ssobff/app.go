package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	kauth "github.com/ghbvf/gocell/kernel/auth"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/adapters/ratelimit"
	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	accesspg "github.com/ghbvf/gocell/cells/accesscore/postgres"
	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	configcore "github.com/ghbvf/gocell/cells/configcore"
	configpg "github.com/ghbvf/gocell/cells/configcore/postgres"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/audit"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	outboxruntime "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/state/cas"
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
// env (see cmd/corebundle/access_module.go); the demo path never reads
// the env in order to keep `go run ./examples/ssobff` self-contained.
const (
	ssobffBootstrapUsername = "ssobff-ops"
	// #nosec G101 -- demo fixture in examples/ssobff binary; production is env-driven (cmd/corebundle).
	ssobffBootstrapPassword = "ssobff-bootstrap-pass-1!"
)

// ssobffDatabaseURLEnv is the environment variable holding the PostgreSQL DSN.
// Required: NewSSOBFFApp fails fast when absent.
const ssobffDatabaseURLEnv = "DATABASE_URL"

// SSOBFFApp is the shared ssobff composition root used by main and tests.
//
// Single-pod demo only: the idempotency claimer is in-memory
// (idempotency.NewInMemClaimer). Multi-pod deployments require a Redis-backed
// claimer to guarantee at-most-once event processing across replicas.
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

// NewSSOBFFApp builds the ssobff bootstrap app backed by a real PostgreSQL
// database. DATABASE_URL (or WithSSOBFFDatabaseURL option) must be set.
//
// On startup, all pending migrations are applied automatically so the schema
// is always up to date before any cell initializes.
//
// ref: uber-go/fx app.go — single app factory shared by production and tests.
// Deviates by keeping explicit typed construction instead of DI reflection.
func NewSSOBFFApp(opts ...SSOBFFAppOption) (*SSOBFFApp, error) {
	cfg := defaultSSOBFFAppConfig()
	for _, opt := range opts {
		if opt == nil {
			return nil, fmt.Errorf("ssobff: nil app option")
		}
		if err := opt(cfg); err != nil {
			return nil, err
		}
	}

	// Validate service secret before attempting DB connection so that
	// configuration errors surface with a clear message at startup.
	internalAuthChain, err := newInternalAuthChain(cfg.internalServiceSecret)
	if err != nil {
		return nil, fmt.Errorf("ssobff: configure internal listener auth: %w", err)
	}

	if cfg.databaseURL == "" {
		return nil, fmt.Errorf("ssobff: %s must be set", ssobffDatabaseURLEnv)
	}

	ctx := context.Background()
	pool, err := newSSOBFFPool(ctx, cfg.databaseURL)
	if err != nil {
		return nil, err
	}

	txMgr := adapterpg.NewTxManager(pool)

	eb := eventbus.New(eventbus.WithClock(clock.Real()))
	// Demo only: test keys are generated in-process, so tokens do not survive
	// restart and cannot be verified by another replica.
	jwtIssuer, jwtVerifier, err := newSSOBFFJWT()
	if err != nil {
		_ = pool.Close(ctx)
		return nil, err
	}

	// Demo deployment runs in interactive mode: no initialadmin lifecycle is
	// wired (the operator POSTs to /api/v1/access/setup/admin to create the
	// first admin). Bootstrap credentials are still mandatory — they protect
	// the setup endpoint via Basic Auth (ADR §D2 operator credential via env). The demo uses the package-local ssobffBootstrap* constants;
	// production deployments inject from K8s Secret / Vault.
	// Build pgOutboxWriter + auditcore (and capture pgLedgerStore) BEFORE the
	// bootstrap middleware: runtime/audit.NewBootstrapAuthFailObserver needs
	// the same ledger.Store auditcore drains into HTTP queries, so bootstrap
	// 401/429s land in the auditcore hash chain (not slog-only).
	pgOutboxWriter := adapterpg.NewOutboxWriter(clock.Real())
	auc, ledgerStore, err := buildSSOBFFAuditCore(cfg.logger, eb, pgOutboxWriter, pool, txMgr)
	if err != nil {
		_ = pool.Close(ctx)
		return nil, err
	}
	authFailObserver, err := audit.NewBootstrapAuthFailObserver(cfg.logger, ledgerStore, clock.Real())
	if err != nil {
		_ = pool.Close(ctx)
		return nil, fmt.Errorf("ssobff: audit.NewBootstrapAuthFailObserver: %w", err)
	}

	ssobffBootstrapCreds := auth.BootstrapCredentials{
		Username: []byte(ssobffBootstrapUsername),
		Password: []byte(ssobffBootstrapPassword),
	}
	rlLimiter := ratelimit.New(ratelimit.Config{
		Rate:  ssobffBootstrapRateLimitPerSec,
		Burst: ssobffBootstrapRateLimitBurst,
	}, clock.Real())
	bootstrapMW := auth.NewBootstrapMiddleware(
		ssobffBootstrapCreds,
		rlLimiter,
		authFailObserver,
	)
	ssobffSessionProto, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	if err != nil {
		return nil, fmt.Errorf("ssobff: session.NewProtocol: %w", err)
	}

	// P1.5 fix (PR-CFG-L2-DIVERGENCE review): durable mode requires an outbox
	// relay; without it events stay in outbox_entries pending and subscribers
	// never receive them. Mirrors cmd/corebundle/bundle_configcore_storage.go
	// PG path (lines 109-118 + WithRelay).
	// outbox_entries table health is covered by the pool-level postgres_ready probe —
	// acceptable for this demo example; production bundles use per-cell repo probes.
	pgOutboxStore := adapterpg.NewOutboxStore(pool.DB(), clock.Real())
	relayCfg := outboxruntime.DefaultRelayConfig()
	relayCfg.Clock = clock.Real()
	relayWorker := outboxruntime.NewRelay(pgOutboxStore, eb, relayCfg)

	asm, cb, primaryAuth, err := buildSSOBFFAssembly(ssobffBuildParams{
		pool: pool, txMgr: txMgr, eb: eb, pgOutboxWriter: pgOutboxWriter,
		jwtIssuer: jwtIssuer, jwtVerifier: jwtVerifier,
		bootstrapMW: bootstrapMW, sessionProto: ssobffSessionProto, logger: cfg.logger,
		auc: auc,
	})
	if err != nil {
		_ = pool.Close(ctx)
		return nil, err
	}

	b := bootstrap.New(
		bootstrap.WithClock(clock.Real()),
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(eb),
		bootstrap.WithSubscriber(eb),
		bootstrap.WithConsumerBase(cb),
		bootstrap.WithManagedResource(pool),
		// LIFO close: relay registered last → stopped first; relay must stop before pool closes.
		bootstrap.WithRelay(relayWorker),
		listenerOption(cell.PrimaryListener, cfg.primary, []kauth.ListenerAuth{primaryAuth}),
		listenerOption(cell.InternalListener, cfg.internal, internalAuthChain),
		listenerOption(cell.HealthListener, cfg.health, []kauth.ListenerAuth{kauth.AuthNone{}}),
		bootstrap.WithHealthRoutes(healthRouteOptions()...),
	)

	return &SSOBFFApp{
		bootstrap:          b,
		primaryListenAddr:  cfg.primary.addr,
		internalListenAddr: cfg.internal.addr,
		healthListenAddr:   cfg.health.addr,
	}, nil
}

// buildSSOBFFAuditCore wires the ssobff auditcore Cell backed by PostgreSQL —
// ledger.Protocol is owned by the composition root; cells never hold the raw
// HMAC key. Mirrors cmd/corebundle/audit_module.go durable path but uses
// in-source demo HMAC key (production deployments must inject from a secret manager).
//
// Returns the AuditCore cell alongside the pgLedgerStore it was wired with, so
// the composition root can hand the same store to
// runtime/audit.NewBootstrapAuthFailObserver — bootstrap auth failures land in
// the same hash chain that auditcore drains into HTTP queries.
func buildSSOBFFAuditCore(
	logger *slog.Logger,
	eb outbox.Publisher,
	outboxWriter *adapterpg.OutboxWriter,
	pool *adapterpg.Pool,
	txMgr *adapterpg.TxManager,
) (*auditcore.AuditCore, ledger.Store, error) {
	cursorCodec, err := query.NewCursorCodec([]byte("ssobff-audit-cursor-key-32bytes!"))
	if err != nil {
		return nil, nil, fmt.Errorf("ssobff: create audit cursor codec: %w", err)
	}
	ns, err := ledger.ParseNamespaceID("auditcore")
	if err != nil {
		return nil, nil, fmt.Errorf("ssobff: parse audit namespace: %w", err)
	}
	// WARNING: demo key only. Production deployments must inject from a secret manager.
	protocol, err := ledger.NewProtocol(
		ledger.WithChainHMAC([]byte("ssobff-dev-hmac-key-32-bytes!!!!")), // #nosec G101 -- demo fixture, never used in production
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("ssobff: build audit protocol: %w", err)
	}
	pgLedgerStore, err := adapterpg.NewLedgerStore(pool.DB(), txMgr, protocol, clock.Real())
	if err != nil {
		return nil, nil, fmt.Errorf("ssobff: adapterpg.NewLedgerStore: %w", err)
	}
	auc := auditcore.NewAuditCore(
		auditcore.WithClock(clock.Real()),
		auditcore.WithLedgerProtocol(protocol),
		auditcore.WithLedgerStore(pgLedgerStore),
		auditcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(outboxWriter)),
		auditcore.WithTxManager(persistence.WrapForCell(txMgr)),
		auditcore.WithCursorCodec(cursorCodec),
		auditcore.WithLogger(logger),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
	)
	return auc, pgLedgerStore, nil
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
}

// buildSSOBFFAssembly wires all three platform cells, registers them in a new
// CoreAssembly, and constructs the ConsumerBase and primary listener auth.
// Extracted from NewSSOBFFApp to reduce cognitive complexity.
func buildSSOBFFAssembly(p ssobffBuildParams) (*assembly.CoreAssembly, *outbox.ConsumerBase, kauth.ListenerAuth, error) {
	accessStorageOpts, err := buildSSOBFFAccessCoreStorageOpts(p.pool, p.txMgr, p.sessionProto)
	if err != nil {
		return nil, nil, nil, err
	}
	accessCAS, err := cas.NewProtocol(cas.WithVersionField(accesscore.PasswordVersionField))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ssobff: cas.NewProtocol (accesscore): %w", err)
	}
	// accesscore paginates (session/identity list endpoints) so durable mode
	// requires a cursor codec — same demo-key pattern as config/audit above.
	// WARNING: demo key only; production deployments must inject from a secret manager.
	accessCursorCodec, err := query.NewCursorCodec([]byte("ssobff-access-cursor-key-32bytes"))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ssobff: create access cursor codec: %w", err)
	}
	ac := accesscore.NewAccessCore(append(
		accessStorageOpts,
		accesscore.WithClock(clock.Real()),
		accesscore.WithBootstrapAuth(p.bootstrapMW),
		accesscore.WithOutboxDeps(outbox.WrapPublisherForCell(p.eb), outbox.WrapWriterForCell(p.pgOutboxWriter)),
		accesscore.WithJWTIssuer(p.jwtIssuer),
		accesscore.WithJWTVerifier(p.jwtVerifier),
		accesscore.WithCASProtocol(accessCAS),
		accesscore.WithCursorCodec(accessCursorCodec),
		accesscore.WithLogger(p.logger),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
	)...)

	// auditcore is built by NewSSOBFFApp so its ledger.Store can also feed
	// runtime/audit.NewBootstrapAuthFailObserver before the bootstrap
	// middleware constructs.
	auc := p.auc

	configStorageOpts, err := buildSSOBFFConfigCoreStorageOpts(p.pool)
	if err != nil {
		return nil, nil, nil, err
	}
	configCAS, err := cas.NewProtocol(cas.WithVersionField(configcore.VersionField))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ssobff: cas.NewProtocol (configcore): %w", err)
	}
	cc := configcore.NewConfigCore(append(
		configStorageOpts,
		configcore.WithClock(clock.Real()),
		configcore.WithOutboxDeps(outbox.WrapPublisherForCell(p.eb), outbox.WrapWriterForCell(p.pgOutboxWriter)),
		configcore.WithTxManager(persistence.WrapForCell(p.txMgr)),
		configcore.WithCASProtocol(configCAS),
		configcore.WithLogger(p.logger),
		configcore.WithMetricsProvider(metrics.NopProvider{}),
	)...)

	asm := assembly.New(assembly.Config{ID: "ssobff", DurabilityMode: outbox.DurabilityDurable, Clock: clock.Real()})
	if err := registerSSOBFFCells(asm, ac, auc, cc); err != nil {
		return nil, nil, nil, err
	}
	cb, err := outbox.NewConsumerBase(
		idempotency.NewInMemClaimer(clock.Real()),
		outbox.ConsumerBaseConfig{},
		clock.Real(),
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ssobff: create consumer base: %w", err)
	}
	primaryAuth, err := kauth.NewAuthJWTFromAssembly(asm)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("ssobff: primary listener auth plan: %w", err)
	}
	return asm, cb, primaryAuth, nil
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
	migrator, err := adapterpg.NewMigrator(pool, migrationsFS, "schema_migrations")
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
func buildSSOBFFConfigCoreStorageOpts(pool *adapterpg.Pool) ([]configcore.Option, error) {
	configCursorCodec, err := query.NewCursorCodec([]byte("ssobff-config-cursor-key-32bytes"))
	if err != nil {
		return nil, fmt.Errorf("ssobff: create config cursor codec: %w", err)
	}
	storageOpt, err := configpg.WithPool(pool.DB(), clock.Real())
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
	pool *adapterpg.Pool,
	txMgr *adapterpg.TxManager,
	sessionProto *session.Protocol,
) ([]accesscore.Option, error) {
	pgBundle, err := accesspg.NewBundle(pool.DB(), txMgr, clock.Real())
	if err != nil {
		return nil, fmt.Errorf("ssobff: accesspg.NewBundle: %w", err)
	}
	sessionStore, err := adapterpg.NewSessionStore(pool.DB(), txMgr, sessionProto, clock.Real())
	if err != nil {
		return nil, fmt.Errorf("ssobff: adapterpg.NewSessionStore: %w", err)
	}
	refreshStore, err := adapterpg.NewRefreshStore(pool.DB(), txMgr, accesscore.DefaultRefreshPolicy(), clock.Real(), nil)
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

// newSSOBFFJWT creates an ephemeral JWT issuer and verifier backed by a freshly
// generated RSA key pair.
//
// Demo only: ephemeral in-process RSA keys; tokens invalidated on restart.
func newSSOBFFJWT() (*auth.JWTIssuer, *auth.JWTVerifier, error) {
	slog.Warn("ssobff: generating in-process JWT key — tokens become invalid on restart; do not use for multi-pod deployment")
	privKey, pubKey, err := auth.GenerateRSAKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("ssobff: generate RSA key pair: %w", err)
	}
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	if err != nil {
		return nil, nil, fmt.Errorf("ssobff: create key set: %w", err)
	}
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "ssobff-dev", 15*time.Minute, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	if err != nil {
		return nil, nil, fmt.Errorf("ssobff: create JWT issuer: %w", err)
	}
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clock.Real(),
		auth.WithExpectedAudiences("gocell"),
		auth.WithExpectedIssuer("ssobff-dev"))
	if err != nil {
		return nil, nil, fmt.Errorf("ssobff: create JWT verifier: %w", err)
	}
	return jwtIssuer, jwtVerifier, nil
}

func listenerOption(ref cell.ListenerRef, binding listenerBinding, authChain []kauth.ListenerAuth) bootstrap.Option {
	var opts []bootstrap.ListenerOption
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
