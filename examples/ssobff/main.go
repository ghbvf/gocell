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
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/shutdown"
)

// envOr returns os.Getenv(key) when set; otherwise the fallback. Used to
// override listener addresses without rebuilding the binary — the smoke
// regression guard injects high ports to avoid colliding with developer
// docker/dev-server bindings on the canonical 8081/9081/9091 trio.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	internalAuthChain, err := newInternalAuthChainFromEnv()
	if err != nil {
		logger.Error("failed to configure internal listener auth", slog.Any("error", err))
		os.Exit(1)
	}

	// In-memory event bus (publisher + subscriber).
	// Production RabbitMQ wiring: see P3-DEFER-03 (blocked on Batch 5: WM-17 + ER-ARCH-02).
	eb := eventbus.New()

	// DO NOT COPY TO PRODUCTION: RSA key pair is generated in-process every
	// boot, so all signed tokens become invalid on restart and there is no
	// way to verify tokens issued by another replica. Use auth.LoadKeySetFromEnv()
	// which reads from env vars and supports key rotation via
	// GOCELL_JWT_PREV_PUBLIC_KEY + GOCELL_JWT_PREV_KEY_EXPIRES.
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
		accesscore.WithTxManager(persistence.NoopTxRunner{}),
		accesscore.WithLogger(logger),
		accesscore.WithRefreshMetricsProvider(metrics.NopProvider{}), // dev only — replace with Prometheus-backed Provider in production
	)

	// --- auditcore (L3): tamper-evident audit log ---
	// DO NOT COPY TO PRODUCTION: this HMAC key is a public string constant;
	// any audit log signed with it is forgeable by anyone reading the demo
	// source. Production deployments must inject a fresh 32-byte secret via
	// secrets manager / Vault. 32 bytes matches SHA-256 block size used by
	// the audit HMAC chain.
	auditHMACKey := []byte("ssobff-dev-hmac-key-32-bytes!!!!")
	// DO NOT COPY TO PRODUCTION: cursor HMAC key is a public string; an
	// attacker who can read the demo source can forge or read pagination
	// cursors. Use a fresh secret in production.
	auditCursorCodec, err := query.NewCursorCodec([]byte("ssobff-audit-cursor-key-32bytes!"))
	if err != nil {
		logger.Error("failed to create audit cursor codec", slog.Any("error", err))
		os.Exit(1)
	}
	auc := auditcore.NewAuditCore(
		auditcore.WithInMemoryDefaults(),
		auditcore.WithOutboxDeps(eb, nw),
		auditcore.WithHMACKey(auditHMACKey),
		auditcore.WithTxManager(persistence.NoopTxRunner{}),
		auditcore.WithCursorCodec(auditCursorCodec),
		auditcore.WithLogger(logger),
		auditcore.WithMetricsProvider(metrics.NopProvider{}), // dev only — replace with Prometheus-backed Provider in production
	)

	// --- configcore (L2): configuration + feature flags ---
	// DO NOT COPY TO PRODUCTION — same reason as auditCursorCodec above.
	configCursorCodec, err := query.NewCursorCodec([]byte("ssobff-config-cursor-key-32bytes"))
	if err != nil {
		logger.Error("failed to create config cursor codec", slog.Any("error", err))
		os.Exit(1)
	}
	cc := configcore.NewConfigCore(
		configcore.WithInMemoryDefaults(),
		configcore.WithOutboxDeps(eb, nw),
		configcore.WithTxManager(persistence.NoopTxRunner{}),
		configcore.WithCursorCodec(configCursorCodec),
		configcore.WithLogger(logger),
		configcore.WithMetricsProvider(metrics.NopProvider{}), // dev only — replace with Prometheus-backed Provider in production
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
	// accesscore Cell itself via auth.Mount; cell.NewAuthJWTFromAssembly(asm)
	// in the []cell.ListenerAuth authChain resolves the verifier from accesscore
	// at phase4 and Bootstrap installs the matcher-aware AuthMiddleware on
	// the primary listener's router (PR262 typed auth plan).
	//
	// PR-A35 + PR-A14b: /readyz?verbose is policy-gated. Honour
	// GOCELL_READYZ_VERBOSE_TOKEN if the operator sets one; otherwise waive
	// the verbose endpoint via WithReadyzVerboseDisabled so this demo runs
	// out of the box without exposing internal topology anonymously.
	healthOpts := []bootstrap.HealthRouteGroupOption{}
	if tok := os.Getenv("GOCELL_READYZ_VERBOSE_TOKEN"); tok != "" {
		healthOpts = append(healthOpts, bootstrap.WithReadyzVerboseToken(tok))
	} else {
		healthOpts = append(healthOpts, bootstrap.WithReadyzVerboseDisabled())
	}

	// Listener address defaults follow docs/ops/listener-topology.md:
	// primary on :8081 (public), internal + health on loopback (control
	// plane / probes never face the public network without explicit
	// override). All three accept ENV overrides for smoke tests and
	// containerised deployments.
	primaryAddr := envOr("GOCELL_SSOBFF_PRIMARY_ADDR", ":8081")
	internalAddr := envOr("GOCELL_SSOBFF_INTERNAL_ADDR", "127.0.0.1:9081")
	healthAddr := envOr("GOCELL_SSOBFF_HEALTH_ADDR", "127.0.0.1:9091")

	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithListener(cell.PrimaryListener, primaryAddr, []cell.ListenerAuth{cell.NewAuthJWTFromAssembly(asm)}),
		bootstrap.WithListener(cell.InternalListener, internalAddr, internalAuthChain),
		// Dedicated health listener on a loopback port — keeps /healthz, /readyz,
		// /metrics off the public port (8081) so probes and metric scrapers do
		// not share the auth-fronted business surface. Also gives operators a
		// stable port to script readiness against (referenced by
		// examples/ssobff/smoke_test.go and docs/ops/listener-topology.md).
		bootstrap.WithListener(cell.HealthListener, healthAddr, nil),
		bootstrap.WithHealthRoutes(healthOpts...),
		// Bootstrap phase3b auto-discovers LifecycleHooks() from accesscore.
	)

	// Pass "" so per-OS platform defaults in ResolveBootstrapCredentialPath kick in.
	// Override with GOCELL_STATE_DIR to redirect to a custom directory.
	credPath, err := accesscore.ResolveBootstrapCredentialPath(os.Getenv("GOCELL_STATE_DIR"))
	if err != nil {
		logger.Warn("ssobff: failed to resolve bootstrap credential path",
			slog.Any("error", err))
		credPath = "<unresolved>"
	}
	logger.Info("ssobff: starting; if first run, initial admin credentials are written to cred_path",
		slog.String("mode", "in-memory"),
		slog.Int("cells", 3),
		slog.String("primary_addr", primaryAddr),
		slog.String("internal_addr", internalAddr),
		slog.String("health_addr", healthAddr),
		slog.String("cred_path", credPath),
	)
	if err := app.Run(ctx); err != nil {
		logger.Error("ssobff: application exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}
