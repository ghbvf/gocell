// Package main is the entry point for the todoorder example application.
// It demonstrates the GoCell "golden path": creating a business Cell with
// HTTP endpoints and outbox-based event publishing.
//
// Demo mode injects NoopWriter + NoopTxRunner for a unified code path.
// Events are validated but discarded (no real broker). Production mode
// would inject a real outbox.Writer + persistence.TxRunner instead.
//
// Usage:
//
//	go run ./examples/todoorder
package main

import (
	"context"
	"log/slog"
	"os"
	"time"

	ordercell "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/shutdown"
)

// Demo bearer token for the unauthenticated quick-start path. See README.md
// for the curl examples that exercise the role-gated endpoints.
const demoCustomerToken = "todoorder-customer-demo-token"

// demoTokenVerifier is a minimal IntentTokenVerifier that accepts a single
// hard-coded bearer token and issues a Principal carrying RoleCustomer.
// It exists purely so the example's role-gated business routes (PR-CFG-C)
// remain reachable from the documented quick-start path.
type demoTokenVerifier struct{}

func (demoTokenVerifier) VerifyIntent(_ context.Context, token string, expected auth.TokenIntent) (auth.Claims, error) {
	if expected != auth.TokenIntentAccess || token != demoCustomerToken {
		return auth.Claims{}, errcode.New(errcode.ErrAuthUnauthorized, "invalid demo token")
	}
	now := time.Now()
	return auth.Claims{
		Subject:   "todoorder-demo-customer",
		Issuer:    "todoorder-demo",
		Audience:  []string{"gocell"},
		IssuedAt:  now,
		ExpiresAt: now.Add(8 * time.Hour),
		Roles:     []string{ordercell.RoleCustomer},
		TokenUse:  auth.TokenIntentAccess,
	}, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// Cursor codec for pagination (demo mode).
	cursorCodec, err := query.NewCursorCodec([]byte("todoorder-cursor-key-32bytes!!!!"))
	if err != nil {
		logger.Error("failed to create cursor codec", slog.Any("error", err))
		os.Exit(1)
	}

	// Create the order cell with in-memory defaults.
	// Demo mode: NoopWriter + NoopTxRunner → unified outbox code path (zero fork).
	// Events are validated by NoopWriter then discarded. In production, inject
	// a real outbox.Writer (e.g., postgres.OutboxWriter) + persistence.TxRunner
	// (e.g., postgres.TxManager) for durable event delivery via relay.
	oc := ordercell.NewOrderCell(
		ordercell.WithOutboxWriter(outbox.NoopWriter{}),
		ordercell.WithTxManager(persistence.NoopTxRunner{}),
		ordercell.WithCursorCodec(cursorCodec),
		ordercell.WithLogger(logger),
	)

	// Build assembly and register the cell.
	asm := assembly.New(assembly.Config{ID: "todoorder", DurabilityMode: cell.DurabilityDemo})
	if err := asm.Register(oc); err != nil {
		logger.Error("failed to register ordercell", slog.Any("error", err))
		os.Exit(1)
	}

	// Bootstrap the application on :8082.
	ctx, stop := shutdown.NotifyContext(context.Background())
	defer stop()

	// PR-A35 + PR-A14b: /readyz?verbose is policy-gated. When the operator
	// sets GOCELL_READYZ_VERBOSE_TOKEN, attach PolicyVerboseToken to the
	// readyz route group; otherwise waive the verbose endpoint via
	// WithReadyzVerboseDisabled so the demo binary keeps starting out of the
	// box without exposing internal topology anonymously.
	healthOpts := []bootstrap.HealthRouteGroupOption{}
	if tok := os.Getenv("GOCELL_READYZ_VERBOSE_TOKEN"); tok != "" {
		healthOpts = append(healthOpts, bootstrap.WithReadyzPolicy(
			bootstrap.PolicyVerboseToken("X-Readyz-Token", tok)))
	} else {
		healthOpts = append(healthOpts, bootstrap.WithReadyzVerboseDisabled())
	}

	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, ":8082", bootstrap.PolicyJWT(demoTokenVerifier{})),
		bootstrap.WithListener(cell.InternalListener, ":9082", cell.Policy{}),
		bootstrap.WithHealthRoutes(healthOpts...),
	)

	logger.Info("todoorder: starting on :8082; protected routes require the documented demo bearer token")
	if err := app.Run(ctx); err != nil {
		logger.Error("todoorder: application exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}
