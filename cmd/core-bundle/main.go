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
		return fmt.Errorf("unknown GOCELL_ADAPTER_MODE %q; known values: \"\" (dev), \"real\" (not yet implemented)", mode)
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

	hmacKey := envOrDefault("GOCELL_HMAC_KEY", "dev-hmac-key-replace-in-prod!!!!")

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

	slog.Info("adapter mode: in-memory (development)",
		slog.String("requested", adapterMode),
		slog.String("effective", "in-memory"))

	auditCursorCodec, err := query.NewCursorCodec(
		envOrDefault("GOCELL_AUDIT_CURSOR_KEY", "core-bundle-audit-cursor-key32!"),
	)
	if err != nil {
		return fmt.Errorf("create audit cursor codec: %w", err)
	}
	configCursorCodec, err := query.NewCursorCodec(
		envOrDefault("GOCELL_CONFIG_CURSOR_KEY", "core-bundle-cfg-cursor-key-32b!"),
	)
	if err != nil {
		return fmt.Errorf("create config cursor codec: %w", err)
	}

	configCell := configcore.NewConfigCore(
		configcore.WithInMemoryDefaults(),
		configcore.WithPublisher(eb),
		configcore.WithCursorCodec(configCursorCodec),
	)
	accessCell := accesscore.NewAccessCore(
		accesscore.WithInMemoryDefaults(),
		accesscore.WithPublisher(eb),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
	)
	auditCell := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithPublisher(eb),
		auditcore.WithHMACKey(hmacKey),
		auditcore.WithCursorCodec(auditCursorCodec),
	)

	asm := assembly.New(assembly.Config{ID: "core-bundle"})
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
		"mode":      "in-memory",
		"storage":   "in-memory",
		"event_bus": "in-memory",
	}
	slog.Info("core-bundle: startup configuration",
		slog.String("adapter_mode", adapterInfo["mode"]),
		slog.String("storage", adapterInfo["storage"]),
		slog.String("event_bus", adapterInfo["event_bus"]))

	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithHTTPAddr(":8080"),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithPublicEndpoints([]string{
			"/api/v1/access/sessions/login",
			"/api/v1/access/sessions/refresh",
		}),
		bootstrap.WithAdapterInfo(adapterInfo),
	)

	return app.Run(ctx)
}
