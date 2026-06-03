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
	"strings"

	kauth "github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cellvocab"

	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/composition"
	"github.com/ghbvf/gocell/runtime/lifecycle"
)

// runCorebundle is the handwritten runtime half behind the generated
// assembly entrypoint. The generated main.go owns the assembly ID and cell
// order; this function owns environment loading and runtime option wiring.
func runCorebundle(ctx context.Context, assemblyID string, assemblyCellIDs []string) error {
	// The redacting slog default is sealed by the generated main.go's run()
	// (SLOG-HANDLER-SEALED-FUNNEL-01 A3 generated segment) before this function
	// is invoked, so every slog.Default() call here is already scrubbed.
	compShared, locals, err := LoadSharedDepsFromEnv(ctx)
	if err != nil {
		return err
	}

	// Close shared infrastructure (postgres pool + redis client) if startup aborts
	// before bootstrap.Run takes ownership of the ManagedResources (provisionCapabilities /
	// composition.Builder.Build / buildAssembly / option wiring failures). The redis
	// client is created inside LoadSharedDepsFromEnv (which already closes it on its own
	// internal failure); registering this defer immediately after Load covers the whole
	// window from here to handedToBootstrap — including provisionCapabilities. The closure
	// reads handedToBootstrap lazily at exit.
	// Once bootstrap.Run is reached both resources are managed by bootstrap's LIFO teardown.
	handedToBootstrap := false
	defer func() { releaseUnhandedResources(ctx, locals, handedToBootstrap) }()

	// Provision assembly-level shared infrastructure (postgres pool / redis
	// client → cap.*Provider) once, before any module consumes it. Mirrors
	// fx.New() resolve-before-start ordering.
	if err := provisionCapabilities(ctx, compShared, locals); err != nil {
		return err
	}

	mods := generatedCellModules()

	adapterInfo := adapterInfoForSharedDeps(compShared, locals)
	slog.Info("corebundle: startup configuration",
		slog.String("adapter_mode", adapterInfo["mode"]),
		slog.String("storage", adapterInfo["storage"]),
		slog.String("event_bus", adapterInfo["event_bus"]),
		slog.String("outbox_storage", adapterInfo["outbox_storage"]),
		slog.String("redis", adapterInfo["redis"]),
		slog.String("service_token_nonce_store", adapterInfo["service_token_nonce_store"]),
		slog.String("outbox_consumer_claimer", adapterInfo["outbox_consumer_claimer"]))

	logSinglePodNonceStoreAcknowledgement(compShared)

	// runtimeOptsFunc stays in cmd: auth construction is AUTH-PLAN-04-allowlisted
	// to cmd/.
	runtimeOptsFunc := func(cells []cell.Cell) ([]bootstrap.Option, error) {
		logAssemblyMaturity(cells)

		asm, buildErr := buildAssembly(locals, assemblyID, durabilityModeForTopology(compShared.Topology), compShared.Clock, cells...)
		if buildErr != nil {
			return nil, fmt.Errorf("build assembly: %w", buildErr)
		}

		consumerBase, cbErr := buildConsumerBase(compShared)
		if cbErr != nil {
			return nil, cbErr
		}

		opts, rtErr := defaultRuntimeOptions(compShared, locals, asm, consumerBase, locals.metricsHandler, adapterInfo)
		if rtErr != nil {
			return nil, fmt.Errorf("default runtime options: %w", rtErr)
		}
		return opts, nil
	}

	// composition.New(assemblyCellIDs...) seals the assembly's cell-id closed set
	// (M12a #1093): Build fail-fasts if generatedCellModules drifts from the
	// assembly.yaml cell list (replaces the former hand-written assertModuleIDsMatch).
	app, err := composition.New(assemblyCellIDs...).With(mods...).Build(ctx, compShared, runtimeOptsFunc)
	if err != nil {
		return err
	}

	handedToBootstrap = true
	return app.Run(ctx)
}

// releaseUnhandedResources closes the assembly's shared infrastructure (postgres
// pool + redis client) that startup opened but bootstrap.Run never took ownership
// of. It is the body of runCorebundle's startup-abort defer. When
// handedToBootstrap is true, bootstrap owns the resources via its LIFO teardown,
// so this is a no-op. Both fields are nil-safe: poolMR is nil until
// provisionPostgres sets it, and closeRedisClientAfterFailedLoad tolerates a nil
// redis client.
func releaseUnhandedResources(ctx context.Context, locals *cmdLocals, handedToBootstrap bool) {
	if handedToBootstrap {
		return
	}
	if locals.poolMR != nil {
		_ = locals.poolMR.Close(ctx)
	}
	closeRedisClientAfterFailedLoad(ctx, locals.redisClient)
}

// logAssemblyMaturity emits a startup Info log of the running assembly's
// maturity-lifecycle distribution as a structured group (e.g.
// lifecycle.experimental=1 lifecycle.asset=2), with per-phase cell ID lists.
// This is the consumer of runtime/lifecycle.LifecycleAggregator: it makes the
// maturity composition of a deployment visible at boot, so operators notice if
// (say) a production bundle is unexpectedly running experimental cells. The
// aggregator exposes raw per-cell lifecycles; the distribution and cell IDs are
// computed here, by the consumer.
//
// The lifecycle group is built by ranging over cellvocab.AllCellLifecycles()
// so any future phase addition is automatically included without editing this
// function.
func logAssemblyMaturity(cells []cell.Cell) {
	if len(cells) == 0 {
		return
	}
	ids := make([]cell.CellIdentity, len(cells))
	for i, c := range cells {
		ids[i] = c
	}

	// Collect cell IDs per lifecycle phase.
	byPhase := make(map[cellvocab.CellLifecycle][]string)
	for _, e := range lifecycle.NewLifecycleAggregator(ids).Snapshot() {
		byPhase[e.Lifecycle] = append(byPhase[e.Lifecycle], e.CellID)
	}

	// Build lifecycle group attrs ordered by AllCellLifecycles (ascending rank).
	// Each phase contributes a count attr and a cell-IDs attr so operators can
	// identify which cells are at each maturity level.
	lcAttrs := make([]any, 0, len(cellvocab.AllCellLifecycles())*2)
	for _, phase := range cellvocab.AllCellLifecycles() {
		cellIDs := byPhase[phase]
		lcAttrs = append(lcAttrs, slog.Int(string(phase), len(cellIDs)))
		if len(cellIDs) > 0 {
			lcAttrs = append(lcAttrs, slog.String(string(phase)+"_cells", strings.Join(cellIDs, ",")))
		}
	}

	slog.Info("corebundle: assembly maturity composition",
		slog.Int("total_cells", len(cells)),
		slog.Group("lifecycle", lcAttrs...),
	)
}

// logSinglePodNonceStoreAcknowledgement emits a positive-path Info log when
// the deployment is real-mode + single-pod + InMemory NonceStore, making the
// operator's explicit single-pod replay-protection choice visible at startup.
func logSinglePodNonceStoreAcknowledgement(shared *composition.SharedDeps) {
	if shared == nil || shared.NonceStore == nil ||
		shared.NonceStore.Kind() != kauth.NonceStoreKindInMemory {
		return
	}
	if !shared.Topology.RequireProductionControlPlane() ||
		!shared.Topology.SinglePodReplayProtection() {
		return
	}
	slog.Info("controlplane: in-memory nonce store acknowledged for single-pod deployment",
		slog.String("nonce_store_kind", string(shared.NonceStore.Kind())),
		slog.String("note", "GOCELL_SINGLE_POD=1 set; multi-pod deployments must inject a distributed NonceStore via WithServiceTokenNonceStore"))
}
