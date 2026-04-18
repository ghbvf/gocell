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
	"github.com/ghbvf/gocell/runtime/worker"
)

// noopTxRunner executes fn directly without a real transaction (demo mode).
type noopTxRunner struct{}

func (noopTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

var _ persistence.TxRunner = noopTxRunner{}

// ssoBFFLazyWorker defers worker resolution to Start/Stop time so that a
// worker.Worker produced during asm.Init can be registered with
// bootstrap.WithWorkers before bootstrap.New is called.
// get() returns nil when admin already existed (bootstrap skipped), making
// Start and Stop safe no-ops.
type ssoBFFLazyWorker struct {
	get func() worker.Worker
}

func (l *ssoBFFLazyWorker) Start(ctx context.Context) error {
	if w := l.get(); w != nil {
		return w.Start(ctx)
	}
	return nil
}

func (l *ssoBFFLazyWorker) Stop(ctx context.Context) error {
	if w := l.get(); w != nil {
		return w.Stop(ctx)
	}
	return nil
}

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
	jwtVerifier, err := auth.NewJWTVerifier(keySet, auth.WithExpectedAudiences("gocell"))
	if err != nil {
		logger.Error("failed to create JWT verifier", slog.Any("error", err))
		os.Exit(1)
	}

	// Shared noop outbox writer for all L2+ Cells.
	var nw outbox.Writer = outbox.NoopWriter{}

	// Lazy bootstrap worker: resolves the cleanup worker at WorkerGroup.Start() time
	// (bootstrap Step 8), after asm.StartWithConfig has fired the sink (Step 3-4).
	// No-op when admin already existed and sink was never called.
	var adminBootstrapWorker worker.Worker
	bootstrapSink := func(w worker.Worker) { adminBootstrapWorker = w }

	// --- access-core (L2): identity, session, RBAC ---
	ac := accesscore.NewAccessCore(
		accesscore.WithInMemoryDefaults(),
		accesscore.WithInitialAdminBootstrap(),
		accesscore.WithBootstrapWorkerSink(bootstrapSink),
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

	// lazyAdminWorker defers resolution to Start() time (after asm.StartWithConfig
	// fires the sink). No-op when admin already existed.
	lazyAdminWorker := &ssoBFFLazyWorker{get: func() worker.Worker { return adminBootstrapWorker }}

	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithHTTPAddr(":8081"),
		bootstrap.WithPublicEndpoints(publicEndpoints),
		bootstrap.WithPasswordResetExemptEndpoints([]string{
			"POST /api/v1/access/users/{id}/password",
			"DELETE /api/v1/access/sessions/{id}",
		}),
		bootstrap.WithPasswordResetChangeEndpointHint("POST /api/v1/access/users/{id}/password"),
		bootstrap.WithWorkers(lazyAdminWorker),
	)

	stateDir := os.Getenv("GOCELL_STATE_DIR")
	if stateDir == "" {
		stateDir = "/run/gocell"
	}
	credPath := stateDir + "/initial_admin_password"
	logger.Info("sso-bff: starting on :8081; if first run, initial admin credentials are written to "+credPath,
		slog.String("mode", "in-memory"),
		slog.Int("cells", 3),
		slog.String("cred_path", credPath),
	)
	if err := app.Run(ctx); err != nil {
		logger.Error("sso-bff: application exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}
