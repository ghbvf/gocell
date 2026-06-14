package percellpg

import (
	"context"
	"fmt"
	"sort"
	"strings"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/lifecycle"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/migration"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/capability"
)

// Config carries the per-cell DSN resolution and pool options. The composition
// root reads per-cell environment variables (GOCELL_<CELLID>_DATABASE_URL) and
// passes the resolved configs here, keeping this package pure with respect to
// os.Getenv and exhaustively unit-testable.
type Config struct {
	// Cells maps each postgres-requiring cell ID to its resolved PG Config
	// (DSN + pool knobs). An empty DSN for any cell triggers a fail-closed
	// startup error — sharing another cell's pool without operator intent would
	// bypass per-cell credential isolation.
	Cells map[string]adapterpg.Config

	// ProjectionSourceTopics is the sorted set of outbox-projection contract IDs
	// threaded into NewJournalingOutboxWriter (preserves EPIC #1504 D4); pass
	// generatedProjectionSourceTopics() from the composition root.
	ProjectionSourceTopics []string

	// RequireRestrictedRole opts the serving pool into the
	// postgres_app_role_restricted_ready precondition probe (#1676 [F-B11]):
	// a superuser-served deployment reports /readyz 503 instead of silently
	// leaking across tenants. The composition root passes true for the serving
	// pool; false for tool pools (migrations).
	RequireRestrictedRole bool
}

// Deps is the resolved per-cell PG dependency set. For memory topology, Provider
// is nil and Resources is empty. For postgres topology with all cells sharing the
// same DSN, Provider holds the single deduped capability.PGProvider and Resources
// holds the pool as a lifecycle.ManagedResource for LIFO close.
type Deps struct {
	Provider  capability.PGProvider
	Resources []lifecycle.ManagedResource
}

// newPool is the package-level pool factory seam. Tests can override it to inject
// a fake without a live database (mirrors replaydeps.go's newRedisClient factory).
// Production always uses adapterpg.NewPool.
var newPool = adapterpg.NewPool

// Resolve selects the postgres backend for topo. clk is the mandatory positional
// clock (ADR clock-positional-injection-funnel), threaded into the outbox writer.
//
// Memory topology returns empty Deps (no pool). Postgres topology validates that
// all postgres cells have a non-empty DSN and that all DSNs deduplicate to
// exactly one (colocated invariant), then opens ONE shared pool.
//
// See the package doc for the selection matrix and the two fail-closed invariants
// (missing-DSN gate, distinct-DSN gate).
func Resolve(ctx context.Context, clk clock.Clock, topo bootstrap.Topology, cfg Config) (Deps, error) {
	clock.MustHaveClock(clk, "percellpg.Resolve")

	agreed, ok, err := resolveAgreedConfig(topo, cfg)
	if err != nil {
		return Deps{}, err
	}
	if !ok {
		// Memory topology: no pool needed.
		return Deps{}, nil
	}

	pool, err := newPool(ctx, agreed)
	if err != nil {
		return Deps{}, fmt.Errorf("percellpg: open postgres pool: %w", err)
	}

	if vErr := verifyPGPreconditions(ctx, pool); vErr != nil {
		_ = pool.Close(ctx) // no leak on verify error, mirrors cap_wiring.go
		return Deps{}, vErr
	}

	writer := adapterpg.NewJournalingOutboxWriter(adapterpg.NewOutboxWriter(clk), cfg.ProjectionSourceTopics)
	provider := capability.NewPGProvider(adapterpg.NewTxManager(pool), writer, pool.DB())

	return Deps{
		Provider:  provider,
		Resources: []lifecycle.ManagedResource{pool},
	}, nil
}

// resolveAgreedConfig is the PURE topology gate and DSN deduplicator. It:
//   - returns (zero, false, nil) for non-postgres topology (memory mode).
//   - iterates cfg.Cells in sorted cellID order; fails-closed on any empty DSN.
//   - deduplicates DSNs; fails-closed if >1 distinct DSN is found.
//   - returns (agreedConfig, true, nil) when exactly one distinct DSN exists.
//
// Pure: no I/O, no os.Getenv — exhaustively testable.
func resolveAgreedConfig(topo bootstrap.Topology, cfg Config) (adapterpg.Config, bool, error) {
	if topo.StorageBackend() != bootstrap.StorageBackendPostgres {
		return adapterpg.Config{}, false, nil
	}

	// Sort cell IDs for deterministic error messages.
	cellIDs := make([]string, 0, len(cfg.Cells))
	for id := range cfg.Cells {
		cellIDs = append(cellIDs, id)
	}
	sort.Strings(cellIDs)

	// Dedup DSNs; fail-closed on empty DSN.
	seen := make(map[string]adapterpg.Config, len(cellIDs))
	for _, id := range cellIDs {
		cellCfg := cfg.Cells[id]
		trimmed := strings.TrimSpace(cellCfg.DSN)
		if trimmed == "" {
			envVar := "GOCELL_" + strings.ToUpper(id) + "_DATABASE_URL"
			return adapterpg.Config{}, false, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"percellpg: postgres-requiring cell is missing its per-cell database URL; "+
					"refusing to silently share another cell's pool",
				errcode.WithInternal(errcode.InternalAttr("cell_id", id)),
				errcode.WithInternal(errcode.InternalAttr("env_var", envVar)),
			)
		}
		seen[trimmed] = cellCfg
	}

	if len(seen) > 1 {
		return adapterpg.Config{}, false, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"percellpg: per-cell distinct database credentials require split topology "+
				"with per-cell outbox relay fan-out (US4 #1963 / #2152); colocated assemblies must configure an identical "+
				"GOCELL_<CELLID>_DATABASE_URL for every postgres cell",
			errcode.WithInternal(errcode.InternalAttr("distinct_dsn_count", len(seen))),
			errcode.WithInternal(errcode.InternalAttr("cell_ids", strings.Join(cellIDs, ","))),
		)
	}

	// Exactly one distinct DSN: use the config of the first sorted cell (includes
	// pool knobs like MaxConns / IdleTimeout / MaxLifetime set by that cell's env).
	// Apply the RequireRestrictedRole override from the caller.
	agreed := cfg.Cells[cellIDs[0]]
	agreed.RequireRestrictedRole = cfg.RequireRestrictedRole
	return agreed, true, nil
}

// verifyPGPreconditions runs the three assembly-wide PG fail-fast checks.
// Moved from cmd/corebundle/cap_wiring.go into this package so the resolver owns
// the full pool-open → verify → wrap sequence (mirrors auditcore's admin pool
// pattern). Caller owns pool lifecycle; on error the caller must close the pool.
func verifyPGPreconditions(ctx context.Context, pool *adapterpg.Pool) error {
	if err := verifyPGSchema(ctx, pool); err != nil {
		return err
	}
	// S3+S5: column-existence fail-fast catches partial migrations.
	if err := adapterpg.VerifyExpectedShape(ctx, pool); err != nil {
		return fmt.Errorf("percellpg: PG schema shape: %w", err)
	}
	// B2-X-03: operators must DROP INVALID indexes manually before start.
	if err := adapterpg.VerifyNoInvalidIndexes(ctx, pool); err != nil {
		return fmt.Errorf("percellpg: PG invalid indexes: %w", err)
	}
	return nil
}

func verifyPGSchema(ctx context.Context, pool *adapterpg.Pool) error {
	migrationsFS, err := adapterpg.MigrationsFS()
	if err != nil {
		return fmt.Errorf("percellpg: PG migrations fs: %w", err)
	}
	if err := adapterpg.VerifyExpectedVersion(ctx, pool, migrationsFS, migration.PlatformNamespace); err != nil {
		return fmt.Errorf("percellpg: PG schema guard: %w", err)
	}
	return nil
}
