package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cellmodules/percellpg"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/capability"
	"github.com/ghbvf/gocell/framework/runtime/composition"
)

// provisionCapabilities is the assembly's single shared-infrastructure
// provisioning site. It iterates the codegen-declared generatedCapabilities()
// (single source: the derived union of the assembly cells' cell.yaml `requires`,
// computed in kernel/assembly.GenerateModulesGen), and for each declared
// capability provisions the shared resource exactly once and wraps it into the
// sealed runtime/capability provider stored on shared. Consuming cell modules receive
// the injected provider (shared.PG / shared.Redis) and never construct adapter
// primitives themselves.
//
// Postgres provisioning now delegates to cellmodules/percellpg.Resolve, which
// handles per-cell DSN resolution, dedup-by-DSN, fail-closed gates, pool
// construction, schema verification, and provider wrapping. This file remains the
// sole sanctioned caller entry point in cmd/ (CAPABILITY-PROVIDER-FUNNEL-01
// downstream allowlist); the actual adapter constructors moved to percellpg.
//
// Called from runCorebundle after LoadSharedDepsFromEnv and before composition.Builder.Build
// so the providers are present (or fail-fast) before any module.Provide runs —
// mirroring fx.New()'s resolve-before-start ordering (ref: uber-go/fx app.go).
func provisionCapabilities(ctx context.Context, shared *composition.SharedDeps, locals *cmdLocals) error {
	// INVARIANT: generatedCapabilities() exists because the corebundle assembly's
	// cells declare a non-empty `requires` union (auditcore/configcore require
	// postgres). The codegen template emits generatedCapabilities() iff that union
	// is non-empty — so this unconditional call is only safe while corebundle keeps
	// provisioning ≥1 capability. A hypothetical zero-capability corebundle would
	// not need a provisioner at all; this function would then be removed alongside.
	for _, c := range generatedCapabilities() {
		switch c {
		case capability.Postgres:
			if err := provisionPostgres(ctx, shared, locals); err != nil {
				return err
			}
		case capability.Redis:
			provisionRedis(shared, locals)
		default:
			// Reached when a cell requires a capability that is a recognized enum
			// member but has no provisioning path here — today only rabbitmq (it is
			// in CapabilityEnum + capabilityConstNames, so it passes FMT-36 + codegen,
			// but provisionCapabilities has no rabbitmq case). By-design fail-fast:
			// a real provider must land its provisioning atomically. Truly unknown
			// values are already rejected upstream by FMT-36 + the codegen guard.
			// See runtime/capability.RabbitMQ.
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"corebundle: declared capability has no provisioning path",
				errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("capability=%q", string(c)))))
		}
	}
	return nil
}

// provisionPostgres delegates postgres pool provisioning to cellmodules/percellpg.Resolve.
// It builds a percellpg.Config by iterating generatedPostgresCells() (single source:
// cell IDs whose cell.yaml requires postgres, derived by codegen) and loading each
// cell's per-cell DSN from the environment via LoadPGConfig. The resolver handles
// dedup-by-DSN, fail-closed gates (missing DSN, distinct DSNs), pool construction,
// schema/shape/index verification, and capability.PGProvider wrapping.
//
// In memory topology the resolver returns empty Deps and shared.PG stays nil;
// cell modules fall through to their in-memory storage path.
//
// The decorator's topic argument MUST stay the direct generatedProjectionSourceTopics()
// call (PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01 rejects a threaded
// variable, which could hide a hand-typed list).
func provisionPostgres(ctx context.Context, shared *composition.SharedDeps, locals *cmdLocals) error {
	cells := make(map[string]adapterpg.Config, len(generatedPostgresCells()))
	for _, cellID := range generatedPostgresCells() {
		pgCfg, err := LoadPGConfig(strings.ToUpper(cellID))
		if err != nil {
			return fmt.Errorf("percellpg: load config for cell %s: %w", cellID, err)
		}
		cells[cellID] = pgCfg
	}

	// Surface the wired topic-set size so operators can distinguish "journal inactive
	// because no projections are declared" (count 0, expected today) from a wiring
	// regression — the durable journal otherwise gives no startup signal (#1504 PR-02).
	slog.InfoContext(ctx, "corebundle: journaling outbox writer wired",
		slog.Int("projection_source_topic_count", len(generatedProjectionSourceTopics())))

	deps, err := percellpg.Resolve(ctx, shared.Clock, shared.Topology, percellpg.Config{
		Cells:                  cells,
		ProjectionSourceTopics: generatedProjectionSourceTopics(),
		RequireRestrictedRole:  true,
	})
	if err != nil {
		return err
	}

	if deps.Provider != nil {
		shared.PG = deps.Provider
	}
	if len(deps.Resources) > 0 {
		locals.poolMR = deps.Resources[0]
	}
	return nil
}

// provisionRedis wraps the shared redis client (constructed in
// LoadSharedDepsFromEnv via buildSharedReplayDeps and held on locals.redisClient)
// into the sealed capability.RedisProvider. Nil (no-op) in modes without redis.
func provisionRedis(shared *composition.SharedDeps, locals *cmdLocals) {
	if locals.redisClient != nil {
		shared.Redis = capability.NewRedisProvider(locals.redisClient)
	}
}
