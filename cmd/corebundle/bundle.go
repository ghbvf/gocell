package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
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
	"github.com/ghbvf/gocell/runtime/http/router"
	outboxruntime "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/worker"
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
	if topo.AdapterMode == "real" {
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
	opts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		bootstrap.WithHTTPAddr(":8080"),
		bootstrap.WithPublisher(shared.EventBus),
		bootstrap.WithSubscriber(shared.EventBus),
		bootstrap.WithConsumerMiddleware(consumerBase.AsMiddleware()),
		// Public routes and password-reset-exempt routes are declared by the
		// owning Cells via auth.Declare (see cells/accesscore/cell.go and
		// cells/accesscore/slices/identitymanage/handler.go). Bootstrap only
		// needs the opt-in signal that an auth provider cell will be wired.
		bootstrap.WithAuthDiscovery(),
		bootstrap.WithAdapterInfo(adapterInfo),
		bootstrap.WithRouterOptions(router.WithMetricsHandler(metricsHandler)),
		bootstrap.WithMetricsProvider(shared.PromStack.metricProvider),
	}
	if shared.VerboseToken != "" {
		opts = append(opts, bootstrap.WithVerboseToken(shared.VerboseToken))
	}
	if shared.InternalGuard != nil {
		opts = append(opts, bootstrap.WithInternalEndpointGuard("/internal/v1/", shared.InternalGuard))
	}
	return opts
}

// buildKeyProvider constructs the KeyProvider from the given providerName
// (pre-read from GOCELL_KEY_PROVIDER by the caller). Supported values:
// "local-aes", "vault-transit".
// In memory mode (empty providerName) nil is returned (no encryption;
// NoopTransformer via keyProviderToTransformer).
// In postgres mode (empty providerName) the function fails-fast: sensitive-value
// encryption is a production security invariant; silently wiring NoopTransformer
// would defeat the stated threat model (see docs/architecture/202604191800-adr-config-value-encryption.md).
// Operators must explicitly opt in via GOCELL_KEY_PROVIDER=local-aes (dev/CI only) or
// vault-transit (production).
//
// ref: kubernetes/kubernetes pkg/apiserver/admission/config.go — missing
// EncryptionConfig in an active storage path is a startup error, not a warning.
// ref: go-kratos/kratos config.Watch — required dependency failure aborts boot.
func buildKeyProvider(storageBackend, adapterMode, providerName string) (kcrypto.KeyProvider, error) {
	if providerName == "" {
		if storageBackend == "postgres" {
			return nil, errcode.New(errcode.ErrConfigKeyMissing,
				"configcore: GOCELL_KEY_PROVIDER must be set when StorageBackend=postgres "+
					"(known values: \"local-aes\" for dev/CI, \"vault-transit\" for production). "+
					"Silent NoopTransformer fallback is disabled because it would persist "+
					"sensitive values unencrypted.")
		}
		return nil, nil
	}
	switch providerName {
	case "local-aes":
		masterKeyRaw := os.Getenv("GOCELL_MASTER_KEY")
		// Normalize hex to lowercase before demo-key check: hex.DecodeString is
		// case-insensitive, so "0123ABCD..." and "0123abcd..." produce identical
		// key material. Comparing at string level without normalization would let
		// an uppercase variant of a known demo key slip through.
		if err := rejectDemoKey(adapterMode, "GOCELL_MASTER_KEY", []byte(strings.ToLower(masterKeyRaw))); err != nil {
			return nil, err
		}
		kp, err := crypto.NewLocalAESKeyProviderFromEnv()
		if err != nil {
			return nil, fmt.Errorf("local-aes key provider: %w", err)
		}
		slog.Info("configcore: key provider initialized", slog.String("provider", "local-aes"))
		return kp, nil
	case "vault-transit":
		kp, err := adaptervault.NewTransitKeyProviderFromEnv()
		if err != nil {
			return nil, fmt.Errorf("vault-transit key provider: %w", err)
		}
		slog.Info("configcore: key provider initialized", slog.String("provider", "vault-transit"))
		return kp, nil
	default:
		return nil, errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("unknown GOCELL_KEY_PROVIDER %q; known values: \"local-aes\", \"vault-transit\"", providerName))
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
// Signature reduced from 5 return values (mode, opts, pool, relay, err) to
// 3 (ManagedResource, opts, err). pool + relay lifecycle are now owned by
// *adapterpg.PGResource which satisfies lifecycle.ManagedResource (kernel/lifecycle).
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
func buildConfigCoreOpts(ctx context.Context, topo bootstrap.Topology, pub outbox.Publisher, metricsProvider metrics.Provider, vt kcrypto.ValueTransformer) (kernellifecycle.ManagedResource, []configcore.Option, error) {
	switch topo.StorageBackend {
	case "postgres":
		pool, err := adapterpg.NewPool(ctx, adapterpg.ConfigFromEnv())
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
			configcore.WithPostgresDefaults(pool.DB(), outboxWriter),
			configcore.WithTxManager(txMgr),
			configcore.WithValueTransformer(vt),
		}
		return pgRes, opts, nil

	case "memory":
		slog.Info("configcore: using in-memory storage", slog.String("cell_adapter_mode", topo.StorageBackend))
		return nil, []configcore.Option{configcore.WithInMemoryDefaults()}, nil

	default:
		// Unreachable: TopologyFromEnv validation already rejects unknown
		// StorageBackend values. Keep as defence-in-depth only.
		return nil, nil, errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("buildConfigCoreOpts: unexpected StorageBackend %q (topology validation bypass)", topo.StorageBackend))
	}
}

// adminBootstrapWorkerOpts wires WithInitialAdminBootstrap + WithBootstrapWorkerSink
// onto the given base accesscore options and returns the extended options together
// with a bootstrap.Option that lazily adds the cleanup worker to the bootstrap
// WorkerGroup.
//
// Lifecycle ordering: the sink fires inside asm.StartWithConfig (Step 3-4 of
// bootstrap.Run), before the WorkerGroup starts (Step 8). worker.Lazy() resolves
// the worker at Start() time — after the assembly has Init'd.
//
// When no admin exists: sink fires, adminWorker is non-nil, cleaner runs.
// When admin already exists: sink is not called, LazyWorker.Start/Stop are no-ops.
//
// Thread safety: Set (writer) and Start/Stop (readers) synchronise via
// atomic.Pointer inside worker.LazyWorker (F-OPS-2).
//
// ref: docs/architecture/202604181900-adr-auth-setup-first-run.md (scheme H)
func adminBootstrapWorkerOpts(base []accesscore.Option, bootstrapOpts ...accesscore.InitialAdminOption) (accessOpts []accesscore.Option, lazyWorkerOpt bootstrap.Option) {
	lazy := worker.Lazy()
	sink := func(w worker.Worker) { _ = lazy.Set(w) }
	accessOpts = append(base,
		accesscore.WithInitialAdminBootstrap(bootstrapOpts...),
		accesscore.WithBootstrapWorkerSink(sink),
	)
	lazyWorkerOpt = bootstrap.WithWorkers(lazy)
	return accessOpts, lazyWorkerOpt
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
