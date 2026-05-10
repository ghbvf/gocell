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

	ordercell "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/shutdown"
)

// demoTxRunner is a pass-through TxRunner for demo mode: executes fn directly
// without a database transaction (no L2 atomicity). Production assemblies must
// inject a real TxRunner (e.g., postgres.TxManager).
type demoTxRunner struct{}

func (demoTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
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
	jwtVerifier, err := newJWTVerifierFromEnv()
	if err != nil {
		logger.Error("failed to configure JWT verifier", slog.Any("error", err))
		os.Exit(1)
	}

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
		ordercell.WithTxManager(demoTxRunner{}),
		ordercell.WithCursorCodec(cursorCodec),
		ordercell.WithLogger(logger),
	)

	// Build assembly and register the cell.
	asm := assembly.New(assembly.Config{ID: "todoorder", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	if err := asm.Register(oc); err != nil {
		logger.Error("failed to register ordercell", slog.Any("error", err))
		os.Exit(1)
	}

	// Bootstrap the application on :8082.
	ctx, stop := shutdown.NotifyContext(context.Background())
	defer stop()

	// PR-A35 + PR269 round-3: /readyz?verbose is gated by the health handler's
	// strict X-Readyz-Token check. When the operator sets
	// GOCELL_READYZ_VERBOSE_TOKEN, plumb it via WithReadyzVerboseToken;
	// otherwise waive the verbose endpoint via WithReadyzVerboseDisabled so the
	// demo binary keeps starting out of the box without exposing internal
	// topology anonymously.
	healthOpts := []bootstrap.HealthRouteGroupOption{}
	if tok := os.Getenv("GOCELL_READYZ_VERBOSE_TOKEN"); tok != "" {
		healthOpts = append(healthOpts, bootstrap.WithReadyzVerboseToken(tok))
	} else {
		healthOpts = append(healthOpts, bootstrap.WithReadyzVerboseDisabled())
	}

	app := bootstrap.New(
		bootstrap.WithClock(clock.Real()),
		bootstrap.WithAssembly(asm),
		bootstrap.WithListener(cell.PrimaryListener, ":8082",
			[]cell.ListenerAuth{cell.MustNewAuthJWT(jwtVerifier)}),
		bootstrap.WithListener(cell.InternalListener, ":9082", internalAuthChain),
		bootstrap.WithHealthRoutes(healthOpts...),
	)

	logger.Info("todoorder: starting on :8082; protected routes require an RS256 bearer token")
	if err := app.Run(ctx); err != nil {
		logger.Error("todoorder: application exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}
