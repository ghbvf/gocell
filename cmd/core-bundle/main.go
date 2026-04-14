// Package main is the entry point for the core-bundle assembly.
// It bootstraps config-core, access-core, and audit-core with in-memory
// repositories by default, suitable for development and integration testing.
//
// Set GOCELL_ADAPTER_MODE=real to enable real adapter wiring (requires
// GOCELL_POSTGRES_DSN, GOCELL_REDIS_ADDR, GOCELL_RABBITMQ_URL).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

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
// In "real" mode, keys are loaded from environment variables (fail-fast if missing).
// In dev mode (default), an ephemeral RSA key pair is generated per process.
func loadKeySet(adapterMode string) (*auth.KeySet, error) {
	if adapterMode == "real" {
		return auth.LoadKeySetFromEnv()
	}
	if adapterMode != "" {
		slog.Warn("unrecognized GOCELL_ADAPTER_MODE, falling back to dev mode",
			slog.String("value", adapterMode),
			slog.String("expected", "real"))
	}
	privKey, pubKey := auth.MustGenerateTestKeyPair()
	slog.Warn("dev mode: using ephemeral RSA key pair; tokens will be invalidated on restart")
	return auth.NewKeySet(privKey, pubKey)
}

func main() {
	// Determine adapter mode early — it controls key loading strategy.
	adapterMode := os.Getenv("GOCELL_ADAPTER_MODE")

	hmacKey := envOrDefault("GOCELL_HMAC_KEY", "dev-hmac-key-replace-in-prod!!!!")

	keySet, err := loadKeySet(adapterMode)
	if err != nil {
		slog.Error("failed to load JWT key set", "error", err)
		os.Exit(1)
	}

	jwtIssuer, err := auth.NewJWTIssuer(keySet, "core-bundle", 15*time.Minute)
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

	if adapterMode == "real" {
		slog.Info("adapter mode: real — adapter stubs prepared (connect in integration tests)")

		// TODO(Phase 3): Wire real adapters here when available:
		//   postgresDSN := os.Getenv("GOCELL_POSTGRES_DSN")
		//   redisAddr   := os.Getenv("GOCELL_REDIS_ADDR")
		//   rabbitmqURL := os.Getenv("GOCELL_RABBITMQ_URL")
		//
		// Real adapter initialization:
		//   pgPool := adapters.NewPostgresPool(postgresDSN)
		//   outboxWriter := adapters.NewPostgresOutboxWriter(pgPool)
		//   configOpts = append(configOpts, configcore.WithOutboxWriter(outboxWriter))
		//   accessOpts = append(accessOpts, accesscore.WithOutboxWriter(outboxWriter))
		//   auditOpts  = append(auditOpts, auditcore.WithOutboxWriter(outboxWriter))

		// Fallback to in-memory until real adapters are implemented.
		configOpts = append(configOpts, configcore.WithInMemoryDefaults())
		accessOpts = append(accessOpts, accesscore.WithInMemoryDefaults())
		auditOpts = append(auditOpts, auditcore.WithInMemoryDefaults())
	} else {
		slog.Info("adapter mode: in-memory (development)")
		configOpts = append(configOpts, configcore.WithInMemoryDefaults())
		accessOpts = append(accessOpts, accesscore.WithInMemoryDefaults())
		auditOpts = append(auditOpts, auditcore.WithInMemoryDefaults())
	}

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

	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithHTTPAddr(":8080"),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithPublicEndpoints(publicEndpoints),
	)

	if err := app.Run(ctx); err != nil {
		slog.Error("application failed", "error", err)
		os.Exit(1)
	}
}
