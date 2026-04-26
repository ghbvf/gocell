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
// # Required env vars (all adapter modes)
//
//   - GOCELL_SERVICE_SECRET: HMAC-SHA256 secret (≥32 bytes) protecting
//     /internal/v1/* paths via ServiceTokenMiddleware. Must be set in all
//     adapter modes — missing in any mode aborts startup with
//     ERR_CONTROLPLANE_SERVICE_SECRET_MISSING (SEC-FAIL-CLOSED, PR-MODE-1).
//
// See also: docs/ops/env-vars.md for the full env var reference.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/shutdown"
)

func main() {
	ctx, cancel := shutdown.NotifyContext(context.Background())
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

	// Module order is load-bearing: ConfigCoreModule MUST run first because it
	// creates the shared *adapterpg.Pool and writes it to shared.SharedPGPool.
	// AccessCore + AuditCore read SharedPGPool to wire their outbox writers in
	// postgres mode. Reordering here will trigger ERR_CELL_MISSING_OUTBOX at
	// startup. See cmd/corebundle/{access,audit}_module.go.
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

	consumerBase, err := buildConsumerBase(shared)
	if err != nil {
		return err
	}

	metricsHandler := shared.metricsHandler

	adapterInfo := adapterInfoForSharedDeps(shared)
	slog.Info("corebundle: startup configuration",
		slog.String("adapter_mode", adapterInfo["mode"]),
		slog.String("storage", adapterInfo["storage"]),
		slog.String("event_bus", adapterInfo["event_bus"]),
		slog.String("outbox_storage", adapterInfo["outbox_storage"]),
		slog.String("redis", adapterInfo["redis"]),
		slog.String("service_token_nonce_store", adapterInfo["service_token_nonce_store"]),
		slog.String("outbox_consumer_claimer", adapterInfo["outbox_consumer_claimer"]))

	logSinglePodNonceStoreAcknowledgement(shared)
	logInitialAdminCredPath()

	opts := defaultRuntimeOptions(shared, asm, consumerBase, metricsHandler, adapterInfo)
	opts = append(opts, cellOpts...)

	return bootstrap.New(opts...).Run(ctx)
}

// logSinglePodNonceStoreAcknowledgement emits a positive-path Info log when
// the deployment is real-mode + single-pod + InMemory NonceStore — i.e. the
// operator explicitly opted into single-pod replay protection via
// GOCELL_SINGLE_POD=1 and accepted in-memory nonce semantics. This makes the
// "I know what I'm doing" choice visible in startup logs.
//
// Multi-pod + real + InMemory is rejected upstream by SharedDeps.Validate
// (ERR_CONTROLPLANE_NONCE_STORE_MISSING), so this function does not emit a
// defensive Warn for that case — that path is unreachable in normal startup,
// and a log here would be dead code.
func logSinglePodNonceStoreAcknowledgement(shared *SharedDeps) {
	if shared == nil || shared.InternalGuard == nil {
		return
	}
	ns := shared.InternalGuard.NonceStore()
	if ns == nil || ns.Kind() != auth.NonceStoreKindInMemory {
		return
	}
	if !shared.Topology.RequireProductionControlPlane() ||
		!shared.Topology.SinglePodReplayProtection {
		return
	}
	slog.Info("controlplane: in-memory nonce store acknowledged for single-pod deployment",
		slog.String("nonce_store_kind", string(ns.Kind())),
		slog.String("note", "GOCELL_SINGLE_POD=1 set; multi-pod deployments must inject a distributed NonceStore via WithServiceTokenNonceStore"))
}
