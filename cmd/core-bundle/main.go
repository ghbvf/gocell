// Package main is the entry point for the core-bundle assembly.
// It bootstraps config-core, access-core, and audit-core with in-memory
// repositories by default, suitable for development and integration testing.
//
// Set GOCELL_ADAPTER_MODE=real to enable real adapter wiring (requires
// GOCELL_POSTGRES_DSN, GOCELL_REDIS_ADDR, GOCELL_RABBITMQ_URL).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	accesscore "github.com/ghbvf/gocell/cells/access-core"
	auditcore "github.com/ghbvf/gocell/cells/audit-core"
	configcore "github.com/ghbvf/gocell/cells/config-core"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

func envOrDefault(key, fallback string) []byte {
	if v := os.Getenv(key); v != "" {
		return []byte(v)
	}
	slog.Warn("using dev-only default key; set env var for production", slog.String("var", key))
	return []byte(fallback)
}

// loadKeySet returns a KeySet based on the adapter mode.
// validateAdapterMode must be called before loadKeySet to reject invalid modes.
// In "real" mode, keys are loaded from environment variables (fail-fast if missing).
// In dev mode (default), an ephemeral RSA key pair is generated per process.
func loadKeySet(adapterMode string) (*auth.KeySet, error) {
	if adapterMode == "real" {
		return auth.LoadKeySetFromEnv()
	}
	// All other modes use ephemeral dev keys (validateAdapterMode already
	// rejected unknown values, so only "" reaches here).
	privKey, pubKey := auth.MustGenerateTestKeyPair()
	slog.Warn("dev mode: using ephemeral RSA key pair; tokens will be invalidated on restart")
	return auth.NewKeySet(privKey, pubKey)
}

// validateAdapterMode rejects unrecognised GOCELL_ADAPTER_MODE values.
// Follows the project allowlist convention (cf. cell.ParseLevel, cmd/gocell/verify).
func validateAdapterMode(mode string) error {
	switch mode {
	case "":
		return nil
	case "real":
		return fmt.Errorf("adapter mode %q is not yet supported: real adapter implementations are pending", mode)
	default:
		return fmt.Errorf("unknown GOCELL_ADAPTER_MODE %q; valid values: \"\" (dev), \"real\"", mode)
	}
}

func main() {
	// Determine adapter mode early — it controls key loading strategy.
	adapterMode := os.Getenv("GOCELL_ADAPTER_MODE")

	// Strict mode: refuse to start with in-memory fallbacks when the operator
	// explicitly requested production-grade adapters via GOCELL_ADAPTER_MODE=real.
	if err := validateAdapterMode(adapterMode); err != nil {
		slog.Error("adapter mode validation failed",
			slog.String("adapter_mode", adapterMode),
			slog.Any("error", err))
		os.Exit(1)
	}

	hmacKey := envOrDefault("GOCELL_HMAC_KEY", "dev-hmac-key-replace-in-prod!!!!")

	keySet, err := loadKeySet(adapterMode)
	if err != nil {
		slog.Error("failed to load JWT key set", "error", err)
		os.Exit(1)
	}

	jwtIssuer, err := auth.NewJWTIssuer(keySet, "core-bundle", auth.DefaultAccessTokenTTL)
	if err != nil {
		slog.Error("failed to create JWT issuer", "error", err)
		os.Exit(1)
	}
	jwtVerifier, err := auth.NewJWTVerifier(keySet)
	if err != nil {
		slog.Error("failed to create JWT verifier", "error", err)
		os.Exit(1)
	}

	// Create shared event bus (in-memory by default).
	// When GOCELL_ADAPTER_MODE=real, a real message broker adapter would replace
	// this; for now we always use the in-memory event bus as a fallback.
	eb := eventbus.New()

	// Build cell options based on adapter mode.
	var (
		configOpts []configcore.Option
		accessOpts []accesscore.Option
		auditOpts  []auditcore.Option
	)

	slog.Info("adapter mode: in-memory (development)",
		slog.String("requested", adapterMode),
		slog.String("effective", "in-memory"))
	configOpts = append(configOpts, configcore.WithInMemoryDefaults())
	accessOpts = append(accessOpts, accesscore.WithInMemoryDefaults())
	auditOpts = append(auditOpts, auditcore.WithInMemoryDefaults())

	// Cursor codecs for pagination — per-cell isolation prevents cross-cell cursor reuse.
	auditCursorCodec, err := query.NewCursorCodec(
		envOrDefault("GOCELL_AUDIT_CURSOR_KEY", "core-bundle-audit-cursor-key32!"),
	)
	if err != nil {
		slog.Error("failed to create audit cursor codec", "error", err)
		os.Exit(1)
	}
	configCursorCodec, err := query.NewCursorCodec(
		envOrDefault("GOCELL_CONFIG_CURSOR_KEY", "core-bundle-cfg-cursor-key-32b!"),
	)
	if err != nil {
		slog.Error("failed to create config cursor codec", "error", err)
		os.Exit(1)
	}

	// Common options.
	configOpts = append(configOpts,
		configcore.WithPublisher(eb),
		configcore.WithCursorCodec(configCursorCodec),
	)
	accessOpts = append(accessOpts,
		accesscore.WithPublisher(eb),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
	)
	auditOpts = append(auditOpts,
		auditcore.WithPublisher(eb),
		auditcore.WithHMACKey(hmacKey),
		auditcore.WithCursorCodec(auditCursorCodec),
	)

	// Create cells.
	configCell := configcore.NewConfigCore(configOpts...)
	accessCell := accesscore.NewAccessCore(accessOpts...)
	auditCell := auditcore.NewAuditCore(auditOpts...)

	// Create assembly and register cells in dependency order.
	asm := assembly.New(assembly.Config{ID: "core-bundle"})
	if err := asm.Register(configCell); err != nil {
		slog.Error("failed to register config-core", "error", err)
		os.Exit(1)
	}
	if err := asm.Register(accessCell); err != nil {
		slog.Error("failed to register access-core", "error", err)
		os.Exit(1)
	}
	if err := asm.Register(auditCell); err != nil {
		slog.Error("failed to register audit-core", "error", err)
		os.Exit(1)
	}

	// Bootstrap the application.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Public endpoints declared at composition root — not in runtime/auth defaults.
	// Only login and refresh are accessible without a valid JWT.
	publicEndpoints := []string{
		"/api/v1/access/sessions/login",
		"/api/v1/access/sessions/refresh",
	}

	adapterInfo := map[string]string{
		"mode":      "in-memory",
		"storage":   "in-memory",
		"event_bus": "in-memory",
	}
	slog.Info("core-bundle: startup configuration",
		slog.String("adapter_mode", adapterInfo["mode"]),
		slog.String("storage", adapterInfo["storage"]),
		slog.String("event_bus", adapterInfo["event_bus"]))

	bootstrapOpts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		bootstrap.WithHTTPAddr(":8080"),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithPublicEndpoints(publicEndpoints),
		bootstrap.WithAdapterInfo(adapterInfo),
	}

	// Cell health probes (e.g. session-store) are auto-discovered by bootstrap
	// via cell.HealthContributor interface — no manual registration needed.
	app := bootstrap.New(bootstrapOpts...)

	if err := app.Run(ctx); err != nil {
		slog.Error("application failed", "error", err)
		os.Exit(1)
	}
}
