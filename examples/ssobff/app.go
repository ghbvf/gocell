package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	configcore "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

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

	eb := eventbus.New()
	// Demo only: test keys are generated in-process, so tokens do not survive
	// restart and cannot be verified by another replica.
	jwtIssuer, jwtVerifier, err := newSSOBFFJWT()
	if err != nil {
		return nil, err
	}

	var nw outbox.Writer = outbox.NoopWriter{}
	ac := accesscore.NewAccessCore(
		accesscore.WithInMemoryDefaults(),
		accesscore.WithInitialAdminBootstrap(),
		accesscore.WithOutboxDeps(eb, nw),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithTxManager(persistence.NoopTxRunner{}),
		accesscore.WithLogger(cfg.logger),
		accesscore.WithRefreshMetricsProvider(metrics.NopProvider{}),
	)

	// Demo only: HMAC and cursor keys are public source constants. Production
	// deployments must inject fresh secrets from a secret manager.
	auditCursorCodec, err := query.NewCursorCodec([]byte("ssobff-audit-cursor-key-32bytes!"))
	if err != nil {
		return nil, fmt.Errorf("ssobff: create audit cursor codec: %w", err)
	}
	auc := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithOutboxDeps(eb, nw),
		auditcore.WithHMACKey([]byte("ssobff-dev-hmac-key-32-bytes!!!!")),
		auditcore.WithTxManager(persistence.NoopTxRunner{}),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithLogger(cfg.logger),
		auditcore.WithMetricsProvider(metrics.NopProvider{}),
	)

	configCursorCodec, err := query.NewCursorCodec([]byte("ssobff-config-cursor-key-32bytes"))
	if err != nil {
		return nil, fmt.Errorf("ssobff: create config cursor codec: %w", err)
	}
	cc := configcore.NewConfigCore(
		configcore.WithInMemoryDefaults(),
		configcore.WithOutboxDeps(eb, nw),
		configcore.WithTxManager(persistence.NoopTxRunner{}),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithLogger(cfg.logger),
		configcore.WithMetricsProvider(metrics.NopProvider{}),
	)

	asm := assembly.New(assembly.Config{ID: "ssobff", DurabilityMode: cell.DurabilityDemo})
	for _, registerErr := range []error{asm.Register(ac), asm.Register(auc), asm.Register(cc)} {
		if registerErr != nil {
			return nil, fmt.Errorf("ssobff: register cell: %w", registerErr)
		}
	}

	b := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(eb),
		bootstrap.WithSubscriber(eb),
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
		internalServiceSecret: os.Getenv(ssobffServiceSecretEnv),
		primary:               listenerBinding{addr: envOr("GOCELL_SSOBFF_PRIMARY_ADDR", ":8081")},
		internal:              listenerBinding{addr: envOr("GOCELL_SSOBFF_INTERNAL_ADDR", "127.0.0.1:9081")},
		health:                listenerBinding{addr: envOr("GOCELL_SSOBFF_HEALTH_ADDR", "127.0.0.1:9091")},
	}
}

func newSSOBFFJWT() (*auth.JWTIssuer, *auth.JWTVerifier, error) {
	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey)
	if err != nil {
		return nil, nil, fmt.Errorf("ssobff: create key set: %w", err)
	}
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "ssobff-dev", 15*time.Minute,
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	if err != nil {
		return nil, nil, fmt.Errorf("ssobff: create JWT issuer: %w", err)
	}
	jwtVerifier, err := auth.NewJWTVerifier(keySet,
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
