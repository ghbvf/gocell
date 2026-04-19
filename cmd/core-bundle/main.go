// Package main is the entry point for the core-bundle assembly.
// It bootstraps config-core, access-core, and audit-core with in-memory
// repositories by default, suitable for development and integration testing.
//
// DurabilityDurable is set to reject noop placeholders (NoopWriter,
// NoopTxRunner, DiscardPublisher) even in dev mode. Set GOCELL_ADAPTER_MODE=real
// to require all secrets from env vars (fail-fast on missing).
//
// # Required env vars (all adapter modes)
//
//   - GOCELL_JWT_ISSUER: JWT iss claim written into tokens and verified on
//     inbound requests via VerifyIntent. Must be set before startup.
//
//   - GOCELL_JWT_AUDIENCE: JWT aud claim written into tokens and verified on
//     inbound requests via VerifyIntent. Must be set before startup.
//
// # Required env vars (real adapter mode only)
//
//   - GOCELL_SERVICE_SECRET: HMAC-SHA256 secret (≥32 bytes) protecting
//     /internal/v1/* paths via ServiceTokenMiddleware. Missing in real mode
//     aborts startup; missing in dev mode disables the guard with a Warn log.
//
// See also: docs/ops/env-vars.md for the full env var reference.
package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	adapterprom "github.com/ghbvf/gocell/adapters/prometheus"
	accesscore "github.com/ghbvf/gocell/cells/access-core"
	auditcore "github.com/ghbvf/gocell/cells/audit-core"
	configcore "github.com/ghbvf/gocell/cells/config-core"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	authconfig "github.com/ghbvf/gocell/runtime/auth/config"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/router"
	outboxruntime "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/worker"
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// loadSecret loads a secret from the given environment variable. In "real"
// adapter mode, the env var is required and missing values are a hard error.
// In dev mode, missing values fall back to devDefault with a warning.
//
// ref: Kubernetes two-phase validation — Complete then Validate, both fail-fast.
func loadSecret(envKey, devDefault, adapterMode string) ([]byte, error) {
	if v := os.Getenv(envKey); v != "" {
		return []byte(v), nil
	}
	if adapterMode == "real" {
		return nil, fmt.Errorf("%s must be set in adapter mode \"real\"", envKey)
	}
	slog.Warn("using dev-only default; set env var for production",
		slog.String("var", envKey),
		slog.String("mode", "dev-fallback"),
		slog.String("action_required", "set env var before real mode"))
	return []byte(devDefault), nil
}

// loadKeySet returns a KeySet, preferring environment-provided keys.
// In "real" adapter mode, env keys are required (fail-fast if missing).
// In dev mode, env keys are used if available; otherwise an ephemeral RSA
// key pair is generated per process (tokens invalidated on restart).
//
// ref: Kubernetes kube-apiserver refuses to start without --service-account-key-file.
func loadKeySet(adapterMode string) (*auth.KeySet, error) {
	// Prefer env-provided keys regardless of adapter mode.
	ks, err := auth.LoadKeySetFromEnv()
	if err == nil {
		slog.Info("JWT key set loaded from environment variables")
		return ks, nil
	}
	if adapterMode == "real" {
		return nil, fmt.Errorf("real adapter mode requires JWT key env vars: %w", err)
	}
	// Dev mode: ephemeral keys (acceptable for development only).
	privKey, pubKey := auth.MustGenerateTestKeyPair()
	slog.Warn("dev mode: using ephemeral RSA key pair; tokens will be invalidated on restart")
	return auth.NewKeySet(privKey, pubKey)
}

// metricsTokenHeader names the request header used to authenticate
// /metrics scrapers when a token is configured. Mirrors the X-Readyz-Token
// convention for /readyz?verbose — keeping the same shape for all
// control-plane endpoints lets operators standardise scraper config.
const metricsTokenHeader = "X-Metrics-Token"

// withMetricsTokenGuard wraps h so requests without a matching
// X-Metrics-Token header are rejected with 401 Unauthorized. Uses
// crypto/subtle.ConstantTimeCompare to avoid timing side channels on
// token comparison.
func withMetricsTokenGuard(token string, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get(metricsTokenHeader)), []byte(token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// loadCursorCodec loads a cursor HMAC secret from envName (with a dev-only
// fallback to devDefault) and constructs a CursorCodec. In "real" adapter
// mode the secret must be set and must not match a well-known demo value.
//
// When prevEnvName is non-empty and that env var is set, the value is loaded
// as the previous (verification-only) key to enable the kube-apiserver-style
// rotation lifecycle: decode tries current first, then previous. The previous
// key is subject to the same demo-key guard as current; failures at any stage
// are fail-fast (no silent fallback to single-key mode). If the previous env
// is unset, the codec is constructed in single-key mode.
//
// label is used in wrapping error messages.
//
// ref: kube-apiserver --service-account-signing-key-file (single current) +
// --service-account-key-file (multi verification) — same signing/verification
// split applied to cursor HMAC tokens.
// ref: gorilla/securecookie CodecsFromPairs — ordered key list, first match
// wins during decode.
func loadCursorCodec(adapterMode, envName, prevEnvName, devDefault, label string) (*query.CursorCodec, error) {
	key, err := loadSecret(envName, devDefault, adapterMode)
	if err != nil {
		return nil, fmt.Errorf("%s cursor key: %w", label, err)
	}
	if err := rejectDemoKey(adapterMode, envName, key); err != nil {
		return nil, err
	}

	var prevKey []byte
	if prevEnvName != "" {
		if v := os.Getenv(prevEnvName); v != "" {
			prevKey = []byte(v)
			if err := rejectDemoKey(adapterMode, prevEnvName, prevKey); err != nil {
				return nil, err
			}
		}
	}

	codec, err := query.NewCursorCodec(key, prevKey)
	if err != nil {
		return nil, fmt.Errorf("create %s cursor codec: %w", label, err)
	}
	if len(prevKey) > 0 {
		slog.Info("cursor key rotation active",
			slog.String("label", label),
			slog.String("current_env", envName),
			slog.String("previous_env", prevEnvName))
	}
	return codec, nil
}

// cursorCodecs holds the parsed cursor codecs for audit-core and config-core.
type cursorCodecs struct {
	audit  *query.CursorCodec
	config *query.CursorCodec
}

// loadAllCursorCodecs loads and validates the audit and config cursor codecs.
// Extracted from run() to reduce cognitive complexity.
func loadAllCursorCodecs(adapterMode string) (cursorCodecs, error) {
	audit, err := loadCursorCodec(adapterMode,
		"GOCELL_AUDIT_CURSOR_KEY", "GOCELL_AUDIT_CURSOR_PREVIOUS_KEY",
		"core-bundle-audit-cursor-key-32!", "audit")
	if err != nil {
		return cursorCodecs{}, err
	}
	cfg, err := loadCursorCodec(adapterMode,
		"GOCELL_CONFIG_CURSOR_KEY", "GOCELL_CONFIG_CURSOR_PREVIOUS_KEY",
		"core-bundle-cfg-cursor-key--32b!", "config")
	if err != nil {
		return cursorCodecs{}, err
	}
	return cursorCodecs{audit: audit, config: cfg}, nil
}

// buildAssembly constructs the core-bundle Assembly and registers the three
// cells with durable mode. Extracted to keep run() cognitive complexity ≤ 15.
func buildAssembly(ps promStack, configCell *configcore.ConfigCore, accessCell *accesscore.AccessCore, auditCell *auditcore.AuditCore) (*assembly.CoreAssembly, error) {
	asm := assembly.New(assembly.Config{
		ID:              "core-bundle",
		DurabilityMode:  cell.DurabilityDurable,
		HookObserver:    ps.hookObserver,
		MetricsProvider: ps.metricProvider,
		// HookTimeout omitted → assembly.DefaultHookTimeout (30s) applies.
	})
	if err := asm.Register(configCell); err != nil {
		return nil, fmt.Errorf("register config-core: %w", err)
	}
	if err := asm.Register(accessCell); err != nil {
		return nil, fmt.Errorf("register access-core: %w", err)
	}
	if err := asm.Register(auditCell); err != nil {
		return nil, fmt.Errorf("register audit-core: %w", err)
	}
	return asm, nil
}

// pgHealthCheckerOpts returns a single bootstrap.WithHealthChecker option
// bound to pool.Health when pool is non-nil. Returns nil when pool is nil so
// the caller can unconditionally append without a guard block.
//
// Each probe call uses a fresh context.Background()-derived timeout so that
// a SIGTERM cancelling the outer ctx does not cause probes to fail immediately.
// K8s cannot distinguish "PG is down" from "process is shutting down" if the
// outer ctx is passed directly — the probe must remain callable until the
// process terminates.
//
// ref: Kubernetes readyz — external dependencies contribute named checks.
// ref: Uber fx lifecycle — resources must be explicitly hooked; the framework
// does not auto-manage lifetime.
func pgHealthCheckerOpts(pool *adapterpg.Pool) []bootstrap.Option {
	if pool == nil {
		return nil
	}
	return []bootstrap.Option{
		bootstrap.WithHealthChecker("postgres", func() error {
			probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return pool.Health(probeCtx)
		}),
	}
}

// buildAdapterInfo builds the adapter-info map that's exposed via
// bootstrap.WithAdapterInfo. It reflects the RESOLVED runtime topology
// (not static strings) so /readyz?verbose and adapter_info metrics match
// what actually serves traffic.
//
// The event_bus field reflects the actual in-process event bus (always
// in-memory at present — the relay forwards PG outbox entries INTO the
// in-memory bus, it does not replace it). outbox_storage distinguishes the
// outbox persistence backend so operators can tell whether the relay is active.
//
// ref: go-micro service metadata — mode changes must be visible to observers.
func buildAdapterInfo(effectiveMode, cellAdapterMode string) map[string]string {
	storageMode := "in-memory"
	outboxStorage := "in-memory"
	if cellAdapterMode == "postgres" {
		storageMode = "postgres"
		outboxStorage = "postgres"
	}
	return map[string]string{
		"mode":           effectiveMode,
		"storage":        storageMode,
		"event_bus":      "in-memory", // in-process eventbus; relay forwards PG outbox entries into it
		"outbox_storage": outboxStorage,
	}
}

// validateModeCoupling enforces that the DATA plane (cellAdapterMode) and
// CONTROL plane (adapterMode) agree on production posture. If the cell has
// committed to a real backend (postgres), operators MUST also set
// GOCELL_ADAPTER_MODE=real so key loading, /metrics, and /readyz?verbose
// run with production guards. Otherwise real persistence runs with dev-grade
// HMAC/cursor keys and unauthenticated control-plane endpoints — the exact
// split ops/security review flagged on PR #169.
//
// ref: go-zero serviceconf — single config drives all gates; misalignment is fatal.
// ref: go-micro mode/profile — runtime mode is observed by all subsystems.
func validateModeCoupling(cellAdapterMode, adapterMode string) error {
	if cellAdapterMode == "postgres" && adapterMode != "real" {
		return errcode.New(errcode.ErrValidationFailed,
			"GOCELL_CELL_ADAPTER_MODE=postgres requires GOCELL_ADAPTER_MODE=real "+
				"(real persistence demands production key loading, token-guarded "+
				"/metrics, and token-guarded /readyz?verbose)")
	}
	return nil
}

// validateAdapterMode rejects unrecognised GOCELL_ADAPTER_MODE values.
// Follows the project allowlist convention (cf. cell.ParseLevel, cmd/gocell/verify).
func validateAdapterMode(mode string) error {
	switch mode {
	case "", "real":
		return nil
	default:
		return fmt.Errorf("unknown GOCELL_ADAPTER_MODE %q; known values: \"\" (unset = dev) or \"real\"", mode)
	}
}

// buildConfigCoreOpts selects storage-adapter options for config-core based on
// GOCELL_CELL_ADAPTER_MODE. Returns the selected mode, the cell options, the
// underlying *adapterpg.Pool (non-nil iff mode=="postgres"), and a relay
// worker (non-nil iff mode=="postgres") so the caller can plumb lifecycle
// (Close, Health, relay start/stop) into bootstrap.
//
// metricsProvider is used to wire K2 relay metrics into the relay worker when
// running in postgres mode. Pass metrics.NopProvider{} in tests that do not
// exercise the relay path (or have no metrics backend configured).
//
// "postgres" = real PG (requires GOCELL_PG_DSN; run migrations first).
// "memory" or unset = in-memory repos (dev/test only).
//
// The relay worker (outboxruntime.Relay) satisfies runtime/worker.Worker via
// a compile-time assertion in runtime/outbox. It must be registered via
// bootstrap.WithWorkers so the bootstrap lifecycle starts it in Step 8 and
// stops it LIFO on shutdown — see docs/references/202604181900-outbox-wire-
// framework-comparison.md for the Kratos/fx rationale.
//
// Pilot scope: single global switch applies to all cells. Before adding a 2nd
// cell's PG wiring, split to per-cell `GOCELL_<CELL>_ADAPTER_MODE`
// (backlog: GOCELL-PER-CELL-ADAPTER-01).
//
// ref: Kratos wire — adapter selected at assembly init time, not run time.
// ref: Uber fx lifecycle — external resources must hook OnStart/OnStop;
//
//	the framework does not auto-manage pool lifetime. We return pool to run()
//	so that Close() and Health() both get wired into bootstrap explicitly.
func buildConfigCoreOpts(ctx context.Context, pub outbox.Publisher, metricsProvider metrics.Provider) (mode string, opts []configcore.Option, pool *adapterpg.Pool, relay worker.Worker, err error) {
	mode = os.Getenv("GOCELL_CELL_ADAPTER_MODE")
	if mode == "" {
		mode = "memory"
	}
	switch mode {
	case "postgres":
		pool, err = adapterpg.NewPool(ctx, adapterpg.ConfigFromEnv())
		if err != nil {
			return mode, nil, nil, nil, fmt.Errorf("config-core PG pool: %w", err)
		}
		// Any failure after NewPool must close the pool locally — the caller
		// only defers Close on successful return. K2's post-acquire failure
		// boundary (metrics registration) would otherwise leak DB connections.
		//
		// A12: fail-fast on schema version mismatch (startup time, before any traffic).
		// ref: pressly/goose v3.27 GetDBVersion — reads max version from schema_migrations.
		if schemaErr := adapterpg.VerifyExpectedVersion(ctx, pool, adapterpg.MigrationsFS()); schemaErr != nil {
			pool.Close()
			return mode, nil, nil, nil, fmt.Errorf("config-core PG schema guard: %w", schemaErr)
		}
		// A4: warn on INVALID indexes (non-fatal; operator must clean up manually).
		if invalid, detectErr := adapterpg.DetectInvalidIndexes(ctx, pool); detectErr != nil {
			slog.Warn("config-core: could not detect invalid indexes",
				slog.Any("error", detectErr))
		} else if len(invalid) > 0 {
			slog.Warn("config-core: invalid indexes detected; manual cleanup required",
				slog.Any("indexes", invalid))
		}
		outboxWriter := adapterpg.NewOutboxWriter()
		txMgr := adapterpg.NewTxManager(pool)
		// Wire K2 relay metrics into production relay (OBS-RELAY-REGISTER-ATOMIC-01).
		// Falls back to NoopRelayCollector only when provider registration fails,
		// which surfaces as an error rather than silently losing metrics.
		relayCfg := outboxruntime.DefaultRelayConfig()
		relayMetrics, rmErr := outbox.NewProviderRelayCollector(metricsProvider, "config-core")
		if rmErr != nil {
			pool.Close()
			return mode, nil, nil, nil, fmt.Errorf("config-core outbox relay metrics: %w", rmErr)
		}
		relayCfg.Metrics = relayMetrics
		pgStore := adapterpg.NewOutboxStore(pool.DB())
		relayWorker := outboxruntime.NewRelay(pgStore, pub, relayCfg)
		slog.Info("config-core: using PostgreSQL storage", slog.String("cell_adapter_mode", mode))
		return mode, []configcore.Option{
			configcore.WithPostgresDefaults(pool.DB(), outboxWriter),
			configcore.WithTxManager(txMgr),
		}, pool, relayWorker, nil
	case "memory":
		slog.Info("config-core: using in-memory storage", slog.String("cell_adapter_mode", mode))
		return mode, []configcore.Option{configcore.WithInMemoryDefaults()}, nil, nil, nil
	default:
		return mode, nil, nil, nil, errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("unknown GOCELL_CELL_ADAPTER_MODE %q; known values: \"\" (unset = memory) or \"postgres\"", mode))
	}
}

// jwtDeps groups JWT signing and verification components built at startup.
// registry is the single source of truth for all JWT configuration; issuer
// and verifier are constructed from registry so they share the same settings.
type jwtDeps struct {
	issuer   *auth.JWTIssuer
	verifier *auth.JWTVerifier
	registry *authconfig.Registry
}

// buildJWTDeps loads the key set and constructs a Registry + issuer + verifier.
// GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE are required in all modes;
// missing values cause fail-fast before any assembly init.
//
// ref: kube-apiserver --service-account-issuer — required at startup.
// ref: Hydra internal/driver/config.DefaultProvider — single Registry pattern
// plan: docs/plans/202604191515-auth-federated-whistle.md §F1
func buildJWTDeps(adapterMode string) (jwtDeps, error) {
	keySet, err := loadKeySet(adapterMode)
	if err != nil {
		return jwtDeps{}, fmt.Errorf("load JWT key set: %w", err)
	}

	// Registry reads GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE, then validates
	// them in real mode. Config errors use ErrAuthVerifierConfig so operators
	// can distinguish startup misconfigurations from runtime key errors.
	reg, err := authconfig.FromEnv(
		authconfig.WithKeys(keySet),
		authconfig.WithRealMode(true),
	)
	if err != nil {
		return jwtDeps{}, fmt.Errorf("build JWT registry: %w", err)
	}

	issuer, err := authconfig.NewJWTIssuerFromRegistry(reg, auth.DefaultAccessTokenTTL)
	if err != nil {
		return jwtDeps{}, fmt.Errorf("create JWT issuer: %w", err)
	}

	verifier, err := authconfig.NewJWTVerifierFromRegistry(reg)
	if err != nil {
		return jwtDeps{}, fmt.Errorf("create JWT verifier: %w", err)
	}

	slog.Info("core-bundle: JWT deps built",
		slog.String("issuer", reg.Issuer()),
		slog.Any("audiences", reg.Audiences()),
		slog.String("adapter_mode", adapterMode))

	return jwtDeps{issuer: issuer, verifier: verifier, registry: reg}, nil
}

// adminBootstrapWorkerOpts wires WithInitialAdminBootstrap + WithBootstrapWorkerSink
// onto the given base access-core options and returns the extended options together
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
// Sweep (P1-16) runs inside Cell.Init before EnsureAdmin, removing expired
// credential files unconditionally — including when adminExists==true causes
// EnsureAdmin to return early.
//
// Thread safety: Set (writer) and Start/Stop (readers) synchronise via
// atomic.Pointer inside worker.LazyWorker (F-OPS-2).
//
// ref: docs/architecture/202604181900-adr-auth-setup-first-run.md (scheme H)
func adminBootstrapWorkerOpts(base []accesscore.Option) (accessOpts []accesscore.Option, lazyWorkerOpt bootstrap.Option) {
	lazy := worker.Lazy()
	sink := func(w worker.Worker) { _ = lazy.Set(w) }
	accessOpts = append(base,
		accesscore.WithInitialAdminBootstrap(),
		accesscore.WithBootstrapWorkerSink(sink),
	)
	lazyWorkerOpt = bootstrap.WithWorkers(lazy)
	return accessOpts, lazyWorkerOpt
}

// promStack groups the Prometheus hook observer and metric provider.
type promStack struct {
	registry       *prom.Registry
	hookObserver   *adapterprom.HookObserver
	metricProvider *adapterprom.MetricProvider
}

// buildPromStack creates an isolated Prometheus registry, a hook observer,
// and a metric provider on top of it.
func buildPromStack() (promStack, error) {
	registry := prom.NewRegistry()
	hookObserver, err := adapterprom.NewHookObserver(adapterprom.HookObserverConfig{
		Registry: registry,
	})
	if err != nil {
		return promStack{}, fmt.Errorf("register cell hook observer: %w", err)
	}
	metricProvider, err := adapterprom.NewMetricProvider(adapterprom.MetricProviderConfig{
		Registry:  registry,
		Namespace: "gocell",
	})
	if err != nil {
		return promStack{}, fmt.Errorf("build metrics provider: %w", err)
	}
	return promStack{
		registry:       registry,
		hookObserver:   hookObserver,
		metricProvider: metricProvider,
	}, nil
}

// buildMetricsHandler constructs the /metrics HTTP handler.
// In "real" adapter mode, metricsToken must be non-empty (fail-fast).
// When metricsToken is set the handler is wrapped with a token guard;
// otherwise a warning is emitted and the handler is unauthenticated.
//
// ref: Kubernetes metrics/rbac — control-plane endpoints must be guarded.
func buildMetricsHandler(adapterMode, metricsToken string, registry *prom.Registry) (http.Handler, error) {
	if adapterMode == "real" && metricsToken == "" {
		return nil, fmt.Errorf("GOCELL_METRICS_TOKEN must be set in adapter mode \"real\" to prevent anonymous /metrics exposure; scrapers must send X-Metrics-Token header")
	}
	h := http.Handler(promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	if metricsToken != "" {
		return withMetricsTokenGuard(metricsToken, h), nil
	}
	slog.Warn("GOCELL_METRICS_TOKEN not set; /metrics exposes cell lifecycle signals without authentication (dev mode only)")
	return h, nil
}

// buildVerboseOpts returns bootstrap options for /readyz?verbose.
// In "real" adapter mode, verboseToken must be non-empty (fail-fast).
func buildVerboseOpts(adapterMode, verboseToken string) ([]bootstrap.Option, error) {
	if adapterMode == "real" && verboseToken == "" {
		return nil, fmt.Errorf("GOCELL_READYZ_VERBOSE_TOKEN must be set in adapter mode \"real\" to prevent anonymous topology exposure via /readyz?verbose")
	}
	if verboseToken != "" {
		return []bootstrap.Option{bootstrap.WithVerboseToken(verboseToken)}, nil
	}
	slog.Warn("GOCELL_READYZ_VERBOSE_TOKEN not set; /readyz?verbose exposes internal topology without authentication (dev mode only)")
	return nil, nil
}

// internalGuardFromEnv builds a ServiceTokenMiddleware guard for /internal/v1/*
// from GOCELL_SERVICE_SECRET (and optionally GOCELL_SERVICE_SECRET_PREVIOUS).
//
//   - In "real" adapter mode, the env var is required; missing value returns an error.
//   - In dev mode (any non-"real" mode), an empty secret returns (nil, nil), meaning
//     "no guard installed" — the caller then skips WithInternalEndpointGuard.
//
// The returned guard is nil only in dev mode with an empty secret. In all other
// cases a functioning guard (or an error) is returned.
//
// ref: Kubernetes kube-apiserver service-account verification — guard only when
// key material is present; no guard is better than a broken guard.
func internalGuardFromEnv(adapterMode string) (func(http.Handler) http.Handler, error) {
	secret := os.Getenv(auth.EnvServiceSecret)
	if secret == "" {
		if adapterMode == "real" {
			return nil, fmt.Errorf("GOCELL_SERVICE_SECRET must be set in adapter mode \"real\" to protect /internal/v1/*")
		}
		slog.Warn("controlplane guard disabled: GOCELL_SERVICE_SECRET is empty (dev mode only)")
		return nil, nil
	}
	prevSecret := os.Getenv(auth.EnvServiceSecretPrevious)
	var prevBytes []byte
	if prevSecret != "" {
		prevBytes = []byte(prevSecret)
	}
	ring, err := auth.NewHMACKeyRing([]byte(secret), prevBytes)
	if err != nil {
		return nil, fmt.Errorf("build service HMAC key ring: %w", err)
	}
	return auth.ServiceTokenMiddleware(ring), nil
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
		slog.Warn("core-bundle: invalid GOCELL_STATE_DIR; initial admin credential path unresolvable",
			slog.String("error", err.Error()))
		return
	}
	slog.Info("core-bundle: starting; if first run, initial admin credentials are written to "+credPath,
		slog.String("cred_path", credPath))
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx); err != nil {
		slog.Error("application failed", "error", err)
		os.Exit(1)
	}
}

// run contains all assembly and bootstrap logic, extracted from main() for testability.
func run(ctx context.Context) error {
	adapterMode := os.Getenv("GOCELL_ADAPTER_MODE")
	if err := validateAdapterMode(adapterMode); err != nil {
		return fmt.Errorf("adapter mode: %w", err)
	}

	hmacKey, err := loadSecret("GOCELL_HMAC_KEY", "dev-hmac-key-replace-in-prod!!!!", adapterMode)
	if err != nil {
		return fmt.Errorf("HMAC key: %w", err)
	}
	if err := rejectDemoKey(adapterMode, "GOCELL_HMAC_KEY", hmacKey); err != nil {
		return err
	}

	jwt, err := buildJWTDeps(adapterMode)
	if err != nil {
		return err
	}

	eb := eventbus.New()

	effectiveMode := "in-memory"
	if adapterMode == "real" {
		effectiveMode = "real-keys-in-memory-storage"
	}
	slog.Info("adapter mode",
		slog.String("requested", adapterMode),
		slog.String("effective", effectiveMode))

	codecs, err := loadAllCursorCodecs(adapterMode)
	if err != nil {
		return err
	}

	// Build Prometheus stack before config-core opts so the metrics provider
	// can be passed into buildConfigCoreOpts for K2 relay metrics wiring.
	ps, err := buildPromStack()
	if err != nil {
		return err
	}

	cellAdapterMode, cellAdapterOpts, pgPool, relayWorker, err := buildConfigCoreOpts(ctx, eb, ps.metricProvider)
	if err != nil {
		return fmt.Errorf("config-core cell adapter: %w", err)
	}
	// Pool lifecycle: when running with a real PG pool, we own Close() and
	// owe readiness signals. defer Close here (before mode check) so an early
	// validation failure still releases the pool.
	if pgPool != nil {
		defer pgPool.Close()
	}
	if err := validateModeCoupling(cellAdapterMode, adapterMode); err != nil {
		return err
	}

	configOpts := append([]configcore.Option{
		configcore.WithPublisher(eb),
		configcore.WithCursorCodec(codecs.config),
	}, cellAdapterOpts...)
	configCell := configcore.NewConfigCore(configOpts...)

	accessOpts, adminWorkerOpt := adminBootstrapWorkerOpts([]accesscore.Option{
		accesscore.WithInMemoryDefaults(),
		accesscore.WithPublisher(eb),
		accesscore.WithJWTIssuer(jwt.issuer),
		accesscore.WithJWTVerifier(jwt.verifier),
	})
	accessCell := accesscore.NewAccessCore(accessOpts...)

	auditCell := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithPublisher(eb),
		auditcore.WithHMACKey(hmacKey),
		auditcore.WithCursorCodec(codecs.audit),
	)

	asm, err := buildAssembly(ps, configCell, accessCell, auditCell)
	if err != nil {
		return err
	}

	adapterInfo := buildAdapterInfo(effectiveMode, cellAdapterMode)
	slog.Info("core-bundle: startup configuration",
		slog.String("adapter_mode", adapterInfo["mode"]),
		slog.String("storage", adapterInfo["storage"]),
		slog.String("event_bus", adapterInfo["event_bus"]),
		slog.String("outbox_storage", adapterInfo["outbox_storage"]))

	// /readyz?verbose token — required in real mode, optional in dev.
	// Check this before /metrics so operator error messages name the first
	// missing secret (consistent with the original sequential validation order).
	verboseOpts, err := buildVerboseOpts(adapterMode, os.Getenv("GOCELL_READYZ_VERBOSE_TOKEN"))
	if err != nil {
		return err
	}

	metricsHandler, err := buildMetricsHandler(adapterMode, os.Getenv("GOCELL_METRICS_TOKEN"), ps.registry)
	if err != nil {
		return err
	}

	// Build the /internal/v1/* service-token guard. In real mode, the guard is
	// required (missing secret aborts startup). In dev mode, empty secret skips
	// the guard entirely (nil guard means WithInternalEndpointGuard is not added).
	internalGuard, err := internalGuardFromEnv(adapterMode)
	if err != nil {
		return err
	}

	// Wire ConsumerBase so every subscriber handler inherits two-phase Claimer
	// idempotency, backoff retry, and DLX routing. An in-memory Claimer is used
	// here so the in-process EventBus path has the same semantics as a future
	// multi-pod deployment backed by adapters/redis IdempotencyClaimer.
	//
	// ref: runtime-api.md WithConsumerMiddleware — middleware order is
	// observability-restore (prepended by bootstrap) then ConsumerBase.
	// ConsumerGroup is no longer set here; each subscription's ConsumerGroup is
	// provided by the Cell via EventRouter.AddHandler and flows through
	// Subscription.ConsumerGroup into the idempotency key namespace.
	consumerBase, err := outbox.NewConsumerBase(idempotency.NewInMemClaimer(), outbox.ConsumerBaseConfig{})
	if err != nil {
		return fmt.Errorf("construct ConsumerBase: %w", err)
	}

	logInitialAdminCredPath()

	app := bootstrap.New(assembleBootstrapOpts(bootstrapDeps{
		assembly:        asm,
		eventBus:        eb,
		consumerBase:    consumerBase,
		adapterInfo:     adapterInfo,
		metricsHandler:  metricsHandler,
		metricsProvider: ps.metricProvider,
		verboseOpts:     verboseOpts,
		pgPool:          pgPool,
		relayWorker:     relayWorker,
		adminWorkerOpt:  adminWorkerOpt,
		internalGuard:   internalGuard,
	})...)
	return app.Run(ctx)
}

// bootstrapDeps groups the inputs to assembleBootstrapOpts so the call site in
// run() stays under the cognitive-complexity ceiling.
type bootstrapDeps struct {
	assembly        *assembly.CoreAssembly
	eventBus        *eventbus.InMemoryEventBus
	consumerBase    *outbox.ConsumerBase
	adapterInfo     map[string]string
	metricsHandler  http.Handler
	metricsProvider *adapterprom.MetricProvider
	verboseOpts     []bootstrap.Option
	pgPool          *adapterpg.Pool
	relayWorker     worker.Worker
	adminWorkerOpt  bootstrap.Option
	// internalGuard is the service-token middleware protecting /internal/v1/*.
	// nil means no guard is installed (dev mode with empty GOCELL_SERVICE_SECRET).
	internalGuard func(http.Handler) http.Handler
}

// assembleBootstrapOpts builds the ordered bootstrap.Option slice. Extracted
// from run() to keep run cognitive complexity ≤ 15. Conditional appends
// (verboseOpts, pgHealthChecker, relayWorker) live here so run() stays linear.
//
// ref: Uber fx OnStart non-blocking + goroutine + OnStop blocking pattern.
// GoCell WorkerGroup already implements this contract; OutboxRelay satisfies
// worker.Worker — wiring is conditional on PG mode (A11).
func assembleBootstrapOpts(d bootstrapDeps) []bootstrap.Option {
	opts := append([]bootstrap.Option{
		bootstrap.WithAssembly(d.assembly),
		bootstrap.WithHTTPAddr(":8080"),
		bootstrap.WithPublisher(d.eventBus), bootstrap.WithSubscriber(d.eventBus),
		bootstrap.WithConsumerMiddleware(d.consumerBase.AsMiddleware()),
		bootstrap.WithPublicEndpoints([]string{
			"POST /api/v1/access/sessions/login",
			"POST /api/v1/access/sessions/refresh",
		}),
		// Password-reset exempt routes: change-password and logout are the only
		// endpoints reachable while the token carries password_reset_required=true.
		// Runtime/auth no longer hard-codes these (F6 decoupling).
		bootstrap.WithPasswordResetExemptEndpoints([]string{
			"POST /api/v1/access/users/{id}/password",
			"DELETE /api/v1/access/sessions/{id}",
		}),
		// Client-navigation hint for the 403 response body; runtime/auth no
		// longer carries any business path literal, so the composition root
		// names the endpoint that finishes the reset flow.
		bootstrap.WithPasswordResetChangeEndpointHint("POST /api/v1/access/users/{id}/password"),
		bootstrap.WithAdapterInfo(d.adapterInfo),
		bootstrap.WithRouterOptions(router.WithMetricsHandler(d.metricsHandler)),
		bootstrap.WithMetricsProvider(d.metricsProvider),
	}, d.verboseOpts...)
	opts = append(opts, pgHealthCheckerOpts(d.pgPool)...)
	if d.relayWorker != nil {
		opts = append(opts, bootstrap.WithWorkers(d.relayWorker))
	}
	// Wire the initial-admin bootstrap cleanup worker via worker.Lazy().
	// Sweep (P1-16) runs inside Cell.Init before EnsureAdmin — no Lifecycle hook needed.
	if d.adminWorkerOpt != nil {
		opts = append(opts, d.adminWorkerOpt)
	}
	// Wire the service-token guard for /internal/v1/* when a guard was built.
	// guard is nil in dev mode when GOCELL_SERVICE_SECRET is empty; real mode
	// fail-fasts in internalGuardFromEnv before reaching here.
	if d.internalGuard != nil {
		opts = append(opts, bootstrap.WithInternalEndpointGuard("/internal/v1/", d.internalGuard))
	}
	return opts
}
