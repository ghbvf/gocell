package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/ghbvf/gocell/adapters/ratelimit"
	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	configcore "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
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

// ssobffBootstrapAuthFailLogger returns the onAuthFail observer wired into the
// demo bootstrap middleware.
func ssobffBootstrapAuthFailLogger(logger *slog.Logger) auth.BootstrapAuthFailObserver {
	return func(ctx context.Context, reason string) {
		logger.ErrorContext(ctx, "bootstrap_auth_failed",
			slog.String("event", "bootstrap_auth_failed"),
			slog.String("reason", reason))
	}
}

// demoTxRunner is a pass-through TxRunner used in demo mode. It executes fn
// directly without a database transaction — no L2 atomicity guarantees.
// Production assemblies inject a real TxRunner (e.g., postgres.TxManager).
type demoTxRunner struct{}

func (demoTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// SSOBFFApp is the shared ssobff composition root used by main and tests.
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

// NewSSOBFFApp builds the ssobff bootstrap app.
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

	internalAuthChain, err := newInternalAuthChain(cfg.internalServiceSecret)
	if err != nil {
		return nil, fmt.Errorf("ssobff: configure internal listener auth: %w", err)
	}

	eb := eventbus.New(eventbus.WithClock(clock.Real()))
	// Demo only: test keys are generated in-process, so tokens do not survive
	// restart and cannot be verified by another replica.
	jwtIssuer, jwtVerifier, err := newSSOBFFJWT()
	if err != nil {
		return nil, err
	}

	var nw outbox.Writer = outbox.NoopWriter{}
	// Demo deployment runs in interactive mode: no initialadmin lifecycle is
	// wired (the operator POSTs to /api/v1/access/setup/admin to create the
	// first admin). Bootstrap credentials are still mandatory — they protect
	// the setup endpoint via Basic Auth (ADR §D2 operator credential via env). The demo uses the package-local ssobffBootstrap* constants;
	// production deployments inject from K8s Secret / Vault.
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
		ssobffBootstrapAuthFailLogger(cfg.logger),
	)
	ac := accesscore.NewAccessCore(
		accesscore.WithClock(clock.Real()),
		accesscore.WithInMemoryDefaults(),
		accesscore.WithBootstrapAuth(bootstrapMW),
		accesscore.WithOutboxDeps(eb, nw),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithTxManager(demoTxRunner{}),
		accesscore.WithLogger(cfg.logger),
		accesscore.WithMetricsProvider(metrics.NopProvider{}),
	)

	// Demo only: HMAC and cursor keys are public source constants. Production
	// deployments must inject fresh secrets from a secret manager.
	auditCursorCodec, err := query.NewCursorCodec([]byte("ssobff-audit-cursor-key-32bytes!"))
	if err != nil {
		return nil, fmt.Errorf("ssobff: create audit cursor codec: %w", err)
	}
	auc := auditcore.NewAuditCore(
		auditcore.WithClock(clock.Real()),
		auditcore.WithInMemoryDefaults(),
		auditcore.WithOutboxDeps(eb, nw),
		auditcore.WithHMACKey([]byte("ssobff-dev-hmac-key-32-bytes!!!!")),
		auditcore.WithTxManager(demoTxRunner{}),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithLogger(cfg.logger),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
	)

	configCursorCodec, err := query.NewCursorCodec([]byte("ssobff-config-cursor-key-32bytes"))
	if err != nil {
		return nil, fmt.Errorf("ssobff: create config cursor codec: %w", err)
	}
	cc := configcore.NewConfigCore(
		configcore.WithClock(clock.Real()),
		configcore.WithInMemoryDefaults(),
		configcore.WithOutboxDeps(eb, nw),
		configcore.WithTxManager(demoTxRunner{}),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithLogger(cfg.logger),
		configcore.WithMetricsProvider(metrics.NopProvider{}),
	)

	asm := assembly.New(assembly.Config{ID: "ssobff", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	for _, registerErr := range []error{asm.Register(ac), asm.Register(auc), asm.Register(cc)} {
		if registerErr != nil {
			return nil, fmt.Errorf("ssobff: register cell: %w", registerErr)
		}
	}
	cb, err := outbox.NewConsumerBase(
		idempotency.NewInMemClaimer(clock.Real()),
		outbox.ConsumerBaseConfig{},
		clock.Real(),
	)
	if err != nil {
		return nil, fmt.Errorf("ssobff: create consumer base: %w", err)
	}

	b := bootstrap.New(
		bootstrap.WithClock(clock.Real()),
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(eb),
		bootstrap.WithSubscriber(eb),
		bootstrap.WithConsumerBase(cb),
		listenerOption(cell.PrimaryListener, cfg.primary, []cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)}),
		listenerOption(cell.InternalListener, cfg.internal, internalAuthChain),
		listenerOption(cell.HealthListener, cfg.health, []cell.ListenerAuth{cell.AuthNone{}}),
		bootstrap.WithHealthRoutes(healthRouteOptions()...),
	)

	return &SSOBFFApp{
		bootstrap:          b,
		primaryListenAddr:  cfg.primary.addr,
		internalListenAddr: cfg.internal.addr,
		healthListenAddr:   cfg.health.addr,
	}, nil
}

func defaultSSOBFFAppConfig() *ssobffAppConfig {
	return &ssobffAppConfig{
		logger:                slog.Default(),
		internalServiceSecret: os.Getenv(ssobffServiceKeyEnv),
		primary:               listenerBinding{addr: envOr("GOCELL_SSOBFF_PRIMARY_ADDR", ":8081")},
		internal:              listenerBinding{addr: envOr("GOCELL_SSOBFF_INTERNAL_ADDR", "127.0.0.1:9081")},
		health:                listenerBinding{addr: envOr("GOCELL_SSOBFF_HEALTH_ADDR", "127.0.0.1:9091")},
	}
}

func newSSOBFFJWT() (*auth.JWTIssuer, *auth.JWTVerifier, error) {
	privKey, pubKey := auth.MustGenerateTestKeyPair()
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

func listenerOption(ref cell.ListenerRef, binding listenerBinding, authChain []cell.ListenerAuth) bootstrap.Option {
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
