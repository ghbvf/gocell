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

	"github.com/ghbvf/gocell/runtime/shutdown"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	app, err := NewSSOBFFApp(WithSSOBFFLogger(logger))
	if err != nil {
		logger.Error("failed to build ssobff app", slog.Any("error", err))
		os.Exit(1)
	}

	// Bootstrap handles: assembly.Start -> route registration -> event subscriptions
	// -> HTTP server -> graceful shutdown.
	ctx, stop := shutdown.NotifyContext(context.Background())
	defer stop()

	logger.Info("ssobff: starting",
		slog.String("mode", "in-memory"),
		slog.Int("cells", 3),
		slog.String("primary_addr", app.PrimaryAddr()),
		slog.String("internal_addr", app.InternalAddr()),
		slog.String("health_addr", app.HealthAddr()),
	)
	if err := app.Run(ctx); err != nil {
		logger.Error("ssobff: application exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}
