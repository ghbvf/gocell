package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	accesscore "github.com/ghbvf/gocell/cells/access-core"
	auditcore "github.com/ghbvf/gocell/cells/audit-core"
	configcore "github.com/ghbvf/gocell/cells/config-core"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/crypto"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/router"
	outboxruntime "github.com/ghbvf/gocell/runtime/outbox"
)

// AppDeps groups all runtime dependencies resolved at startup. Production code
// uses AppDepsFromEnv to populate it from environment variables; tests inject
// stubs directly via struct literal.
//
// ref: uber-go/fx fxtest.App — same BuildBootstrap(deps) call in production
// (AppDepsFromEnv) and tests (struct literal), preventing assembly drift.
type AppDeps struct {
	// Topology is the resolved adapter-mode / storage-backend combination.
	Topology bootstrap.Topology

	// PGResource is the ManagedResource wrapping the PG pool + relay (nil in
	// memory mode). Tests inject a fake; production uses *adapterpg.PGResource.
	PGResource bootstrap.ManagedResource

	// configCellOpts holds the config-core cell options built by AppDepsFromEnv.
	// In tests (struct literal without configCellOpts), BuildBootstrap uses
	// in-memory defaults for config-core.
	configCellOpts []configcore.Option

	// metricsHandler is the Prometheus HTTP handler built once in AppDepsFromEnv
	// and reused by BuildBootstrap. Avoids a double call to promhttp.HandlerFor.
	metricsHandler http.Handler

	// verboseOpts are the bootstrap options for /readyz?verbose auth, built
	// once in AppDepsFromEnv and consumed directly by BuildBootstrap.
	verboseOpts []bootstrap.Option

	// JWTDeps holds the JWT issuer and verifier.
	JWTDeps jwtDeps

	// PromStack holds the Prometheus registry, hook observer, and metric provider.
	PromStack promStack

	// CursorCodecs holds the audit and config cursor codecs.
	CursorCodecs cursorCodecs

	// HMACKey is the HMAC secret for audit-core chain authentication.
	HMACKey []byte

	// EventBus is the in-process event bus used for both publish and subscribe.
	EventBus *eventbus.InMemoryEventBus

	// InternalGuard is the service-token middleware protecting /internal/v1/*.
	// nil = no guard (dev mode with empty GOCELL_SERVICE_SECRET).
	InternalGuard func(http.Handler) http.Handler

	// MetricsToken is the token guarding /metrics (empty = open in dev mode).
	MetricsToken string

	// VerboseToken is the token guarding /readyz?verbose.
	VerboseToken string

	// InitialAdminBootstrapOpts are additional options passed to
	// access-core's WithInitialAdminBootstrap. Production (AppDepsFromEnv)
	// leaves this nil so default bcrypt cost=12 is used. Tests append
	// accesscore.WithBootstrapPasswordHasher(initialadmin.BcryptHasher{
	// Cost: bcrypt.MinCost}) so phase3 is not blocked by a 5-7s bcrypt
	// on slow CI runners — startup wiring is still fully exercised.
	InitialAdminBootstrapOpts []accesscore.InitialAdminOption

	// KeyProvider is the KMS backend used to encrypt/decrypt sensitive config
	// values at the repository boundary. nil = NoopTransformer (no encryption).
	// Set via GOCELL_KEY_PROVIDER=local-aes|vault-transit in production;
	// left nil for test struct-literal AppDeps (in-memory mode).
	//
	// BuildBootstrap constructs a ValueTransformer from KeyProvider internally.
	// Tests that need to inject a custom transformer should use configCellOpts
	// (e.g. configcore.WithValueTransformer(fakeT)).
	KeyProvider crypto.KeyProvider
}

// AppDepsFromEnv reads all environment variables and builds a fully-populated
// AppDeps. Returns an error on any misconfiguration (fail-fast before any
// assembly starts).
//
// ref: go-zero serviceconf.MustLoad — single parse-validate call at startup.
func AppDepsFromEnv(ctx context.Context) (*AppDeps, error) {
	topo, err := bootstrap.TopologyFromEnv()
	if err != nil {
		return nil, err
	}
	adapterMode := topo.AdapterMode

	hmacKey, err := loadSecret("GOCELL_HMAC_KEY", "dev-hmac-key-replace-in-prod!!!!", adapterMode)
	if err != nil {
		return nil, fmt.Errorf("HMAC key: %w", err)
	}
	if err := rejectDemoKey(adapterMode, "GOCELL_HMAC_KEY", hmacKey); err != nil {
		return nil, err
	}

	jwt, err := buildJWTDeps(adapterMode)
	if err != nil {
		return nil, err
	}

	codecs, err := loadAllCursorCodecs(adapterMode)
	if err != nil {
		return nil, err
	}

	ps, err := buildPromStack()
	if err != nil {
		return nil, err
	}

	eb := eventbus.New()

	// Build KeyProvider from GOCELL_KEY_PROVIDER env var. The provider
	// constructor is the single validation gate for encryption-mode coupling:
	// postgres storage must fail-fast when no provider is configured.
	// ref: kubernetes/kubernetes EncryptionConfig — missing provider is a
	// startup error, never a silent NoopTransformer fallback.
	kp, err := buildKeyProvider(topo.StorageBackend)
	if err != nil {
		return nil, fmt.Errorf("key provider: %w", err)
	}
	// Derive ValueTransformer from KeyProvider for postgres wiring.
	vt := keyProviderToTransformer(kp)

	// Topology is the single source of truth for adapter/storage selection.
	// buildConfigCoreOpts receives it as a parameter and must not re-read the
	// environment — any second read would create a drift path between the
	// "reported topology" and "actual wiring".
	// ref: go-zero core/conf/config.go validate(v) — single validation gate at
	// the unmarshal boundary, never re-read downstream.
	pgRes, cellOpts, err := buildConfigCoreOpts(ctx, topo, eb, ps.metricProvider, vt)
	if err != nil {
		return nil, err
	}

	internalGuard, err := internalGuardFromEnv(adapterMode)
	if err != nil {
		return nil, err
	}

	verboseToken := os.Getenv("GOCELL_READYZ_VERBOSE_TOKEN")
	verboseOpts, err := buildVerboseOpts(adapterMode, verboseToken)
	if err != nil {
		return nil, err
	}

	metricsToken := os.Getenv("GOCELL_METRICS_TOKEN")
	metricsHandler, err := buildMetricsHandler(adapterMode, metricsToken, ps.registry)
	if err != nil {
		return nil, err
	}

	slog.Info("adapter mode",
		slog.String("requested", adapterMode),
		slog.String("effective", topo.AdapterInfo()["mode"]))

	return &AppDeps{
		Topology:       topo,
		PGResource:     pgRes,
		configCellOpts: cellOpts,
		JWTDeps:        jwt,
		PromStack:      ps,
		CursorCodecs:   codecs,
		HMACKey:        hmacKey,
		EventBus:       eb,
		InternalGuard:  internalGuard,
		MetricsToken:   metricsToken,
		VerboseToken:   verboseToken,
		metricsHandler: metricsHandler,
		verboseOpts:    verboseOpts,
		KeyProvider:    kp,
	}, nil
}

// BuildBootstrap assembles the three cells and all bootstrap options from deps.
// Extra options (e.g. bootstrap.WithListener for tests) may be appended.
//
// This is the canonical assembly entry point shared by production and tests.
// Production calls run() → AppDepsFromEnv → BuildBootstrap.
// Tests call BuildBootstrap directly with a struct-literal AppDeps, ensuring
// identical wiring and preventing assembly drift.
//
// ref: uber-go/fx fxtest.App — same module/option list, different context.
func BuildBootstrap(deps *AppDeps, extra ...bootstrap.Option) (*bootstrap.Bootstrap, error) {
	configCell := buildConfigCell(deps)

	accessOpts, adminWorkerOpt := adminBootstrapWorkerOpts([]accesscore.Option{
		accesscore.WithInMemoryDefaults(),
		accesscore.WithPublisher(deps.EventBus),
		accesscore.WithJWTIssuer(deps.JWTDeps.issuer),
		accesscore.WithJWTVerifier(deps.JWTDeps.verifier),
	}, deps.InitialAdminBootstrapOpts...)
	accessCell := accesscore.NewAccessCore(accessOpts...)

	auditCell := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithPublisher(deps.EventBus),
		auditcore.WithHMACKey(deps.HMACKey),
		auditcore.WithCursorCodec(deps.CursorCodecs.audit),
	)

	asm, err := buildAssembly(deps.PromStack, configCell, accessCell, auditCell)
	if err != nil {
		return nil, err
	}

	// Use the pre-built metricsHandler from AppDepsFromEnv when available (avoids
	// a second promhttp.HandlerFor call). The test path (struct literal AppDeps)
	// leaves metricsHandler nil, so we build it here as a fallback.
	metricsHandler := deps.metricsHandler
	if metricsHandler == nil {
		metricsHandler, err = buildMetricsHandler(
			deps.Topology.AdapterMode, deps.MetricsToken, deps.PromStack.registry)
		if err != nil {
			return nil, err
		}
	}

	consumerBase, err := outbox.NewConsumerBase(idempotency.NewInMemClaimer(), outbox.ConsumerBaseConfig{})
	if err != nil {
		return nil, fmt.Errorf("construct ConsumerBase: %w", err)
	}

	logInitialAdminCredPath()

	adapterInfo := deps.Topology.AdapterInfo()
	slog.Info("core-bundle: startup configuration",
		slog.String("adapter_mode", adapterInfo["mode"]),
		slog.String("storage", adapterInfo["storage"]),
		slog.String("event_bus", adapterInfo["event_bus"]),
		slog.String("outbox_storage", adapterInfo["outbox_storage"]))

	opts := assembleFromDeps(assembledDeps{
		assembly:       asm,
		deps:           deps,
		consumerBase:   consumerBase,
		metricsHandler: metricsHandler,
		adminWorkerOpt: adminWorkerOpt,
		adapterInfo:    adapterInfo,
	})
	opts = append(opts, extra...)
	return bootstrap.New(opts...), nil
}

// buildConfigCell constructs the config-core cell from AppDeps.
// When configCellOpts is populated (via AppDepsFromEnv), those options are used.
// In tests (struct literal without configCellOpts), in-memory defaults apply.
// When deps.configCellOpts is nil, falls back to in-memory defaults; this is the test-injection contract.
//
// ValueTransformer is derived from KeyProvider here (single source of truth).
// Tests that need a custom transformer inject it via configCellOpts:
//
//	deps.configCellOpts = []configcore.Option{configcore.WithValueTransformer(fakeT)}
func buildConfigCell(deps *AppDeps) *configcore.ConfigCore {
	base := []configcore.Option{
		configcore.WithPublisher(deps.EventBus),
		configcore.WithCursorCodec(deps.CursorCodecs.config),
	}
	// Derive ValueTransformer from KeyProvider. When KeyProvider is nil (test
	// struct-literal path), keyProviderToTransformer returns NoopTransformer.
	if vt := keyProviderToTransformer(deps.KeyProvider); vt != nil {
		base = append(base, configcore.WithValueTransformer(vt))
	}
	if deps.configCellOpts != nil {
		return configcore.NewConfigCore(append(base, deps.configCellOpts...)...)
	}
	// Test path: in-memory defaults (no real PG).
	return configcore.NewConfigCore(append(base, configcore.WithInMemoryDefaults())...)
}

// assembledDeps groups the fully-built components ready for option assembly.
type assembledDeps struct {
	assembly       *assembly.CoreAssembly
	deps           *AppDeps
	consumerBase   *outbox.ConsumerBase
	metricsHandler http.Handler
	adminWorkerOpt bootstrap.Option
	adapterInfo    map[string]string
}

// assembleFromDeps constructs the ordered bootstrap.Option slice from resolved deps.
func assembleFromDeps(d assembledDeps) []bootstrap.Option {
	opts := []bootstrap.Option{
		bootstrap.WithAssembly(d.assembly),
		bootstrap.WithHTTPAddr(":8080"),
		bootstrap.WithPublisher(d.deps.EventBus),
		bootstrap.WithSubscriber(d.deps.EventBus),
		bootstrap.WithConsumerMiddleware(d.consumerBase.AsMiddleware()),
		bootstrap.WithPublicEndpoints([]string{
			"POST /api/v1/access/sessions/login",
			"POST /api/v1/access/sessions/refresh",
		}),
		bootstrap.WithPasswordResetExemptEndpoints([]string{
			"POST /api/v1/access/users/{id}/password",
			"DELETE /api/v1/access/sessions/{id}",
		}),
		bootstrap.WithPasswordResetChangeEndpointHint("POST /api/v1/access/users/{id}/password"),
		bootstrap.WithAdapterInfo(d.adapterInfo),
		bootstrap.WithRouterOptions(router.WithMetricsHandler(d.metricsHandler)),
		bootstrap.WithMetricsProvider(d.deps.PromStack.metricProvider),
	}
	if d.deps.VerboseToken != "" {
		opts = append(opts, bootstrap.WithVerboseToken(d.deps.VerboseToken))
	}
	if d.deps.PGResource != nil {
		opts = append(opts, bootstrap.WithManagedResource(d.deps.PGResource))
	}
	if d.adminWorkerOpt != nil {
		opts = append(opts, d.adminWorkerOpt)
	}
	if d.deps.InternalGuard != nil {
		opts = append(opts, bootstrap.WithInternalEndpointGuard("/internal/v1/", d.deps.InternalGuard))
	}
	return opts
}

// buildKeyProvider constructs the KeyProvider from GOCELL_KEY_PROVIDER env var.
// Supported values: "local-aes", "vault-transit".
// In memory mode (empty env var) nil is returned (no encryption; NoopTransformer via keyProviderToTransformer).
// In postgres mode (empty env var) a warning is logged and nil is returned —
// operators should set GOCELL_KEY_PROVIDER=local-aes for production.
func buildKeyProvider(storageBackend string) (crypto.KeyProvider, error) {
	providerName := os.Getenv("GOCELL_KEY_PROVIDER")
	if providerName == "" {
		if storageBackend == "postgres" {
			slog.Warn("config-core: GOCELL_KEY_PROVIDER not set; sensitive values will be stored unencrypted",
				slog.String("storage_backend", storageBackend),
				slog.String("recommendation", "set GOCELL_KEY_PROVIDER=local-aes for production"))
		}
		return nil, nil
	}
	switch providerName {
	case "local-aes":
		kp, err := crypto.NewLocalAESKeyProviderFromEnv()
		if err != nil {
			return nil, fmt.Errorf("local-aes key provider: %w", err)
		}
		slog.Info("config-core: key provider initialized", slog.String("provider", "local-aes"))
		return kp, nil
	case "vault-transit":
		kp, err := crypto.NewVaultTransitKeyProviderFromEnv()
		if err != nil {
			return nil, fmt.Errorf("vault-transit key provider: %w", err)
		}
		slog.Info("config-core: key provider initialized", slog.String("provider", "vault-transit"))
		return kp, nil
	default:
		return nil, errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("unknown GOCELL_KEY_PROVIDER %q; known values: \"local-aes\", \"vault-transit\"", providerName))
	}
}

// keyProviderToTransformer wraps a KeyProvider in a ValueTransformer.
// When kp is nil (no encryption configured), returns NoopTransformer.
func keyProviderToTransformer(kp crypto.KeyProvider) crypto.ValueTransformer {
	if kp == nil {
		return crypto.NoopTransformer{}
	}
	return crypto.NewValueTransformer(kp)
}

// buildConfigCoreOpts selects storage-adapter options for config-core based on
// the already-resolved Topology. Returns a ManagedResource (non-nil iff
// postgres mode) and cell options to pass to configcore.NewConfigCore.
//
// Signature reduced from 5 return values (mode, opts, pool, relay, err) to
// 3 (ManagedResource, opts, err). pool + relay lifecycle are now owned by
// *adapterpg.PGResource which satisfies bootstrap.ManagedResource.
//
// Topology is consumed as a parameter rather than re-read from the environment:
// ref: go-zero core/conf/config.go — validate once, pass through; never
// re-read config downstream of the validation boundary.
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/server.go CompletedOptions
// — sealed type threaded through Run ensures downstream receives a validated
// object, not raw env.
//
// ref: Kratos wire — adapter selected at assembly init time, not run time.
// ref: uber-go/fx lifecycle — external resources hook via ManagedResource.
func buildConfigCoreOpts(ctx context.Context, topo bootstrap.Topology, pub outbox.Publisher, metricsProvider metrics.Provider, vt crypto.ValueTransformer) (bootstrap.ManagedResource, []configcore.Option, error) {
	switch topo.StorageBackend {
	case "postgres":
		pool, err := adapterpg.NewPool(ctx, adapterpg.ConfigFromEnv())
		if err != nil {
			return nil, nil, fmt.Errorf("config-core PG pool: %w", err)
		}
		// A12: fail-fast on schema version mismatch.
		if schemaErr := adapterpg.VerifyExpectedVersion(ctx, pool, adapterpg.MigrationsFS()); schemaErr != nil {
			pool.Close()
			return nil, nil, fmt.Errorf("config-core PG schema guard: %w", schemaErr)
		}
		// A4: warn on INVALID indexes (non-fatal).
		if invalid, detectErr := adapterpg.DetectInvalidIndexes(ctx, pool); detectErr != nil {
			slog.Warn("config-core: could not detect invalid indexes", slog.Any("error", detectErr))
		} else if len(invalid) > 0 {
			slog.Warn("config-core: invalid indexes detected; manual cleanup required",
				slog.Any("indexes", invalid))
		}

		outboxWriter := adapterpg.NewOutboxWriter()
		txMgr := adapterpg.NewTxManager(pool)

		relayCfg := outboxruntime.DefaultRelayConfig()
		relayMetrics, rmErr := outbox.NewProviderRelayCollector(metricsProvider, "config-core")
		if rmErr != nil {
			pool.Close()
			return nil, nil, fmt.Errorf("config-core outbox relay metrics: %w", rmErr)
		}
		relayCfg.Metrics = relayMetrics
		pgStore := adapterpg.NewOutboxStore(pool.DB())
		relayWorker := outboxruntime.NewRelay(pgStore, pub, relayCfg)

		pgRes := adapterpg.NewPGResource(pool, relayWorker)
		slog.Info("config-core: using PostgreSQL storage", slog.String("cell_adapter_mode", topo.StorageBackend))
		opts := []configcore.Option{
			configcore.WithPostgresDefaults(pool.DB(), outboxWriter),
			configcore.WithTxManager(txMgr),
			configcore.WithValueTransformer(vt),
		}
		return pgRes, opts, nil

	case "memory":
		slog.Info("config-core: using in-memory storage", slog.String("cell_adapter_mode", topo.StorageBackend))
		return nil, []configcore.Option{configcore.WithInMemoryDefaults()}, nil

	default:
		// Unreachable: TopologyFromEnv validation already rejects unknown
		// StorageBackend values. Keep as defence-in-depth only.
		return nil, nil, errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("buildConfigCoreOpts: unexpected StorageBackend %q (topology validation bypass)", topo.StorageBackend))
	}
}
