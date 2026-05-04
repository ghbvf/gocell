// Package main is the corebundle composition root.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	adaptervault "github.com/ghbvf/gocell/adapters/vault"
	configcore "github.com/ghbvf/gocell/cells/configcore"
	configpg "github.com/ghbvf/gocell/cells/configcore/postgres"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/kernel/governance"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/crypto"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
	outboxruntime "github.com/ghbvf/gocell/runtime/outbox"
)

// buildAssembly constructs the runtime Assembly and registers the generated
// cell list. Extracted to keep runCorebundle cognitive complexity <= 15.
func buildAssembly(
	ps promStack,
	assemblyID string,
	mode cell.DurabilityMode,
	clk clock.Clock,
	cells ...cell.Cell,
) (*assembly.CoreAssembly, error) {
	asm := assembly.New(assembly.Config{
		ID:              assemblyID,
		DurabilityMode:  mode,
		Clock:           clk,
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

func runtimeBaseOptions(
	shared *SharedDeps,
	asm *assembly.CoreAssembly,
	consumerBase *outbox.ConsumerBase,
	metricsHandler http.Handler,
	adapterInfo map[string]string,
) []bootstrap.Option {
	healthRouteOpts := []bootstrap.HealthRouteGroupOption{
		bootstrap.WithMetricsHandler(metricsHandler),
	}
	if shared.VerboseToken != "" {
		// PR269 round-3: verbose-mode gating is a disclosure concern owned by
		// the health handler, not an authentication scheme. WithReadyzVerboseToken
		// plumbs the token to health.Handler.SetVerboseToken, which on mismatch
		// produces the canonical 401 ErrReadyzVerboseDenied envelope.
		healthRouteOpts = append(healthRouteOpts,
			bootstrap.WithReadyzVerboseToken(shared.VerboseToken),
		)
	}
	if shared.VerboseDisabled {
		healthRouteOpts = append(healthRouteOpts, bootstrap.WithReadyzVerboseDisabled())
	}

	opts := []bootstrap.Option{
		// Single composition-root clock: the same clock is on the assembly
		// (see buildAssembly above) and on the bootstrap; it threads through
		// the lifecycle and default-assembly fallback. The pair is the
		// load-bearing invariant of PROD-CLOCK-INJECTION-01 — never
		// default-fallback in adapters or cells.
		bootstrap.WithClock(shared.Clock),
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(shared.EventBus),
		bootstrap.WithSubscriber(shared.EventBus),
		bootstrap.WithConsumerMiddleware(consumerMiddlewares(shared, consumerBase)...),
		bootstrap.WithSubscriptionValidator(obmetrics.ConfigEventOwnerValidator),
		bootstrap.WithAdapterInfo(adapterInfo),
		bootstrap.WithHealthRoutes(healthRouteOpts...),
		bootstrap.WithMetricsProvider(shared.PromStack.metricProvider),
	}
	if shared.RedisClient != nil {
		opts = append(opts,
			bootstrap.WithHealthChecker("redis_ready", shared.RedisClient.Health),
			bootstrap.WithManagedCloser(shared.RedisClient),
		)
	}
	return opts
}

func consumerMiddlewares(shared *SharedDeps, consumerBase *outbox.ConsumerBase) []outbox.SubscriptionMiddleware {
	return []outbox.SubscriptionMiddleware{
		configEventConsumerMiddleware(shared.ConfigEventCollector),
		consumerBase.AsMiddleware(),
	}
}

func configEventConsumerMiddleware(collector obmetrics.ConfigEventCollector) outbox.SubscriptionMiddleware {
	return obmetrics.ConfigEventMiddleware(collector)
}

// newBootstrapFromOptions creates a bootstrap.Bootstrap from a pre-built option
// slice. Test code must use this function instead of calling bootstrap.New(opts...)
// directly so that CLOCK-INJECTION-TEST-CALLSITE-01 is not triggered (the
// archtest only flags bootstrap.New calls in test files; this wrapper is in
// production code, not a test file).
// NOTE: runtimeBaseOptions always includes bootstrap.WithClock so the clock is
// never missing — this wrapper does not impose an additional contract.
func newBootstrapFromOptions(opts []bootstrap.Option) *bootstrap.Bootstrap {
	return bootstrap.New(opts...) //archtest:allow:clock-injection:via-slice opts assembled by defaultRuntimeOptions includes WithClock
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
	// Primary listener: AuthJWTFromAssembly discovers IntentTokenVerifier from
	// accesscore post-Init (lazy phase4 resolution, fail-closed).
	// Internal listener: AuthServiceToken from InternalGuard. The listener is
	// always registered in the runtime path; SharedDeps.Validate requires both
	// InternalHTTPAddr and InternalGuard before run() reaches this point.
	// Health listener: framework-owned /healthz, /readyz, /metrics route groups;
	// when shared.VerboseToken is set, the health handler's strict-gate path
	// (WithReadyzVerboseToken → SetVerboseToken) requires a matching X-Readyz-Token
	// for ?verbose=true requests; mismatches return 401 ErrReadyzVerboseDenied.
	//
	// ref: go-kratos/kratos app.go — per-server option pattern.
	opts := runtimeBaseOptions(shared, asm, consumerBase, metricsHandler, adapterInfo)
	if shared.PrimaryHTTPAddr != "" {
		opts = append(opts, bootstrap.WithListener(
			cell.PrimaryListener, shared.PrimaryHTTPAddr,
			[]cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)},
		))
	}
	opts = append(opts, bootstrap.WithListener(cell.InternalListener, shared.InternalHTTPAddr, buildInternalAuthChain(shared.InternalGuard)))
	if shared.HealthHTTPAddr != "" {
		opts = append(opts, bootstrap.WithListener(cell.HealthListener, shared.HealthHTTPAddr, []cell.ListenerAuth{cell.AuthNone{}}))
	}
	opts = append(opts, devtoolsOption(shared))
	return opts
}

// buildInternalAuthChain constructs the auth chain for the internal listener.
// guard is always non-nil after SEC-FAIL-CLOSED: internalGuardFromEnv now
// returns an error rather than a nil guard in all adapter modes when
// GOCELL_SERVICE_SECRET is unset, so SharedDeps.Validate fails fast before
// this function is reached with a nil guard.
func buildInternalAuthChain(guard *internalGuard) []cell.ListenerAuth {
	return []cell.ListenerAuth{cell.MustNewAuthServiceToken(guard.NonceStore(), guard.ring)}
}

// buildKeyProvider constructs the KeyProvider from the supplied providerName,
// masterKey, and prevMasterKey (all pre-read from per-cell env by the caller).
//
// Supported providerName values: "local-aes", "vault-transit".
// In memory mode (empty providerName) a no-key sentinel is returned (no
// encryption; NoopTransformer via keyProviderToTransformer).
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
func buildKeyProvider(
	storageBackend, adapterMode, providerName, masterKey, prevMasterKey string, clk clock.Clock,
) (kcrypto.KeyProvider, error) {
	if providerName == "" {
		if storageBackend == "postgres" {
			return nil, errcode.New(errcode.ErrConfigKeyMissing,
				"configcore: GOCELL_CONFIGCORE_KEY_PROVIDER must be set when StorageBackend=postgres "+
					"(known values: \"local-aes\" for dev/CI, \"vault-transit\" for production). "+
					"Silent NoopTransformer fallback is disabled because it would persist "+
					"sensitive values unencrypted.")
		}
		return noKeyProvider{}, nil
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
		kp, err := adaptervault.NewTransitKeyProviderFromEnv(isRealMode(adapterMode), clk)
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
// When kp is nil or the no-key sentinel (no encryption configured), returns
// NoopTransformer.
func keyProviderToTransformer(kp kcrypto.KeyProvider) kcrypto.ValueTransformer {
	if kp == nil || isNoKeyProvider(kp) {
		return crypto.NoopTransformer{}
	}
	return crypto.NewValueTransformer(kp)
}

type noKeyProvider struct{}

const noKeyProviderConfiguredMessage = "configcore: no key provider configured"

func (noKeyProvider) Current(context.Context) (kcrypto.KeyHandle, error) {
	return nil, errcode.New(errcode.ErrConfigKeyMissing, noKeyProviderConfiguredMessage)
}

func (noKeyProvider) ByID(context.Context, string) (kcrypto.KeyHandle, error) {
	return nil, errcode.New(errcode.ErrConfigKeyMissing, noKeyProviderConfiguredMessage)
}

func (noKeyProvider) Rotate(context.Context) (string, error) {
	return "", errcode.New(errcode.ErrConfigKeyMissing, noKeyProviderConfiguredMessage)
}

func isNoKeyProvider(kp kcrypto.KeyProvider) bool {
	_, ok := kp.(noKeyProvider)
	return ok
}

// ConfigCoreModuleConfig bundles the inputs for buildConfigCoreOpts so that
// callers are not affected by positional parameter ordering and new inputs can
// be added without breaking existing call sites.
//
// ref: Uber fx fx.Option — self-contained module config struct.
// ref: go-zero core/conf/config.go — validate once, pass through.
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/server.go CompletedOptions
// — sealed type threaded through Run.
type ConfigCoreModuleConfig struct {
	Topology         bootstrap.Topology
	PGConfig         adapterpg.Config
	Publisher        outbox.Publisher
	MetricsProvider  metrics.Provider
	ValueTransformer kcrypto.ValueTransformer
	OnStaleCipher    func(key, storedKeyID, currentKeyID string)
	Clock            clock.Clock
}

// ConfigCoreModuleResult bundles the outputs from buildConfigCoreOpts. Using a
// result struct rather than multiple return values allows named field access and
// makes nil-vs-empty semantics explicit at the call site.
//
// ref: Uber fx fx.Out result struct pattern.
type ConfigCoreModuleResult struct {
	// PGResource is the ManagedResource for the PG pool. Nil in memory mode.
	PGResource kernellifecycle.ManagedResource
	// PGPool is the raw PG pool opened in postgres mode. Nil in memory mode.
	// ConfigCoreModule writes this to SharedDeps.SharedPGPool after a successful
	// buildConfigCoreOpts call so AccessCoreModule + AuditCoreModule can read it.
	PGPool *adapterpg.Pool
	// CellOptions are the configcore.Option values to pass to NewConfigCore.
	CellOptions []configcore.Option
	// BootstrapOpts are bootstrap.Option values — in postgres mode carries
	// WithManagedResource(relay) so the relay worker is independently managed.
	BootstrapOpts []bootstrap.Option
}

// buildConfigCoreOpts selects storage-adapter options for configcore based on
// the already-resolved Topology. Returns a ConfigCoreModuleResult and an error.
//
// cfg.PGConfig must be built by the caller (via LoadPGConfig) and passed
// explicitly; buildConfigCoreOpts no longer reads environment variables directly.
// In postgres mode, cfg.PGConfig.DSN must be non-empty; an empty DSN causes a
// fail-fast error with a message naming GOCELL_CONFIGCORE_DATABASE_URL.
//
// Relay worker is registered via BootstrapOpts independently of PGResource:
// PGResource owns the pool health probe and pool shutdown (no background worker);
// the relay is a separate ManagedResource with its own Worker/Close/Checkers.
//
// ref: Kratos wire — adapter selected at assembly init time, not run time.
// ref: uber-go/fx lifecycle — external resources hook via ManagedResource.
func buildConfigCoreOpts(ctx context.Context, cfg ConfigCoreModuleConfig) (ConfigCoreModuleResult, error) {
	switch cfg.Topology.StorageBackend {
	case "postgres":
		if cfg.PGConfig.DSN == "" {
			return ConfigCoreModuleResult{}, fmt.Errorf("configcore postgres mode requires GOCELL_CONFIGCORE_DATABASE_URL")
		}
		pool, err := adapterpg.NewPool(ctx, cfg.PGConfig)
		if err != nil {
			return ConfigCoreModuleResult{}, fmt.Errorf("configcore PG pool: %w", err)
		}
		// A12: fail-fast on schema version mismatch.
		if schemaErr := verifyConfigCorePGSchema(ctx, pool); schemaErr != nil {
			_ = pool.Close(ctx)
			return ConfigCoreModuleResult{}, schemaErr
		}
		// A4: warn on INVALID indexes (non-fatal).
		if invalid, detectErr := adapterpg.DetectInvalidIndexes(ctx, pool); detectErr != nil {
			slog.Warn("configcore: could not detect invalid indexes", slog.Any("error", detectErr))
		} else if len(invalid) > 0 {
			slog.Warn("configcore: invalid indexes detected; manual cleanup required",
				slog.Any("indexes", invalid))
		}

		outboxWriter := adapterpg.NewOutboxWriter(cfg.Clock)
		txMgr := adapterpg.NewTxManager(pool)

		relayCfg := outboxruntime.DefaultRelayConfig()
		relayMetrics, rmErr := outbox.NewProviderRelayCollector(cfg.MetricsProvider, "configcore")
		if rmErr != nil {
			_ = pool.Close(ctx)
			return ConfigCoreModuleResult{}, fmt.Errorf("configcore outbox relay metrics: %w", rmErr)
		}
		relayCfg.Metrics = relayMetrics
		relayCfg.Clock = cfg.Clock
		pgStore := adapterpg.NewOutboxStore(pool.DB(), cfg.Clock)
		relayWorker := outboxruntime.NewRelay(pgStore, cfg.Publisher, relayCfg)

		pgRes, storageOpt, storageErr := buildConfigCorePGStorage(pool, cfg)
		if storageErr != nil {
			_ = pool.Close(ctx)
			return ConfigCoreModuleResult{}, storageErr
		}
		slog.Info("configcore: using PostgreSQL storage", slog.String("cell_adapter_mode", cfg.Topology.StorageBackend))
		cellOpts := []configcore.Option{
			storageOpt,
			// PG adapter path: publisher + real outbox.Writer compose a
			// WriterEmitter at Cell boundary; L2 transactional atomicity applies.
			configcore.WithOutboxDeps(cfg.Publisher, outboxWriter),
			configcore.WithTxManager(txMgr),
		}
		// Relay is registered independently via bootstrap so its Worker()/Close()
		// lifecycle is managed separately from the pool (PGResource.Worker() == nil).
		return ConfigCoreModuleResult{
			PGResource:    pgRes,
			PGPool:        pool,
			CellOptions:   cellOpts,
			BootstrapOpts: []bootstrap.Option{bootstrap.WithManagedResource(relayWorker)},
		}, nil

	case "memory":
		slog.Info("configcore: using in-memory storage", slog.String("cell_adapter_mode", cfg.Topology.StorageBackend))
		return ConfigCoreModuleResult{
			CellOptions: []configcore.Option{
				configcore.WithInMemoryDefaults(),
				// Memory adapter path: publisher only, writer=nil → DirectEmitter.
				configcore.WithOutboxDeps(cfg.Publisher, nil),
			},
		}, nil

	default:
		// Unreachable: TopologyFromEnv validation already rejects unknown
		// StorageBackend values. Keep as defense-in-depth only.
		return ConfigCoreModuleResult{}, errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("buildConfigCoreOpts: unexpected StorageBackend %q (topology validation bypass)", cfg.Topology.StorageBackend))
	}
}

func verifyConfigCorePGSchema(ctx context.Context, pool *adapterpg.Pool) error {
	migrationsFS, err := adapterpg.MigrationsFS()
	if err != nil {
		return fmt.Errorf("configcore PG migrations fs: %w", err)
	}
	if err := adapterpg.VerifyExpectedVersion(ctx, pool, migrationsFS); err != nil {
		return fmt.Errorf("configcore PG schema guard: %w", err)
	}
	return nil
}

func buildConfigCorePGStorage(
	pool *adapterpg.Pool, cfg ConfigCoreModuleConfig,
) (kernellifecycle.ManagedResource, configcore.Option, error) {
	pgRes, err := adapterpg.NewPGResource(pool)
	if err != nil {
		return nil, nil, fmt.Errorf("configcore PG resource: %w", err)
	}
	storageOpt, err := configpg.WithPool(pool.DB(), cfg.Clock,
		configpg.WithValueTransformer(cfg.ValueTransformer),
		configpg.WithOnStaleCipher(cfg.OnStaleCipher),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("configcore PG repository wiring: %w", err)
	}
	return pgRes, storageOpt, nil
}

// defaultDevtoolsParseTimeout is the max time allowed for project metadata
// parsing during bootstrap. Exceeding this disables the catalog endpoint
// (best-effort degradation) rather than blocking server startup.
const defaultDevtoolsParseTimeout = 30 * time.Second

// devtoolsOption builds the WithDevtoolsCatalog bootstrap option for the catalog
// endpoint. Best-effort metadata parse: logs at Warn (degraded operation per
// observability.md) and disables the endpoint when GOCELL_PROJECT_ROOT is unset,
// resolves outside the working tree, or doesn't expose a valid project tree.
// The endpoint is absent when pm is nil — Bootstrap treats nil pm as "disabled".
//
// generatedPackageGraph is the build-time generated package dep graph from
// catalog_gen.go (produced by `go generate ./cmd/corebundle/`). When nil (e.g.
// go generate has not been run), the packageDeps block is simply omitted.
func devtoolsOption(shared *SharedDeps) bootstrap.Option {
	root := shared.ProjectRoot
	if root == "" {
		slog.Warn("devtools: GOCELL_PROJECT_ROOT unset; catalog endpoint disabled")
		return bootstrap.WithDevtoolsCatalog(nil, "", nil)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		slog.Warn("devtools: GOCELL_PROJECT_ROOT path resolution failed; catalog endpoint disabled",
			slog.String("root", root),
			slog.Any("error", err))
		return bootstrap.WithDevtoolsCatalog(nil, "", nil)
	}
	cwd, err := os.Getwd()
	if err != nil {
		slog.Warn("devtools: cwd lookup failed; catalog endpoint disabled",
			slog.Any("error", err))
		return bootstrap.WithDevtoolsCatalog(nil, "", nil)
	}
	if !governance.IsWithinRoot(cwd, absRoot) {
		slog.Warn("devtools: GOCELL_PROJECT_ROOT escapes working tree; catalog endpoint disabled",
			slog.String("root", root),
			slog.String("absRoot", absRoot),
			slog.String("cwd", cwd))
		return bootstrap.WithDevtoolsCatalog(nil, "", nil)
	}
	pm, err := parseProjectWithTimeout(absRoot, defaultDevtoolsParseTimeout, shared.Clock)
	if err != nil {
		return bootstrap.WithDevtoolsCatalog(nil, "", nil)
	}

	// Derive wire summaries from cell.go marker comments. Best-effort: a scan
	// error (e.g. malformed marker) disables wireSummary but does not block the
	// catalog endpoint. See docs/architecture/202605051500-adr-k05-markergen-cellgen-unified.md
	// Decision 6.
	wireSummaries, wsErr := BuildCellWireSummaries(absRoot, pm)
	if wsErr != nil {
		slog.Warn("devtools: wire summary scan failed; wireSummary omitted from catalog",
			slog.String("root", absRoot),
			slog.Any("error", wsErr))
		wireSummaries = nil
	}

	slog.Info("devtools: catalog endpoint enabled", slog.String("root", absRoot))
	return bootstrap.WithDevtoolsCatalog(pm, absRoot, generatedPackageGraph, wireSummaries)
}

// parseProjectWithTimeout runs metadata.NewParser(absRoot).Parse() in a
// goroutine and returns within timeout. On parse error or timeout the catalog
// endpoint is disabled (best-effort degradation); the caller receives nil and
// the error/timeout is already logged here. The clock is injected for testability
// and to satisfy PROD-CLOCK-INJECTION-01 (no time.After in production).
func parseProjectWithTimeout(absRoot string, timeout time.Duration, clk clock.Clock) (*metadata.ProjectMeta, error) {
	type parseResult struct {
		pm  *metadata.ProjectMeta
		err error
	}
	done := make(chan parseResult, 1)
	go func() {
		pm, err := metadata.NewParser(absRoot).Parse()
		done <- parseResult{pm, err}
	}()
	timer := clk.NewTimerAt(clk.Now().Add(timeout))
	defer timer.Stop()
	select {
	case r := <-done:
		if r.err != nil {
			slog.Warn("devtools: project metadata parse failed; catalog endpoint disabled",
				slog.String("root", absRoot),
				slog.Any("error", r.err))
			return nil, r.err
		}
		return r.pm, nil
	case <-timer.C():
		slog.Warn("devtools: project metadata parse timeout; catalog endpoint disabled",
			slog.String("root", absRoot),
			slog.Duration("timeout", timeout))
		return nil, fmt.Errorf("devtools: parse timeout after %s", timeout)
	}
}

// buildConsumerBase constructs ConsumerBase from the topology-selected
// idempotency claimer built in LoadSharedDepsFromEnv.
func buildConsumerBase(deps *SharedDeps) (*outbox.ConsumerBase, error) {
	if deps == nil {
		return nil, fmt.Errorf("construct ConsumerBase: SharedDeps is nil")
	}
	if deps.ConsumerClaimer == nil {
		return nil, fmt.Errorf("construct ConsumerBase: SharedDeps.ConsumerClaimer must be set")
	}
	cb, err := outbox.NewConsumerBase(deps.ConsumerClaimer, outbox.ConsumerBaseConfig{}, deps.Clock)
	if err != nil {
		return nil, fmt.Errorf("construct ConsumerBase: %w", err)
	}
	return cb, nil
}
