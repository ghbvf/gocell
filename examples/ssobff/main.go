// Package main is the entry point for the ssobff example application.
// It demonstrates combining the three built-in GoCell Cells (accesscore,
// auditcore, configcore) into a single SSO BFF assembly using in-memory
// dependencies for development.
//
// Usage:
//
//	go run ./examples/ssobff
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	auditcore "github.com/ghbvf/gocell/cells/auditcore"
	configcore "github.com/ghbvf/gocell/cells/configcore"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

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
	jwtIssuer, err := auth.NewJWTIssuer(keySet, "ssobff-dev", 15*time.Minute,
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	if err != nil {
		logger.Error("failed to create JWT issuer", slog.Any("error", err))
		os.Exit(1)
	}
	jwtVerifier, err := auth.NewJWTVerifier(keySet,
		auth.WithExpectedAudiences("gocell"),
		auth.WithExpectedIssuer("ssobff-dev"))
	if err != nil {
		logger.Error("failed to create JWT verifier", slog.Any("error", err))
		os.Exit(1)
	}

	// Shared noop outbox writer for all L2+ Cells.
	var nw outbox.Writer = outbox.NoopWriter{}

	// --- accesscore (L2): identity, session, RBAC ---
	// Bootstrap phase3b auto-discovers LifecycleHooks() from accesscore — no
	// worker.Lazy or sink plumbing needed.
	ac := accesscore.NewAccessCore(
		accesscore.WithInMemoryDefaults(),
		accesscore.WithInitialAdminBootstrap(),
		accesscore.WithPublisher(eb),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
		accesscore.WithOutboxWriter(nw),
		accesscore.WithTxManager(noopTxRunner{}),
		accesscore.WithLogger(logger),
	)

	// --- auditcore (L3): tamper-evident audit log ---
	// 32 bytes: matches SHA-256 block size used by the audit HMAC chain.
	auditHMACKey := []byte("ssobff-dev-hmac-key-32-bytes!!!!")
	auditCursorCodec, err := query.NewCursorCodec([]byte("ssobff-audit-cursor-key-32bytes!"))
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

	// --- configcore (L2): configuration + feature flags ---
	configCursorCodec, err := query.NewCursorCodec([]byte("ssobff-config-cursor-key-32bytes"))
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
	asm := assembly.New(assembly.Config{ID: "ssobff", DurabilityMode: cell.DurabilityDemo})
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

	stateDir := os.Getenv("GOCELL_STATE_DIR")
	if stateDir == "" {
		stateDir = "/run/gocell"
	}
	// Public routes and password-reset-exempt routes are declared by the
	// accesscore Cell itself via auth.Declare. Bootstrap only needs the
	// opt-in signal that the assembly expects an auth provider cell.
	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithHTTPPrimaryAddr(":8081"), bootstrap.WithHTTPInternalAddr(":9081"),
		bootstrap.WithAuthDiscovery(),
		// Bootstrap phase3b auto-discovers LifecycleHooks() from accesscore.
	)

	credPath, err := accesscore.ResolveBootstrapCredentialPath(stateDir)
	if err != nil {
		logger.Warn("ssobff: invalid GOCELL_STATE_DIR for credential path resolution",
			slog.String("error", err.Error()))
		credPath = stateDir + "/initial_admin_password"
	}
	logger.Info("ssobff: starting on :8081; if first run, initial admin credentials are written to the path below",
		slog.String("mode", "in-memory"),
		slog.Int("cells", 3),
		slog.String("cred_path", credPath),
	)
	if err := app.Run(ctx); err != nil {
		logger.Error("ssobff: application exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}
