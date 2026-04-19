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

	adapterprom "github.com/ghbvf/gocell/adapters/prometheus"
	accesscore "github.com/ghbvf/gocell/cells/access-core"
	auditcore "github.com/ghbvf/gocell/cells/audit-core"
	configcore "github.com/ghbvf/gocell/cells/config-core"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	authconfig "github.com/ghbvf/gocell/runtime/auth/config"
	"github.com/ghbvf/gocell/runtime/bootstrap"
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

// loadRequiredEnv loads a required env var. Returns an error if the value is
// empty; there is no fallback default. Used for env vars that are mandatory
// in all adapter modes (e.g. GOCELL_JWT_ISSUER, GOCELL_JWT_AUDIENCE).
func loadRequiredEnv(envKey, description string) (string, error) {
	v := os.Getenv(envKey)
	if v == "" {
		return "", fmt.Errorf("%s must be set (%s)", envKey, description)
	}
	return v, nil
}

// loadJWTIssuer loads the JWT issuer string from GOCELL_JWT_ISSUER.
// The env var is required in all adapter modes — there is no fallback default.
func loadJWTIssuer(adapterMode string) (string, error) {
	return loadRequiredEnv("GOCELL_JWT_ISSUER", fmt.Sprintf("adapter mode %q", adapterMode))
}

// loadJWTAudience loads the JWT audience string from GOCELL_JWT_AUDIENCE.
// The env var is required in all adapter modes — there is no fallback default.
func loadJWTAudience(adapterMode string) (string, error) {
	return loadRequiredEnv("GOCELL_JWT_AUDIENCE", fmt.Sprintf("adapter mode %q", adapterMode))
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

// run is the application entry point: parse environment, assemble, and run.
// Extracted from main() for testability.
//
// ref: uber-go/fx app.go Run — thin wrapper around Start/Stop lifecycle.
func run(ctx context.Context) error {
	deps, err := AppDepsFromEnv(ctx)
	if err != nil {
		return err
	}
	app, err := BuildBootstrap(deps)
	if err != nil {
		return err
	}
	return app.Run(ctx)
}
