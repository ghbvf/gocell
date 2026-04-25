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
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/shutdown"
)

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

	// PR-A35: /readyz?verbose is token-gated in every mode. Honour
	// GOCELL_READYZ_VERBOSE_TOKEN if the operator sets one (matches the
	// curl example in README.md); otherwise waive the verbose endpoint so
	// the demo binary keeps starting out of the box without exposing
	// internal topology anonymously.
	readyzOpts := []bootstrap.Option{}
	if tok := os.Getenv("GOCELL_READYZ_VERBOSE_TOKEN"); tok != "" {
		readyzOpts = append(readyzOpts, bootstrap.WithVerboseToken(tok))
	} else {
		readyzOpts = append(readyzOpts, bootstrap.WithVerboseDisabled())
	}

	opts := []bootstrap.Option{
		bootstrap.WithAssembly(asm),
		bootstrap.WithHTTPPrimaryAddr(":8082"), bootstrap.WithHTTPInternalAddr(":9082"),
	}
	opts = append(opts, readyzOpts...)
	app := bootstrap.New(opts...)

	logger.Info("todoorder: starting on :8082")
	if err := app.Run(ctx); err != nil {
		logger.Error("todoorder: application exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}
