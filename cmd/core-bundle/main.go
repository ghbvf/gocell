// Package main is the entry point for the core-bundle assembly.
// It bootstraps config-core, access-core, and audit-core with in-memory
// repositories by default, suitable for development and integration testing.
//
// DurabilityDurable is set to reject noop placeholders (NoopWriter,
// NoopTxRunner, DiscardPublisher) even in dev mode. Set GOCELL_ADAPTER_MODE=real
// to require all secrets from env vars (fail-fast on missing).
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
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/router"
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
	slog.Warn("using dev-only default; set env var for production", slog.String("var", envKey))
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
// label is used in wrapping error messages.
func loadCursorCodec(adapterMode, envName, devDefault, label string) (*query.CursorCodec, error) {
	key, err := loadSecret(envName, devDefault, adapterMode)
	if err != nil {
		return nil, fmt.Errorf("%s cursor key: %w", label, err)
	}
	if err := rejectDemoKey(adapterMode, envName, key); err != nil {
		return nil, err
	}
	codec, err := query.NewCursorCodec(key)
	if err != nil {
		return nil, fmt.Errorf("create %s cursor codec: %w", label, err)
	}
	return codec, nil
}

// validateAdapterMode rejects unrecognised GOCELL_ADAPTER_MODE values.
// Follows the project allowlist convention (cf. cell.ParseLevel, cmd/gocell/verify).
func validateAdapterMode(mode string) error {
	switch mode {
	case "", "real":
		return nil
	default:
		return fmt.Errorf("unknown GOCELL_ADAPTER_MODE %q; known values: \"\" (dev), \"real\"", mode)
	}
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

	keySet, err := loadKeySet(adapterMode)
	if err != nil {
		return fmt.Errorf("load JWT key set: %w", err)
	}

	jwtIssuer, err := auth.NewJWTIssuer(keySet, "core-bundle", auth.DefaultAccessTokenTTL)
	if err != nil {
		return fmt.Errorf("create JWT issuer: %w", err)
	}
	jwtVerifier, err := auth.NewJWTVerifier(keySet)
	if err != nil {
		return fmt.Errorf("create JWT verifier: %w", err)
	}

	eb := eventbus.New()

	// NOTE: Storage adapters (postgres/redis/rabbitmq) are not yet wired even in
	// "real" mode — only JWT keys + HMAC + cursor keys come from env. Storage is
	// always in-memory for now. adapterInfo reflects storage state, not mode.
	effectiveMode := "in-memory"
	if adapterMode == "real" {
		effectiveMode = "real-keys-in-memory-storage"
	}
	slog.Info("adapter mode",
		slog.String("requested", adapterMode),
		slog.String("effective", effectiveMode))

	auditCursorCodec, err := loadCursorCodec(adapterMode, "GOCELL_AUDIT_CURSOR_KEY", "core-bundle-audit-cursor-key-32!", "audit")
	if err != nil {
		return err
	}
	configCursorCodec, err := loadCursorCodec(adapterMode, "GOCELL_CONFIG_CURSOR_KEY", "core-bundle-cfg-cursor-key--32b!", "config")
	if err != nil {
		return err
	}

	configCell := configcore.NewConfigCore(
		configcore.WithInMemoryDefaults(),
		configcore.WithPublisher(eb),
		configcore.WithCursorCodec(configCursorCodec),
	)

	accessOpts := []accesscore.Option{
		accesscore.WithInMemoryDefaults(),
		accesscore.WithPublisher(eb),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
	}

	// Seed admin role + optional admin user from env vars.
	// Unsetenv to remove plaintext from /proc/{pid}/environ as soon as possible
	// (defense-in-depth; Go's string immutability prevents full cleanup).
	adminUser := os.Getenv("GOCELL_ADMIN_USER")
	adminPass := os.Getenv("GOCELL_ADMIN_PASS")
	_ = os.Unsetenv("GOCELL_ADMIN_PASS")
	switch {
	case adminUser != "" && adminPass != "":
		accessOpts = append(accessOpts, accesscore.WithSeedAdmin(adminUser, adminPass))
	case adminUser != "" || adminPass != "":
		slog.Error("seed admin: both GOCELL_ADMIN_USER and GOCELL_ADMIN_PASS must be set; got only one, skipping admin user creation")
		accessOpts = append(accessOpts, accesscore.WithSeedAdminRole())
	default:
		accessOpts = append(accessOpts, accesscore.WithSeedAdminRole())
	}

	accessCell := accesscore.NewAccessCore(accessOpts...)
	auditCell := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithPublisher(eb),
		auditcore.WithHMACKey(hmacKey),
		auditcore.WithCursorCodec(auditCursorCodec),
	)

	// Register cell lifecycle hook metrics on a dedicated Prometheus registry.
	// The registry is isolated from the default global registry so test runs
	// and multiple assemblies can coexist without collisions.
	promRegistry := prom.NewRegistry()
	hookObserver, err := adapterprom.NewHookObserver(adapterprom.HookObserverConfig{
		Registry: promRegistry,
	})
	if err != nil {
		return fmt.Errorf("register cell hook observer: %w", err)
	}

	// Expose the Prometheus registry to kernel modules via the
	// provider-neutral metrics.Provider surface. The assembly dispatcher
	// uses it for drop counters; bootstrap exposes it for caller-registered
	// metrics (e.g. pool collectors wired from real adapter topologies).
	metricProvider, err := adapterprom.NewMetricProvider(adapterprom.MetricProviderConfig{
		Registry:  promRegistry,
		Namespace: "gocell",
	})
	if err != nil {
		return fmt.Errorf("build metrics provider: %w", err)
	}

	asm := assembly.New(assembly.Config{
		ID:              "core-bundle",
		DurabilityMode:  cell.DurabilityDurable,
		HookObserver:    hookObserver,
		MetricsProvider: metricProvider,
		// HookTimeout omitted → assembly.DefaultHookTimeout (30s) applies.
	})
	if err := asm.Register(configCell); err != nil {
		return fmt.Errorf("register config-core: %w", err)
	}
	if err := asm.Register(accessCell); err != nil {
		return fmt.Errorf("register access-core: %w", err)
	}
	if err := asm.Register(auditCell); err != nil {
		return fmt.Errorf("register audit-core: %w", err)
	}

	adapterInfo := map[string]string{
		"mode":      effectiveMode,
		"storage":   "in-memory", // storage adapters pending
		"event_bus": "in-memory", // event bus adapters pending
	}
	slog.Info("core-bundle: startup configuration",
		slog.String("adapter_mode", adapterInfo["mode"]),
		slog.String("storage", adapterInfo["storage"]),
		slog.String("event_bus", adapterInfo["event_bus"]))

	// /readyz?verbose token — required in real mode, optional in dev.
	verboseToken := os.Getenv("GOCELL_READYZ_VERBOSE_TOKEN")
	if adapterMode == "real" && verboseToken == "" {
		return fmt.Errorf("GOCELL_READYZ_VERBOSE_TOKEN must be set in adapter mode \"real\" to prevent anonymous topology exposure via /readyz?verbose")
	}

	// /metrics token — required in real mode to avoid anonymous exposure of
	// cell lifecycle signals (cell_id / hook / outcome labels reveal internal
	// topology). In dev mode, unrestricted to keep local debugging friction low.
	// ref: Kubernetes metrics/rbac — control-plane endpoints must be guarded.
	metricsToken := os.Getenv("GOCELL_METRICS_TOKEN")
	if adapterMode == "real" && metricsToken == "" {
		return fmt.Errorf("GOCELL_METRICS_TOKEN must be set in adapter mode \"real\" to prevent anonymous /metrics exposure; scrapers must send X-Metrics-Token header")
	}
	metricsHandler := http.Handler(promhttp.HandlerFor(promRegistry, promhttp.HandlerOpts{}))
	if metricsToken != "" {
		metricsHandler = withMetricsTokenGuard(metricsToken, metricsHandler)
	} else {
		slog.Warn("GOCELL_METRICS_TOKEN not set; /metrics exposes cell lifecycle signals without authentication (dev mode only)")
	}

	bootstrapOpts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		bootstrap.WithHTTPAddr(":8080"),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithPublicEndpoints([]string{
			"/api/v1/access/sessions/login",
			"/api/v1/access/sessions/refresh",
		}),
		bootstrap.WithAdapterInfo(adapterInfo),
		// Expose cell lifecycle hook metrics on /metrics.
		// promhttp serves the isolated registry configured above; the
		// handler is wrapped with token guard when GOCELL_METRICS_TOKEN is set.
		bootstrap.WithRouterOptions(router.WithMetricsHandler(metricsHandler)),
		// Share the same Provider with bootstrap so any future metric
		// registrar (HTTP collector, relay collector, pool collector)
		// lands on one Prometheus registry.
		bootstrap.WithMetricsProvider(metricProvider),
	}
	if verboseToken != "" {
		bootstrapOpts = append(bootstrapOpts, bootstrap.WithVerboseToken(verboseToken))
	} else {
		slog.Warn("GOCELL_READYZ_VERBOSE_TOKEN not set; /readyz?verbose exposes internal topology without authentication (dev mode only)")
	}

	app := bootstrap.New(bootstrapOpts...)

	return app.Run(ctx)
}
