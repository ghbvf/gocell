// Package main is the entry point for the iot-device example application.
// It demonstrates the L4 DeviceLatent consistency model: commands are enqueued
// by the server and polled by IoT devices on their own schedule.
//
// Usage:
//
//	go run ./examples/iot-device
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	devicecell "github.com/ghbvf/gocell/cells/device-cell"
	"github.com/ghbvf/gocell/kernel/assembly"
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

	// Create the device cell with in-memory defaults.
	dc := devicecell.NewDeviceCell(
		devicecell.WithPublisher(eb),
		devicecell.WithLogger(logger),
	)

	// Build assembly and register the cell.
	asm := assembly.New(assembly.Config{ID: "iot-device"})
	if err := asm.Register(dc); err != nil {
		logger.Error("failed to register device-cell", slog.Any("error", err))
		os.Exit(1)
	}

	// Bootstrap the application on :8083.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithEventBus(eb),
		bootstrap.WithHTTPAddr(":8083"),
	)

	logger.Info("iot-device: starting on :8083")
	if err := app.Run(ctx); err != nil {
		logger.Error("iot-device: application exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}
