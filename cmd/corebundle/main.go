// Package main is the entry point for the corebundle assembly.
// It bootstraps configcore, accesscore, and auditcore with in-memory
// repositories by default, suitable for development and integration testing.
//
// DurabilityDemo is used with memory storage so Cells auto-fill explicit no-op
// dependencies. Set GOCELL_CELL_ADAPTER_MODE=postgres to switch the assembly
// to DurabilityDurable and require real writer/tx dependencies; set
// GOCELL_ADAPTER_MODE=real to enable production control-plane secret checks.
//
// # Required env vars (all adapter modes)
//
//   - GOCELL_JWT_ISSUER: JWT iss claim written into tokens and verified on
//     inbound requests via VerifyIntent. Must be set before startup.
//
//   - GOCELL_JWT_AUDIENCE: JWT aud claim written into tokens and verified on
//     inbound requests via VerifyIntent. Must be set before startup.
//
// # Required env vars (real adapter mode only)
//
//   - GOCELL_SERVICE_SECRET: HMAC-SHA256 secret (≥32 bytes) protecting
//     /internal/v1/* paths via ServiceTokenMiddleware. Missing in real mode
//     aborts startup; missing in dev mode disables the guard with a Warn log.
//
// See also: docs/ops/env-vars.md for the full env var reference.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ghbvf/gocell/runtime/bootstrap"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx); err != nil {
		slog.Error("application failed", "error", err)
		os.Exit(1)
	}
}

// run is the application entry point: parse environment, assemble, and run.
// Extracted from main() for testability.
// Flow: LoadSharedDepsFromEnv → BuildApp → buildAssembly → bootstrap.Run.
// Adding a new Cell: implement CellModule and append to BuildApp call below.
//
// ref: uber-go/fx app.go Run — thin wrapper around Start/Stop lifecycle.
// ref: R1d PR#203 — CellModule + BuildApp replaces AppDeps God Struct.
func run(ctx context.Context) error {
	shared, err := LoadSharedDepsFromEnv(ctx)
	if err != nil {
		return err
	}

	cells, cellOpts, err := BuildApp(ctx, shared,
		ConfigCoreModule{},
		AccessCoreModule{},
		AuditCoreModule{},
	)
	if err != nil {
		return err
	}

	asm, err := buildAssembly(shared.PromStack, durabilityModeForTopology(shared.Topology), cells...)
	if err != nil {
		return fmt.Errorf("build assembly: %w", err)
	}

	consumerBase, err := buildConsumerBase()
	if err != nil {
		return err
	}

	metricsHandler := shared.metricsHandler

	adapterInfo := shared.Topology.AdapterInfo()
	slog.Info("corebundle: startup configuration",
		slog.String("adapter_mode", adapterInfo["mode"]),
		slog.String("storage", adapterInfo["storage"]),
		slog.String("event_bus", adapterInfo["event_bus"]),
		slog.String("outbox_storage", adapterInfo["outbox_storage"]))

	logInitialAdminCredPath()

	opts := defaultRuntimeOptions(shared, asm, consumerBase, metricsHandler, adapterInfo)
	opts = append(opts, cellOpts...)

	return bootstrap.New(opts...).Run(ctx)
}
