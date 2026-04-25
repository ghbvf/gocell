package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	adaptervault "github.com/ghbvf/gocell/adapters/vault"
	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	configcore "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/crypto"
	outboxruntime "github.com/ghbvf/gocell/runtime/outbox"
)

// buildAssembly constructs the corebundle Assembly and registers the three
// cells. Extracted to keep run() cognitive complexity ≤ 15.
func buildAssembly(ps promStack, mode cell.DurabilityMode, cells ...cell.Cell) (*assembly.CoreAssembly, error) {
	asm := assembly.New(assembly.Config{
		ID:              "corebundle",
		DurabilityMode:  mode,
		HookObserver:    ps.hookObserver,
		MetricsProvider: ps.metricProvider,
		// HookTimeout omitted → assembly.DefaultHookTimeout (30s) applies.
	})
	for _, c := range cells {
		if err := asm.Register(c); err != nil {
			return nil, fmt.Errorf("register %s: %w", c.ID(), err)
		}
	}
	return asm, nil
}

func durabilityModeForTopology(topo bootstrap.Topology) cell.DurabilityMode {
	if topo.StorageBackend == "postgres" {
		return cell.DurabilityDurable
	}
	return cell.DurabilityDemo
}

// defaultRuntimeOptions constructs the ordered bootstrap.Option slice from the
// shared cross-cutting deps, a pre-built assembly, a ConsumerBase, a metrics
// handler, and the adapter info map. Called by run() after BuildApp returns.
//
// PGResource options are contributed per-Cell by CellModule.Provide (via
// BuildApp opts). This function covers only the cross-cutting concerns:
// HTTP addr, publisher/subscriber, public/exempt endpoints, metrics, etc.
func defaultRuntimeOptions(
	shared *SharedDeps,
	asm *assembly.CoreAssembly,
	consumerBase *outbox.ConsumerBase,
	metricsHandler http.Handler,
	adapterInfo map[string]string,
) []bootstrap.Option {
	// PR-A14b: three-listener topology — primary (business routes + JWT auth),
	// internal (/internal/v1/* + service-token auth), health (/healthz /readyz
	// /metrics on a dedicated port).
	//
	// Primary listener: PolicyJWTFromAssembly discovers IntentTokenVerifier from
	// accesscore post-Init (lazy on first request, fail-closed).
	// Internal listener: PolicyServiceToken from InternalGuard (nil → PolicyNone
	// in dev mode where GOCELL_SERVICE_SECRET is unset).
	// Health listener: framework-owned /healthz, /readyz, /metrics route groups;
	// when shared.VerboseToken is set, PolicyVerboseToken is attached to the
	// /readyz group so verbose responses require a bearer token.
	//
	// ref: go-kratos/kratos app.go — per-server option pattern.
	internalPolicy := buildInternalPolicy(shared.InternalGuard)

	healthRouteOpts := []bootstrap.HealthRouteGroupOption{
		bootstrap.WithMetricsHandler(metricsHandler),
	}
	if shared.VerboseToken != "" {
		// Two layers consulting the same token (PR-A35 defense-in-depth +
		// PR-A14b R2-01 single-config-source):
		//  - WithReadyzPolicy: PolicyVerboseToken middleware on the route
		//    group 401's at the listener layer.
		//  - WithReadyzVerboseToken: health.Handler's strict gate 401's at
		//    the handler layer.
		healthRouteOpts = append(healthRouteOpts,
			bootstrap.WithReadyzPolicy(
				bootstrap.PolicyVerboseToken("X-Readyz-Token", shared.VerboseToken),
			),
			bootstrap.WithReadyzVerboseToken(shared.VerboseToken),
		)
	}
	if shared.VerboseDisabled {
		healthRouteOpts = append(healthRouteOpts, bootstrap.WithReadyzVerboseDisabled())
	}

	opts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(shared.EventBus),
		bootstrap.WithSubscriber(shared.EventBus),
		bootstrap.WithConsumerMiddleware(consumerBase.AsMiddleware()),
		bootstrap.WithAdapterInfo(adapterInfo),
		bootstrap.WithHealthRoutes(healthRouteOpts...),
		bootstrap.WithMetricsProvider(shared.PromStack.metricProvider),
	}
	// Primary listener carries the JWT auth policy. PolicyJWTFromAssembly
	// resolves an IntentTokenVerifier from an authProvider cell during phase4
	// (Validate hook). Tests that pre-bind their own listener still go through
	// this path — they inject the same listener via extra bootstrap.WithListener
	// options downstream.
	if shared.PrimaryHTTPAddr != "" {
		opts = append(opts, bootstrap.WithListener(
			cell.PrimaryListener, shared.PrimaryHTTPAddr,
			bootstrap.PolicyJWTFromAssembly(asm),
		))
	}
	if shared.InternalHTTPAddr != "" {
		opts = append(opts, bootstrap.WithListener(cell.InternalListener, shared.InternalHTTPAddr, internalPolicy))
	}
	// B2: HealthListener is required when a metrics handler is configured.
	// Production deployments always set GOCELL_HTTP_HEALTH_ADDR.
	// Tests inject their own HealthListener via extra bootstrap.WithListener options.
	if shared.HealthHTTPAddr != "" {
		opts = append(opts, bootstrap.WithListener(cell.HealthListener, shared.HealthHTTPAddr, cell.Policy{}))
	}
	return opts
}

// buildInternalPolicy constructs the policy for the internal listener.
// In dev mode (InternalGuard == nil) PolicyNone is used; in production the
// InternalGuard's store and ring are promoted to PolicyServiceToken.
func buildInternalPolicy(guard *internalGuard) cell.Policy {
	if guard == nil {
		return bootstrap.PolicyNone()
	}
	return bootstrap.PolicyServiceToken(guard.NonceStore(), guard.ring)
}

// buildKeyProvider constructs the KeyProvider from the supplied providerName,
// masterKey, and prevMasterKey (all pre-read from per-cell env by the caller).
//
// Supported providerName values: "local-aes", "vault-transit".
// In memory mode (empty providerName) nil is returned (no encryption;
// NoopTransformer via keyProviderToTransformer).
// In postgres mode (empty providerName) the function fails-fast: sensitive-value
// encryption is a production security invariant; silently wiring NoopTransformer
// would defeat the stated threat model (see docs/architecture/202604191800-adr-config-value-encryption.md).
// Operators must explicitly opt in via GOCELL_CONFIGCORE_KEY_PROVIDER=local-aes
// (dev/CI only) or vault-transit (production).
//
// Note: buildKeyProvider intentionally keeps a positional-argument signature
// (unlike buildCursorCodec / buildHMACKey which use config structs) because
// its input set is small, fixed, and semantically distinct per argument
// (storageBackend determines whether encryption is required at all, the
// other four only matter when providerName == "local-aes"). Wrapping in a
// struct would obscure this branching logic. Revisit if a sixth argument is
// ever needed.
//
// ref: kubernetes/kubernetes pkg/apiserver/admission/config.go — missing
// EncryptionConfig in an active storage path is a startup error, not a warning.
// ref: go-kratos/kratos config.Watch — required dependency failure aborts boot.
func buildKeyProvider(storageBackend, adapterMode, providerName, masterKey, prevMasterKey string) (kcrypto.KeyProvider, error) {
	if providerName == "" {
		if storageBackend == "postgres" {
			return nil, errcode.New(errcode.ErrConfigKeyMissing,
				"configcore: GOCELL_CONFIGCORE_KEY_PROVIDER must be set when StorageBackend=postgres "+
					"(known values: \"local-aes\" for dev/CI, \"vault-transit\" for production). "+
					"Silent NoopTransformer fallback is disabled because it would persist "+
					"sensitive values unencrypted.")
		}
		return nil, nil
	}
	switch providerName {
	case "local-aes":
		// Normalize hex to lowercase before demo-key check: hex.DecodeString is
		// case-insensitive, so "0123ABCD..." and "0123abcd..." produce identical
		// key material. Comparing at string level without normalization would let
		// an uppercase variant of a known demo key slip through.
		if err := rejectDemoKey(adapterMode, "GOCELL_CONFIGCORE_MASTER_KEY", []byte(strings.ToLower(masterKey))); err != nil {
			return nil, err
		}
		if prevMasterKey != "" {
			if err := rejectDemoKey(adapterMode, "GOCELL_CONFIGCORE_MASTER_KEY_PREVIOUS", []byte(strings.ToLower(prevMasterKey))); err != nil {
				return nil, err
			}
		}
		kp, err := crypto.NewLocalAESKeyProviderFromKeys(masterKey, prevMasterKey)
		if err != nil {
			return nil, fmt.Errorf("local-aes key provider: %w", err)
		}
		slog.Info("configcore: key provider initialized", slog.String("provider", "local-aes"))
		return kp, nil
	case "vault-transit":
		kp, err := adaptervault.NewTransitKeyProviderFromEnv(isRealMode(adapterMode))
		if err != nil {
			return nil, fmt.Errorf("vault-transit key provider: %w", err)
		}
		slog.Info("configcore: key provider initialized", slog.String("provider", "vault-transit"))
		return kp, nil
	default:
		return nil, errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("unknown GOCELL_CONFIGCORE_KEY_PROVIDER %q; known values: \"local-aes\", \"vault-transit\"", providerName))
	}
}

// keyProviderToTransformer wraps a KeyProvider in a ValueTransformer.
// When kp is nil (no encryption configured), returns NoopTransformer.
func keyProviderToTransformer(kp kcrypto.KeyProvider) kcrypto.ValueTransformer {
	if kp == nil {
		return crypto.NoopTransformer{}
	}
	return crypto.NewValueTransformer(kp)
}

// buildConfigCoreOpts selects storage-adapter options for configcore based on
// the already-resolved Topology. Returns a ManagedResource (non-nil iff
// postgres mode) and cell options to pass to configcore.NewConfigCore.
//
// pgCfg must be built by the caller (via LoadPGConfig) and passed explicitly;
// buildConfigCoreOpts no longer reads environment variables directly.
// In postgres mode, pgCfg.DSN must be non-empty; an empty DSN causes a
// fail-fast error with a message naming GOCELL_CONFIGCORE_DATABASE_URL.
//
// Signature reduced from 5 return values (mode, opts, pool, relay, err) to
// 3 (ManagedResource, opts, err). pool + relay lifecycle are now owned by
// *adapterpg.PGResource which satisfies lifecycle.ManagedResource (kernel/lifecycle).
//
// ref: go-zero core/conf/config.go — validate once, pass through; never
// re-read config downstream of the validation boundary.
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/server.go CompletedOptions
// — sealed type threaded through Run ensures downstream receives a validated
// object, not raw env.
// ref: Kratos wire — adapter selected at assembly init time, not run time.
// ref: uber-go/fx lifecycle — external resources hook via ManagedResource.
func buildConfigCoreOpts(ctx context.Context, topo bootstrap.Topology, pgCfg adapterpg.Config, pub outbox.Publisher, metricsProvider metrics.Provider, vt kcrypto.ValueTransformer) (kernellifecycle.ManagedResource, []configcore.Option, error) {
	switch topo.StorageBackend {
	case "postgres":
		if pgCfg.DSN == "" {
			return nil, nil, fmt.Errorf("configcore postgres mode requires GOCELL_CONFIGCORE_DATABASE_URL")
		}
		pool, err := adapterpg.NewPool(ctx, pgCfg)
		if err != nil {
			return nil, nil, fmt.Errorf("configcore PG pool: %w", err)
		}
		// A12: fail-fast on schema version mismatch.
		if schemaErr := adapterpg.VerifyExpectedVersion(ctx, pool, adapterpg.MigrationsFS()); schemaErr != nil {
			_ = pool.Close(ctx)
			return nil, nil, fmt.Errorf("configcore PG schema guard: %w", schemaErr)
		}
		// A4: warn on INVALID indexes (non-fatal).
		if invalid, detectErr := adapterpg.DetectInvalidIndexes(ctx, pool); detectErr != nil {
			slog.Warn("configcore: could not detect invalid indexes", slog.Any("error", detectErr))
		} else if len(invalid) > 0 {
			slog.Warn("configcore: invalid indexes detected; manual cleanup required",
				slog.Any("indexes", invalid))
		}

		outboxWriter := adapterpg.NewOutboxWriter()
		txMgr := adapterpg.NewTxManager(pool)

		relayCfg := outboxruntime.DefaultRelayConfig()
		relayMetrics, rmErr := outbox.NewProviderRelayCollector(metricsProvider, "configcore")
		if rmErr != nil {
			_ = pool.Close(ctx)
			return nil, nil, fmt.Errorf("configcore outbox relay metrics: %w", rmErr)
		}
		relayCfg.Metrics = relayMetrics
		pgStore := adapterpg.NewOutboxStore(pool.DB())
		relayWorker := outboxruntime.NewRelay(pgStore, pub, relayCfg)

		pgRes, resErr := adapterpg.NewPGResource(pool, relayWorker)
		if resErr != nil {
			_ = pool.Close(ctx)
			return nil, nil, fmt.Errorf("configcore PG resource: %w", resErr)
		}
		slog.Info("configcore: using PostgreSQL storage", slog.String("cell_adapter_mode", topo.StorageBackend))
		opts := []configcore.Option{
			configcore.WithPostgresPool(pool.DB()),
			// PG adapter path: publisher + real outbox.Writer compose a
			// WriterEmitter at Cell boundary; L2 transactional atomicity applies.
			configcore.WithOutboxDeps(pub, outboxWriter),
			configcore.WithTxManager(txMgr),
			configcore.WithValueTransformer(vt),
		}
		return pgRes, opts, nil

	case "memory":
		slog.Info("configcore: using in-memory storage", slog.String("cell_adapter_mode", topo.StorageBackend))
		return nil, []configcore.Option{
			configcore.WithInMemoryDefaults(),
			// Memory adapter path: publisher only, writer=nil → DirectEmitter.
			configcore.WithOutboxDeps(pub, nil),
		}, nil

	default:
		// Unreachable: TopologyFromEnv validation already rejects unknown
		// StorageBackend values. Keep as defence-in-depth only.
		return nil, nil, errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("buildConfigCoreOpts: unexpected StorageBackend %q (topology validation bypass)", topo.StorageBackend))
	}
}

// logInitialAdminCredPath emits a startup info log so operators know where to
// find the initial admin credential on first run. Uses
// accesscore.ResolveBootstrapCredentialPath so the logged path always matches
// the path actually written by the bootstrapper (P2-6: no duplicated path
// resolution logic).
func logInitialAdminCredPath() {
	credPath, err := accesscore.ResolveBootstrapCredentialPath("")
	if err != nil {
		// GOCELL_STATE_DIR is not absolute — the bootstrapper will fail-fast too,
		// so log the error here and let the user fix the config.
		slog.Warn("corebundle: invalid GOCELL_STATE_DIR; initial admin credential path unresolvable",
			slog.String("error", err.Error()))
		return
	}
	slog.Info("corebundle: starting; if first run, initial admin credentials are written to "+credPath,
		slog.String("cred_path", credPath))
}

// buildConsumerBase constructs the in-process ConsumerBase for outbox
// consumer middleware. Uses an in-memory Claimer (idempotency.NewInMemClaimer).
func buildConsumerBase() (*outbox.ConsumerBase, error) {
	cb, err := outbox.NewConsumerBase(idempotency.NewInMemClaimer(), outbox.ConsumerBaseConfig{})
	if err != nil {
		return nil, fmt.Errorf("construct ConsumerBase: %w", err)
	}
	return cb, nil
}
