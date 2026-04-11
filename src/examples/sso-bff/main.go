// Package main is the entry point for the sso-bff example application.
// It demonstrates combining the three built-in GoCell Cells (access-core,
// audit-core, config-core) into a single SSO BFF assembly using in-memory
// dependencies for development.
//
// Usage:
//
//	cd src && go run ./examples/sso-bff
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
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

// noopWriter satisfies outbox.Writer for development mode.
// L2+ Cells require an outbox writer for fail-fast validation;
// this no-op implementation skips transactional outbox persistence.
type noopWriter struct{}

func (noopWriter) Write(_ context.Context, _ outbox.Entry) error { return nil }

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// In-memory event bus (publisher + subscriber).
	eb := eventbus.New()

	// RSA key pair for JWT signing/verification (development only).
	privKey, pubKey := auth.MustGenerateTestKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey)
	if err != nil {
		logger.Error("failed to create key set", slog.Any("error", err))
		os.Exit(1)
	}
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "sso-bff-dev", 15*time.Minute)
	if err != nil {
		logger.Error("failed to create JWT issuer", slog.Any("error", err))
		os.Exit(1)
	}
	jwtVerifier, err := auth.NewJWTVerifier(keySet)
	if err != nil {
		logger.Error("failed to create JWT verifier", slog.Any("error", err))
		os.Exit(1)
	}

	// Shared noop outbox writer for all L2+ Cells.
	var nw outbox.Writer = noopWriter{}

	// --- access-core (L2): identity, session, RBAC ---
	ac := accesscore.NewAccessCore(
		accesscore.WithInMemoryDefaults(),
		accesscore.WithPublisher(eb),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithOutboxWriter(nw),
		accesscore.WithLogger(logger),
	)

	// --- audit-core (L3): tamper-evident audit log ---
	auditHMACKey := []byte("sso-bff-dev-hmac-key-32-bytes!!!")
	auc := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithPublisher(eb),
		auditcore.WithHMACKey(auditHMACKey),
		auditcore.WithOutboxWriter(nw),
		auditcore.WithLogger(logger),
	)

	// --- config-core (L2): configuration + feature flags ---
	cc := configcore.NewConfigCore(
		configcore.WithInMemoryDefaults(),
		configcore.WithPublisher(eb),
		configcore.WithOutboxWriter(nw),
		configcore.WithLogger(logger),
	)

	// Build assembly and register all three Cells.
	asm := assembly.New(assembly.Config{ID: "sso-bff"})
	for _, err := range []error{
		asm.Register(ac),
		asm.Register(auc),
		asm.Register(cc),
	} {
		if err != nil {
			logger.Error("failed to register cell", slog.Any("error", err))
			os.Exit(1)
		}
	}

	// Bootstrap handles: assembly.Start -> route registration -> event subscriptions
	// -> HTTP server -> graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithEventBus(eb),
		bootstrap.WithHTTPAddr(":8081"),
	)

	logger.Info("sso-bff: starting on :8081",
		slog.String("mode", "in-memory"),
		slog.Int("cells", 3),
	)
	if err := app.Run(ctx); err != nil {
		logger.Error("sso-bff: application exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}
