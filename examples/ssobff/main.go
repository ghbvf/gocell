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

	"github.com/ghbvf/gocell/framework/pkg/redaction"
	"github.com/ghbvf/gocell/framework/runtime/observability/logging"
	"github.com/ghbvf/gocell/framework/runtime/shutdown"
)

func main() {
	// Fail-closed sink-side redaction: seal the process-global slog default with
	// the redacting handler before any work, so every slog.Default() call is
	// scrubbed (SLOG-HANDLER-SEALED-FUNNEL-01). logger reuses the sealed default.
	slog.SetDefault(slog.New(logging.NewHandler(logging.Options{Format: logging.FormatJSON})))
	logger := slog.Default()

	app, err := NewSSOBFFApp(WithSSOBFFLogger(logger))
	if err != nil {
		logger.Error("failed to build ssobff app", slog.String("error", redaction.RedactError(err).Error()))
		os.Exit(1)
	}

	// Bootstrap handles: assembly.Start -> route registration -> event subscriptions
	// -> HTTP server -> graceful shutdown.
	ctx, stop := shutdown.NotifyContext(context.Background())
	defer stop()

	logger.Info("ssobff: starting",
		slog.String("mode", "durable-pg"),
		slog.Int("cells", 3),
		slog.String("primary_listen_addr", app.PrimaryListenAddr()),
		slog.String("internal_listen_addr", app.InternalListenAddr()),
		slog.String("health_listen_addr", app.HealthListenAddr()),
	)
	if err := app.Run(ctx); err != nil {
		logger.Error("ssobff: application exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}
