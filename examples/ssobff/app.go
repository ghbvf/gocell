package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	kauth "github.com/ghbvf/gocell/kernel/auth"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/adapters/ratelimit"
	eventtransport "github.com/ghbvf/gocell/cellmodules/eventtransport"
	replaydeps "github.com/ghbvf/gocell/cellmodules/replaydeps"
	accesscore "github.com/ghbvf/gocell/corecells/accesscore"
	accesspg "github.com/ghbvf/gocell/corecells/accesscore/postgres"
	auditcore "github.com/ghbvf/gocell/corecells/auditcore"
	configcore "github.com/ghbvf/gocell/corecells/configcore"
	configpg "github.com/ghbvf/gocell/corecells/configcore/postgres"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/ctxutil"
	"github.com/ghbvf/gocell/pkg/migration"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/redaction"
	"github.com/ghbvf/gocell/runtime/audit"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/bootstrap"
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
// env (see cellmodules/accesscore/module.go); the demo path never reads
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
	transport, err := eventtransport.Resolve(clk, topo, eventtransport.Config{
		AMQPURL: os.Getenv("GOCELL_AMQP_URL"),
	})
	if err != nil {
		closeManagedResources(ctx, rd.Resources)
		return ssobffInfra{}, fmt.Errorf("ssobff: resolve event transport: %w", err)
	}
	return ssobffInfra{rd: rd, transport: transport}, nil
}

// buildSSOBFFBootstrapOptions assembles the bootstrap option slice with LIFO-
// correct resource registration. Extracted to keep NewSSOBFFApp ≤ gocognit 15.
func buildSSOBFFBootstrapOptions(
	infra ssobffInfra,
	cfg *ssobffAppConfig,
	asm *assembly.CoreAssembly,
	cb *outbox.ConsumerBase,
	primaryAuth kauth.ListenerAuth,
	authzOpt bootstrap.Option,
	internalAuthChain []kauth.ListenerAuth,
	relayWorker *outboxruntime.Relay,
	pool *adapterpg.Pool,
) []bootstrap.Option {
	// Pool registered first → closes last; relay registered last → closes
	// first (must stop before pool closes). Broker + Redis resources between.
	opts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(infra.transport.Publisher),
		bootstrap.WithSubscriber(infra.transport.Subscriber),
		bootstrap.WithConsumerBase(cb),
		bootstrap.WithManagedResource(pool),
	}
	for _, mr := range infra.transport.Resources {
		opts = append(opts, bootstrap.WithManagedResource(mr))
	}
	for _, mr := range infra.rd.Resources {
		opts = append(opts, bootstrap.WithManagedResource(mr))
	}
	return append(opts,
		// LIFO close: relay registered last → stopped first; relay must stop before pool closes.
		bootstrap.WithRelay(relayWorker),
		authzOpt, // ABAC PDP injector for the primary listener (#1348 PR-10a).
		listenerOption(cell.PrimaryListener, cfg.primary, []kauth.ListenerAuth{primaryAuth}),
		// internal defaults to loopback (see defaultSSOBFFAppConfig); the cell→cell
		// control plane is never all-interfaces. Override GOCELL_SSOBFF_INTERNAL_ADDR
		// + add a NetworkPolicy for a VPC deployment (docs/ops/listener-topology.md).
		listenerOption(cell.InternalListener, cfg.internal, internalAuthChain),
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
			closeManagedResources(ctx, infra.rd.Resources)
			closeManagedResources(ctx, infra.transport.Resources)
		}
	}()

	// Validate service secret early (fail-fast before DB pool dial).
	if cfg.internalServiceSecret == "" {
		return nil, fmt.Errorf("ssobff: configure internal listener auth: %s must be set for the internal listener", ssobffServiceKeyEnv)
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
			_ = pool.Close(ctx)
		}
	}()

	txMgr := adapterpg.NewTxManager(pool)

	// Demo only: test keys are generated in-process, so tokens do not survive
	// restart and cannot be verified by another replica.
	jwtIssuer, jwtVerifier, err := newSSOBFFJWT(clk)
	if err != nil {
		return nil, err
	}

	// Build pgOutboxWriter + auditcore. After Wave-1 #1423 the bootstrap
	// chain is wired internally into auditcore (WithBootstrapStore); auditcore
	// now subscribes to event.auth.bootstrap-failed.v1 and writes the chain.
	pgOutboxWriter := adapterpg.NewOutboxWriter(clk)
	auc, err := buildSSOBFFAuditCore(ctx, clk, cfg.logger, infra.transport.Publisher, pgOutboxWriter, pool, txMgr)
	if err != nil {
		return nil, err
	}

	asm, cb, primaryAuth, authzOpt, internalAuthChain, err := buildSSOBFFCore(
		clk, cfg, infra, pool, txMgr, pgOutboxWriter, jwtIssuer, jwtVerifier, auc,
	)
	if err != nil {
		return nil, err
	}

	// P1.5 fix (PR-CFG-L2-DIVERGENCE review): durable mode requires an outbox relay.
	pgOutboxStore := adapterpg.NewOutboxStore(pool.DB(), clk)
	relayWorker := outboxruntime.NewRelay(clk, pgOutboxStore, infra.transport.Publisher, outboxruntime.DefaultRelayConfig())

	b := bootstrap.New(clk, buildSSOBFFBootstrapOptions(
		infra, cfg, asm, cb, primaryAuth, authzOpt, internalAuthChain, relayWorker, pool,
	)...)

	loaded = true
	return &SSOBFFApp{
		bootstrap:          b,
		primaryListenAddr:  cfg.primary.addr,
		internalListenAddr: cfg.internal.addr,
		healthListenAddr:   cfg.health.addr,
	}, nil
}

// buildSSOBFFCore wires the session protocol, bootstrap middleware, and assembly.
// Extracted to keep NewSSOBFFApp ≤ gocognit 15.
func buildSSOBFFCore(
	clk clock.Clock,
	cfg *ssobffAppConfig,
	infra ssobffInfra,
	pool *adapterpg.Pool,
	txMgr *adapterpg.TxManager,
	pgOutboxWriter *adapterpg.OutboxWriter,
	jwtIssuer *auth.JWTIssuer,
	jwtVerifier *auth.JWTVerifier,
	auc *auditcore.AuditCore,
) (*assembly.CoreAssembly, *outbox.ConsumerBase, kauth.ListenerAuth, bootstrap.Option, []kauth.ListenerAuth, error) {
	// Bootstrap auth-fail observer (Wave-1 #1423 event-based decoupling).
	var acPtr *accesscore.AccessCore
	ipHashSalt := []byte(envOr(ssobffIPHashSaltEnv, ssobffIPHashSaltDefault))
	authFailObserver := newSSOBFFAuthFailObserver(cfg.logger, &acPtr, ipHashSalt)

	bootstrapMW := auth.NewBootstrapMiddleware(
		auth.BootstrapCredentials{
			Username: []byte(ssobffBootstrapUsername),
			Password: []byte(ssobffBootstrapPassword),
		},
		ratelimit.New(ratelimit.Config{
			Rate:  ssobffBootstrapRateLimitPerSec,
			Burst: ssobffBootstrapRateLimitBurst,
		}, clk),
		authFailObserver,
	)
	sessionProto, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("ssobff: session.NewProtocol: %w", err)
	}

	internalAuthChain, err := newInternalAuthChain(cfg.internalServiceSecret, infra.rd.NonceStore)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("ssobff: configure internal listener auth: %w", err)
	}

	asm, cb, primaryAuth, authzOpt, err := buildSSOBFFAssembly(clk, ssobffBuildParams{
		pool: pool, txMgr: txMgr, eb: infra.transport.Publisher, pgOutboxWriter: pgOutboxWriter,
		jwtIssuer: jwtIssuer, jwtVerifier: jwtVerifier,
		bootstrapMW: bootstrapMW, sessionProto: sessionProto, logger: cfg.logger,
		auc: auc, acRef: &acPtr, claimer: infra.rd.ConsumerClaimer,
	})
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	return asm, cb, primaryAuth, authzOpt, internalAuthChain, nil
}

// closeManagedResources is a best-effort cleanup helper: it closes each
// managed resource in order. Nil resources are skipped. Used by staged
// deferred cleanups when NewSSOBFFApp fails after opening infrastructure.
func closeManagedResources(ctx context.Context, rs []lifecycle.ManagedResource) {
	for _, r := range rs {
		if r != nil {
			_ = r.Close(ctx)
		}
	}
}

// IP-hash salt for the demo (#1488). ssobff is a demo-only binary with NO
// real/demo adapter-mode concept, so — unlike cellmodules/accesscore which goes
// through cellsecrets.BuildHMACKey (real-mode demo-key fail-fast) — the salt is a
// plain env-or-default. The default is still registered in
// cellsecrets.wellKnownDemoKeys so it can never be copied into a real secret. The
// deliberate absence of a real-mode fail-fast here is acceptable because ssobff
// is never a production deployment; the production path (accesscore) enforces both
// demo-key rejection and the ≥32-byte minimum.
const (
	ssobffIPHashSaltEnv     = "GOCELL_SSOBFF_IP_HASH_SALT"
	ssobffIPHashSaltDefault = "dev-ip-hash-salt-ssobff-32-byte!"
)

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
		appendCtx, cancel := ctxutil.WithDetachedTimeout(ctx, 2*time.Second)
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

// buildSSOBFFAuditCore wires the ssobff auditcore Cell backed by PostgreSQL —
// ledger.Protocol is owned by the composition root; cells never hold the raw
// HMAC key. Mirrors cellmodules/auditcore/module.go durable path but uses
// in-source demo HMAC keys (production deployments must inject from a secret
// manager).
//
// Since issue #1121 (ADR 202605270230) the bootstrap auth-fail chain is
// physically isolated from the auditcore relay chain: two independent
// (Protocol, Store) pairs are built, each with its own NamespaceID and HMAC
// key. auditquery reads from both via ledger.MultiStore. The bootstrap store
// is now wired into auditcore.WithBootstrapStore (Wave-1 #1423 event-based
// decoupling) and is no longer returned to the caller.
func buildSSOBFFAuditCore(
	ctx context.Context,
	clk clock.Clock,
	logger *slog.Logger,
	eb outbox.Publisher,
	outboxWriter *adapterpg.OutboxWriter,
	pool *adapterpg.Pool,
	txMgr *adapterpg.TxManager,
) (*auditcore.AuditCore, error) {
	cursorCodec, err := query.NewCursorCodec([]byte("ssobff-audit-cursor-key-32bytes!"))
	if err != nil {
		return nil, fmt.Errorf("ssobff: create audit cursor codec: %w", err)
	}
	auditNS, err := ledger.ParseNamespaceID("auditcore")
	if err != nil {
		return nil, fmt.Errorf("ssobff: parse audit namespace: %w", err)
	}
	// WARNING: demo keys only. Production deployments must inject from a secret manager.
	relayProtocol, err := ledger.NewProtocol(
		auditNS,
		[]byte("ssobff-dev-hmac-key-32-bytes!!!!"), // #nosec G101 -- demo fixture, never used in production
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err != nil {
		return nil, fmt.Errorf("ssobff: build audit protocol: %w", err)
	}
	// Independent HMAC key for the bootstrap chain (ref: hashicorp/vault per-
	// device Salt) so compromise of one chain's key cannot forge entries in
	// the other.
	bootstrapProtocol, err := ledger.NewProtocol(
		audit.BootstrapNamespace(),
		[]byte("ssobff-bootstrap-hmac-key-32byte"), // #nosec G101 -- demo fixture, never used in production
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err != nil {
		return nil, fmt.Errorf("ssobff: build bootstrap audit protocol: %w", err)
	}
	relayStore, err := adapterpg.NewLedgerStore(pool.DB(), txMgr, relayProtocol, clk)
	if err != nil {
		return nil, fmt.Errorf("ssobff: adapterpg.NewLedgerStore (relay): %w", err)
	}
	bootstrapStore, err := adapterpg.NewLedgerStore(pool.DB(), txMgr, bootstrapProtocol, clk)
	if err != nil {
		return nil, fmt.Errorf("ssobff: adapterpg.NewLedgerStore (bootstrap): %w", err)
	}
	bootstrapWrapped, err := audit.NewBootstrapLedgerStore(bootstrapStore)
	if err != nil {
		return nil, fmt.Errorf("ssobff: wrap bootstrap ledger store: %w", err)
	}
	if err := audit.VerifyBootstrapTailOnStartup(ctx, bootstrapWrapped, logger); err != nil {
		return nil, fmt.Errorf("ssobff: bootstrap audit tail verify: %w", err)
	}
	multiStore, err := ledger.NewMultiStore(relayStore, bootstrapStore)
	if err != nil {
		return nil, fmt.Errorf("ssobff: build audit multi-store: %w", err)
	}
	auc := auditcore.NewAuditCore(
		clk,
		auditcore.WithLedgerProtocol(relayProtocol),
		auditcore.WithLedgerStore(relayStore),
		auditcore.WithQueryStore(multiStore),
		auditcore.WithOutboxDeps(outbox.WrapPublisherForCell(eb), outbox.WrapWriterForCell(outboxWriter)),
		auditcore.WithTxManager(persistence.WrapForCell(txMgr)),
		auditcore.WithCursorCodec(cursorCodec),
		auditcore.WithLogger(logger),
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
}

// buildSSOBFFAssembly wires all three platform cells, registers them in a new
// CoreAssembly, and constructs the ConsumerBase and primary listener auth.
// Extracted from NewSSOBFFApp to reduce cognitive complexity.
func buildSSOBFFAssembly(clk clock.Clock, p ssobffBuildParams) (
	*assembly.CoreAssembly, *outbox.ConsumerBase, kauth.ListenerAuth, bootstrap.Option, error,
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
	// requires a cursor codec — same demo-key pattern as config/audit above.
	// WARNING: demo key only; production deployments must inject from a secret manager.
	accessCursorCodec, err := query.NewCursorCodec([]byte("ssobff-access-cursor-key-32bytes"))
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

	configStorageOpts, err := buildSSOBFFConfigCoreStorageOpts(clk, p.pool)
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
	// Wire the ABAC PDP into the primary listener (accesscore provides it). Post
	// #1348 PR-10a the auditquery route gate is permission-based, so an
	// empty-actorId/cross-actor audit read fails closed without a PDP in context;
	// every auditquery-serving assembly must wire it. Shared discovery+lazy logic
	// lives in bootstrap.PrimaryAuthorizerOption (also used by cmd/corebundle,
	// corebundlestarter).
	authzOpt, err := bootstrap.PrimaryAuthorizerOption([]cell.Cell{ac, auc, cc})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("ssobff: primary authorizer wiring: %w", err)
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
	return asm, cb, primaryAuth, authzOpt, nil
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
func buildSSOBFFConfigCoreStorageOpts(clk clock.Clock, pool *adapterpg.Pool) ([]configcore.Option, error) {
	configCursorCodec, err := query.NewCursorCodec([]byte("ssobff-config-cursor-key-32bytes"))
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

// newSSOBFFJWT creates an ephemeral JWT issuer and verifier backed by a freshly
// generated RSA key pair.
//
// Demo only: ephemeral in-process RSA keys; tokens invalidated on restart.
func newSSOBFFJWT(clk clock.Clock) (*auth.JWTIssuer, *auth.JWTVerifier, error) {
	slog.Warn("ssobff: generating in-process JWT key — tokens become invalid on restart; do not use for multi-pod deployment")
	privKey, pubKey, err := auth.GenerateRSAKeyPair()
	if err != nil {
		return nil, nil, fmt.Errorf("ssobff: generate RSA key pair: %w", err)
	}
	keySet, err := auth.NewKeySet(privKey, pubKey, clk)
	if err != nil {
		return nil, nil, fmt.Errorf("ssobff: create key set: %w", err)
	}
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "ssobff-dev", 15*time.Minute, clk,
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	if err != nil {
		return nil, nil, fmt.Errorf("ssobff: create JWT issuer: %w", err)
	}
	jwtVerifier, err := auth.NewJWTVerifier(keySet, clk,
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
