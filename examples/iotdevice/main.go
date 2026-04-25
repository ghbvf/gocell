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
	"time"

	devicecell "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell"
	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/shutdown"
)

const demoAdminToken = "iotdevice-admin-demo-token"

type demoTokenVerifier struct{}

func (demoTokenVerifier) VerifyIntent(_ context.Context, token string, expected auth.TokenIntent) (auth.Claims, error) {
	if expected != auth.TokenIntentAccess || token != demoAdminToken {
		return auth.Claims{}, errcode.New(errcode.ErrAuthUnauthorized, "invalid demo token")
	}
	now := time.Now()
	return auth.Claims{
		Subject:   "iotdevice-demo-admin",
		Issuer:    "iotdevice-demo",
		Audience:  []string{"gocell"},
		IssuedAt:  now,
		ExpiresAt: now.Add(8 * time.Hour),
		Roles:     []string{"admin", "operator"},
		TokenUse:  auth.TokenIntentAccess,
	}, nil
}

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// In-memory event bus for demo mode.
	eb := eventbus.New()

	// Cursor codec for pagination (demo mode).
	cursorCodec, err := query.NewCursorCodec([]byte("iotdevice-cursor-key-32-bytes!!!"))
	if err != nil {
		logger.Error("failed to create cursor codec", slog.Any("error", err))
		os.Exit(1)
	}

	// Create the device cell with in-memory defaults.
	dc := devicecell.NewDeviceCell(
		devicecell.WithPublisher(eb),
		devicecell.WithCursorCodec(cursorCodec),
		devicecell.WithLogger(logger),
	)

	// Build assembly and register the cell.
	asm := assembly.New(assembly.Config{ID: "iotdevice", DurabilityMode: cell.DurabilityDemo})
	if err := asm.Register(dc); err != nil {
		logger.Error("failed to register devicecell", slog.Any("error", err))
		os.Exit(1)
	}

	// Bootstrap the application on :8083.
	ctx, stop := shutdown.NotifyContext(context.Background())
	defer stop()

	app := bootstrap.New(
		bootstrap.WithAssembly(asm),
		bootstrap.WithPublisher(eb), bootstrap.WithSubscriber(eb),
		bootstrap.WithListener(cell.PrimaryListener, ":8083", bootstrap.PolicyJWT(demoTokenVerifier{})),
		bootstrap.WithListener(cell.InternalListener, ":9083", cell.Policy{}),
	)

	logger.Info("iotdevice: starting on :8083; protected routes require the documented demo bearer token")
	if err := app.Run(ctx); err != nil {
		logger.Error("iotdevice: application exited with error", slog.Any("error", err))
		os.Exit(1)
	}
}
