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
//   - GOCELL_SERVICE_SECRET: HMAC-SHA256 secret (>=32 bytes) protecting
//     /internal/v1/* paths via ServiceTokenMiddleware. Must be set in all
//     adapter modes; missing in any mode aborts startup with
//     ERR_CONTROLPLANE_SERVICE_SECRET_MISSING (SEC-FAIL-CLOSED, PR-MODE-1).
//
// See also: docs/ops/env-vars.md for the full env var reference.
package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// runCorebundle is the handwritten runtime half behind the generated
// assembly entrypoint. The generated main.go owns the assembly ID and cell
// order; this function owns environment loading and runtime option wiring.
func runCorebundle(ctx context.Context, assemblyID string, assemblyCellIDs []string) error {
	shared, err := LoadSharedDepsFromEnv(ctx)
	if err != nil {
		return err
	}

	modules, err := corebundleModules(assemblyCellIDs)
	if err != nil {
		return err
	}
	cells, cellOpts, err := BuildApp(ctx, shared, modules...)
	if err != nil {
		return err
	}

	asm, err := buildAssembly(shared.PromStack, assemblyID, durabilityModeForTopology(shared.Topology), shared.Clock, cells...)
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

	opts := defaultRuntimeOptions(shared, asm, consumerBase, metricsHandler, adapterInfo)
	opts = append(opts, cellOpts...)

	return bootstrap.New(opts...).Run(ctx)
}

func corebundleModules(cellIDs []string) ([]CellModule, error) {
	modules := make([]CellModule, 0, len(cellIDs))
	for _, id := range cellIDs {
		switch id {
		case "configcore":
			modules = append(modules, ConfigCoreModule{})
		case "accesscore":
			modules = append(modules, AccessCoreModule{})
		case "auditcore":
			modules = append(modules, AuditCoreModule{})
		default:
			return nil, fmt.Errorf("corebundle: unsupported assembly cell %q", id)
		}
	}
	return modules, nil
}

// logSinglePodNonceStoreAcknowledgement emits a positive-path Info log when
// the deployment is real-mode + single-pod + InMemory NonceStore, making the
// operator's explicit single-pod replay-protection choice visible at startup.
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
