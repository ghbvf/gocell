// Package main is the entry point for the todo-order example application.
// It demonstrates the GoCell "golden path": creating a business Cell with
// HTTP endpoints and in-memory event publishing.
//
// Usage:
//
//	go run ./examples/todo-order
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	ordercell "github.com/ghbvf/gocell/cells/order-cell"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// In-memory event bus for demo mode.
	eb := eventbus.New()

	// Cursor codec for pagination (demo mode).
	cursorCodec, err := query.NewCursorCodec([]byte("todo-order-cursor-key-32bytes!!"))
	if err != nil {
		logger.Error("failed to create cursor codec", slog.Any("error", err))
		os.Exit(1)
	}

	// Create the order cell with in-memory defaults.
	// Demo mode: NoopWriter + NoopTxRunner → unified outbox code path (zero fork).
	oc := ordercell.NewOrderCell(
		ordercell.WithOutboxWriter(outbox.NoopWriter{}),
		ordercell.WithTxManager(persistence.NoopTxRunner{}),
		ordercell.WithPublisher(eb),
		ordercell.WithCursorCodec(cursorCodec),
		ordercell.WithLogger(logger),
	)

	// Build assembly and register the cell.
	asm := assembly.New(assembly.Config{ID: "todo-order"})
	if err := asm.Register(oc); err != nil {
		logger.Error("failed to register order-cell", slog.Any("error", err))
		os.Exit(1)
	}

	// Bootstrap the application on :8082.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithHTTPAddr(":8082"),
	)

	logger.Info("todo-order: starting on :8082")
	if err := app.Run(ctx); err != nil {
		logger.Error("todo-order: application exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}
