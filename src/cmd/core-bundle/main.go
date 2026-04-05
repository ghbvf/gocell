// Package main is the entry point for the core-bundle assembly.
// It bootstraps config-core, access-core, and audit-core with in-memory
// repositories, suitable for development and integration testing.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	accesscore "github.com/ghbvf/gocell/cells/access-core"
	auditcore "github.com/ghbvf/gocell/cells/audit-core"
	configcore "github.com/ghbvf/gocell/cells/config-core"
	"github.com/ghbvf/gocell/kernel/assembly"
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

func main() {
	// Create shared event bus.
	eb := eventbus.New()

	signingKey := envOrDefault("GOCELL_SIGNING_KEY", "dev-signing-key-replace-in-prod!!")
	hmacKey := envOrDefault("GOCELL_HMAC_KEY", "dev-hmac-key-replace-in-prod!!!!")

	// Create cells with in-memory repositories.
	configCell := configcore.NewConfigCore(
		configcore.WithInMemoryDefaults(),
		configcore.WithPublisher(eb),
	)
	accessCell := accesscore.NewAccessCore(
		accesscore.WithInMemoryDefaults(),
		accesscore.WithPublisher(eb),
		accesscore.WithSigningKey(signingKey),
	)
	auditCell := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithPublisher(eb),
		auditcore.WithHMACKey(hmacKey),
	)

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

	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithHTTPAddr(":8080"),
		bootstrap.WithEventBus(eb),
	)

	if err := app.Run(ctx); err != nil {
		slog.Error("application failed", "error", err)
		os.Exit(1)
	}
}
