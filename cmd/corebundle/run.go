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
	shared, err := LoadSharedDepsFromEnv(ctx)
	if err != nil {
		return err
	}
	// Build cmd-private locals from the populated SharedDeps.
	locals := buildCmdLocals(shared)

	// Provision assembly-level shared infrastructure (postgres pool / redis
	// client → cap.*Provider) once, before any module consumes it. Mirrors
	// fx.New() resolve-before-start ordering.
	if err := provisionCapabilities(ctx, shared, locals); err != nil {
		return err
	}
	// Close the pool if startup aborts before bootstrap.Run takes ownership of
	// the ManagedResource (composition.Builder.Build / buildAssembly / option wiring
	// failures). Once bootstrap.Run is reached the pool is managed by bootstrap's
	// LIFO teardown.
	handedToBootstrap := false
	defer func() {
		if !handedToBootstrap && locals.poolMR != nil {
			_ = locals.poolMR.Close(ctx)
		}
	}()

	// Build the composition.SharedDeps view (public, interface-only fields) from
	// the cmd-private SharedDeps.
	compShared := toCompositionSharedDeps(shared)
	compShared.AssemblyID = assemblyID

	mods, err := corebundleModules(assemblyID, assemblyCellIDs, locals)
	if err != nil {
		return err
	}

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

	// runtimeOptsFunc stays in cmd: auth construction is AUTH-PLAN-04-allowlisted
	// to cmd/.
	runtimeOptsFunc := func(cells []cell.Cell) ([]bootstrap.Option, error) {
		logAssemblyMaturity(cells)

		asm, buildErr := buildAssembly(locals, assemblyID, durabilityModeForTopology(shared.Topology), shared.Clock, cells...)
		if buildErr != nil {
			return nil, fmt.Errorf("build assembly: %w", buildErr)
		}

		consumerBase, cbErr := buildConsumerBase(shared)
		if cbErr != nil {
			return nil, cbErr
		}

		opts, rtErr := defaultRuntimeOptions(shared, locals, asm, consumerBase, locals.metricsHandler, adapterInfo)
		if rtErr != nil {
			return nil, fmt.Errorf("default runtime options: %w", rtErr)
		}
		return opts, nil
	}

	app, err := composition.New().With(mods...).Build(ctx, compShared, runtimeOptsFunc)
	if err != nil {
		return err
	}

	handedToBootstrap = true
	return app.Run(ctx)
}

func corebundleModules(assemblyID string, cellIDs []string, locals *cmdLocals) ([]composition.CellModule, error) {
	mods := generatedCellModules(locals)
	if err := assertModuleIDsMatch(assemblyID, cellIDs, mods); err != nil {
		return nil, err
	}
	return mods, nil
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

// assertModuleIDsMatch fails-fast when assembly.yaml.cells (cellIDs) drifts from
// the generated module list. The two should be 1:1 in declaration order; any
// mismatch indicates a missing `gocell generate assembly` run.
func assertModuleIDsMatch(assemblyID string, cellIDs []string, mods []composition.CellModule) error {
	hint := fmt.Sprintf("run `gocell generate assembly --id=%s`", assemblyID)
	if len(cellIDs) != len(mods) {
		return fmt.Errorf(
			"%s: assembly.yaml cells (%d) ↔ modules_gen.go (%d) length mismatch; %s",
			assemblyID, len(cellIDs), len(mods), hint)
	}
	for i, want := range cellIDs {
		if got := mods[i].ID(); got != want {
			return fmt.Errorf(
				"%s: assembly.yaml cells[%d]=%q ↔ modules_gen.go=%q drift; %s",
				assemblyID, i, want, got, hint)
		}
	}
	return nil
}

// logSinglePodNonceStoreAcknowledgement emits a positive-path Info log when
// the deployment is real-mode + single-pod + InMemory NonceStore, making the
// operator's explicit single-pod replay-protection choice visible at startup.
func logSinglePodNonceStoreAcknowledgement(shared *SharedDeps) {
	if shared == nil || shared.InternalGuard == nil {
		return
	}
	ns := shared.InternalGuard.NonceStore()
	if ns == nil || ns.Kind() != kauth.NonceStoreKindInMemory {
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
