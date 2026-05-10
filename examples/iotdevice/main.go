// Package main is the entry point for the iotdevice example application.
// It demonstrates the L4 DeviceLatent consistency model: commands are enqueued
// by the server and polled by IoT devices on their own schedule.
//
// Usage:
//
//	go run ./examples/iotdevice
package main

import (
	"context"
	"log/slog"
	"os"

	devicecell "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/shutdown"
)

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

	// Single root clock shared by assembly, bootstrap, eventbus, and cell.
	// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
	clk := clock.Real()

	// In-memory event bus for demo mode.
	eb := eventbus.New(eventbus.WithClock(clk))

	// Cursor codec for pagination (demo mode).
	cursorCodec, err := query.NewCursorCodec([]byte("iotdevice-cursor-key-32-bytes!!!"))
	if err != nil {
		logger.Error("failed to create cursor codec", slog.Any("error", err))
		os.Exit(1)
	}

	// Create the device cell with in-memory defaults.
	dc := devicecell.NewDeviceCell(
		devicecell.WithClock(clk),
		devicecell.WithDirectPublisher(eb),
		devicecell.WithCursorCodec(cursorCodec),
		devicecell.WithLogger(logger),
	)

	// Build assembly and register the cell.
	asm := assembly.New(assembly.Config{ID: "iotdevice", DurabilityMode: cell.DurabilityDemo, Clock: clk})
	if err := asm.Register(dc); err != nil {
		logger.Error("failed to register devicecell", slog.Any("error", err))
		os.Exit(1)
	}

	// Bootstrap the application on :8083.
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
		bootstrap.WithClock(clk),
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithListener(cell.PrimaryListener, ":8083", []cell.ListenerAuth{cell.MustNewAuthJWT(jwtVerifier)}),
		bootstrap.WithListener(cell.InternalListener, ":9083", internalAuthChain),
		bootstrap.WithHealthRoutes(healthOpts...),
	)

	logger.Info("iotdevice: starting on :8083; protected routes require an RS256 bearer token")
	if err := app.Run(ctx); err != nil {
		logger.Error("iotdevice: application exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}
