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
	signingKey := envOrDefault("GOCELL_SIGNING_KEY", "dev-signing-key-replace-in-prod!!")
	hmacKey := envOrDefault("GOCELL_HMAC_KEY", "dev-hmac-key-replace-in-prod!!!!")

	// Determine adapter mode: "real" for production adapters, default for in-memory.
	adapterMode := os.Getenv("GOCELL_ADAPTER_MODE")

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

	// Common options.
	configOpts = append(configOpts, configcore.WithPublisher(eb))
	accessOpts = append(accessOpts,
		accesscore.WithPublisher(eb),
		accesscore.WithSigningKey(signingKey),
	)
	auditOpts = append(auditOpts,
		auditcore.WithPublisher(eb),
		auditcore.WithHMACKey(hmacKey),
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
