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
	"github.com/ghbvf/gocell/runtime/shutdown"
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
		accesscore.WithOutboxDeps(eb, nw),
		accesscore.WithJWTIssuer(jwtIssuer),
		accesscore.WithJWTVerifier(jwtVerifier),
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
		auditcore.WithOutboxDeps(eb, nw),
		auditcore.WithHMACKey(auditHMACKey),
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
		configcore.WithOutboxDeps(eb, nw),
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
	ctx, stop := shutdown.NotifyContext(context.Background())
	defer stop()

	// Public routes and password-reset-exempt routes are declared by the
	// accesscore Cell itself via auth.Mount. Bootstrap only needs the
	// opt-in signal that the assembly expects an auth provider cell.
	// PR-A35 + PR-A14b: /readyz?verbose is policy-gated. Honour
	// GOCELL_READYZ_VERBOSE_TOKEN if the operator sets one; otherwise waive
	// the verbose endpoint via WithReadyzVerboseDisabled so this demo runs
	// out of the box without exposing internal topology anonymously.
	healthOpts := []bootstrap.HealthRouteGroupOption{}
	if tok := os.Getenv("GOCELL_READYZ_VERBOSE_TOKEN"); tok != "" {
		healthOpts = append(healthOpts, bootstrap.WithReadyzPolicy(
			bootstrap.PolicyVerboseToken("X-Readyz-Token", tok)))
	} else {
		healthOpts = append(healthOpts, bootstrap.WithReadyzVerboseDisabled())
	}

	opts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithListener(cell.PrimaryListener, ":8081", cell.Policy{}),
		bootstrap.WithListener(cell.InternalListener, ":9081", cell.Policy{}),
		bootstrap.WithHealthRoutes(healthOpts...),
		bootstrap.PolicyJWTFromAssembly(asm),
		// Bootstrap phase3b auto-discovers LifecycleHooks() from accesscore.
	}
	app := bootstrap.New(opts...)

	// Pass "" so per-OS platform defaults in ResolveBootstrapCredentialPath kick in.
	// Override with GOCELL_STATE_DIR to redirect to a custom directory.
	credPath, err := accesscore.ResolveBootstrapCredentialPath(os.Getenv("GOCELL_STATE_DIR"))
	if err != nil {
		logger.Warn("ssobff: failed to resolve bootstrap credential path",
			slog.String("error", err.Error()))
		credPath = "<unresolved>"
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
