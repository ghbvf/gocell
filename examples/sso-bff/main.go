// Package main is the entry point for the sso-bff example application.
// It demonstrates combining the three built-in GoCell Cells (access-core,
// audit-core, config-core) into a single SSO BFF assembly using in-memory
// dependencies for development.
//
// Usage:
//
//	go run ./examples/sso-bff
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	accesscore "github.com/ghbvf/gocell/cells/access-core"
	auditcore "github.com/ghbvf/gocell/cells/audit-core"
	configcore "github.com/ghbvf/gocell/cells/config-core"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

func generateDevPassword() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate dev password: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// noopTxRunner executes fn directly without a real transaction (demo mode).
type noopTxRunner struct{}

func (noopTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

var _ persistence.TxRunner = noopTxRunner{}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// In-memory event bus (publisher + subscriber).
	// Production RabbitMQ wiring: see P3-DEFER-03 (blocked on Batch 5: WM-17 + ER-ARCH-02).
	eb := eventbus.New()

	// RSA key pair for JWT signing/verification (development only).
	// Production: use auth.LoadKeySetFromEnv() which reads from env vars and
	// supports key rotation via GOCELL_JWT_PREV_PUBLIC_KEY + GOCELL_JWT_PREV_KEY_EXPIRES.
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
	jwtVerifier, err := auth.NewJWTVerifier(keySet, auth.WithExpectedAudiences(auth.DefaultJWTAudience))
	if err != nil {
		logger.Error("failed to create JWT verifier", slog.Any("error", err))
		os.Exit(1)
	}

	// Shared noop outbox writer for all L2+ Cells.
	var nw outbox.Writer = outbox.NoopWriter{}

	// Dev-only seed admin: in-memory store resets on every restart.
	seedAdminPass, err := generateDevPassword()
	if err != nil {
		logger.Error("sso-bff: failed to generate seed admin password", slog.Any("error", err))
		os.Exit(1)
	}

	// --- access-core (L2): identity, session, RBAC ---
	ac := accesscore.NewAccessCore(
		accesscore.WithInMemoryDefaults(),
		accesscore.WithSeedAdmin("admin", seedAdminPass),
		accesscore.WithPublisher(eb),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithOutboxWriter(nw),
		accesscore.WithTxManager(noopTxRunner{}),
		accesscore.WithLogger(logger),
	)

	// --- audit-core (L3): tamper-evident audit log ---
	// 32 bytes: matches SHA-256 block size used by the audit HMAC chain.
	auditHMACKey := []byte("sso-bff-dev-hmac-key-32-bytes!!!")
	auditCursorCodec, err := query.NewCursorCodec([]byte("sso-bff-audit-cursor-key-32b!!"))
	if err != nil {
		logger.Error("failed to create audit cursor codec", slog.Any("error", err))
		os.Exit(1)
	}
	auc := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithPublisher(eb),
		auditcore.WithHMACKey(auditHMACKey),
		auditcore.WithOutboxWriter(nw),
		auditcore.WithTxManager(noopTxRunner{}),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithLogger(logger),
	)

	// --- config-core (L2): configuration + feature flags ---
	configCursorCodec, err := query.NewCursorCodec([]byte("sso-bff-config-cursor-key-32b!!"))
	if err != nil {
		logger.Error("failed to create config cursor codec", slog.Any("error", err))
		os.Exit(1)
	}
	cc := configcore.NewConfigCore(
		configcore.WithInMemoryDefaults(),
		configcore.WithPublisher(eb),
		configcore.WithOutboxWriter(nw),
		configcore.WithTxManager(noopTxRunner{}),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithLogger(logger),
	)

	// Build assembly and register all three Cells.
	asm := assembly.New(assembly.Config{ID: "sso-bff", DurabilityMode: cell.DurabilityDemo})
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

	// Public endpoints — login and refresh accessible without JWT.
	publicEndpoints := []string{
		"POST /api/v1/access/sessions/login",
		"POST /api/v1/access/sessions/refresh",
	}

	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithHTTPAddr(":8081"),
		bootstrap.WithPublicEndpoints(publicEndpoints),
	)

	logger.Info("sso-bff: seed admin ready — use these credentials to log in",
		slog.String("username", "admin"),
		slog.String("password", seedAdminPass),
		slog.String("note", "dev-only, resets on restart"),
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
