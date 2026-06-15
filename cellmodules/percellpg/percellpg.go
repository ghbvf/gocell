package percellpg

import (
	"sort"
	"strings"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
)

// Config carries the per-cell DSN resolution inputs. The composition root reads
// each postgres cell's GOCELL_<CELLID>_DATABASE_URL (+ pool knobs) and passes the
// resolved configs here, keeping this package pure with respect to os.Getenv and
// exhaustively unit-testable.
type Config struct {
	// Cells maps each postgres-requiring cell ID to its resolved PG Config
	// (DSN + pool knobs). An empty DSN for any cell — or an empty Cells map in
	// postgres topology — triggers a fail-closed startup error.
	Cells map[string]adapterpg.Config

	// RequireRestrictedRole opts the serving pool into the
	// postgres_app_role_restricted_ready precondition probe (#1676 [F-B11]).
	// Applied to the agreed Config the composition root opens the pool from.
	RequireRestrictedRole bool
}

// Resolve is the PURE per-cell DSN gate + dedup. It decides which single
// postgres Config the assembly pool is opened from; it performs NO I/O.
//
// The composition root (cmd/corebundle/cap_wiring.go) owns pool construction,
// schema verification, and capability.PGProvider wrapping — deliberately, so the
// adapter constructors (NewPool / NewTxManager / NewJournalingOutboxWriter) stay
// in the single sanctioned cmd site that CAPABILITY-PROVIDER-FUNNEL-01 and
// PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01 cover. This resolver only
// supplies the agreed DSN config + the three fail-closed gates.
//
// Returns:
//   - (zero, false, nil) for non-postgres (memory) topology — no pool needed.
//   - (agreedConfig, true, nil) for postgres topology with exactly one distinct DSN
//     (colocated). agreedConfig is the alphabetically-first cell's Config (DSN +
//     pool knobs) with RequireRestrictedRole applied from cfg.
//   - fail-closed error for: empty Cells, any empty per-cell DSN, or >1 distinct DSN.
func Resolve(topo bootstrap.Topology, cfg Config) (adapterpg.Config, bool, error) {
	if topo.StorageBackend() != bootstrap.StorageBackendPostgres {
		return adapterpg.Config{}, false, nil
	}

	// Sort cell IDs for deterministic error messages + a stable agreed-config pick.
	cellIDs := make([]string, 0, len(cfg.Cells))
	for id := range cfg.Cells {
		cellIDs = append(cellIDs, id)
	}
	sort.Strings(cellIDs)

	// Empty-cell-set gate: postgres topology with no postgres cells is a
	// fail-closed misconfiguration, not a silent no-op (also guards the
	// cellIDs[0] index below from a panic on an empty map).
	if len(cellIDs) == 0 {
		return adapterpg.Config{}, false, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"percellpg: postgres topology requires at least one postgres-requiring cell "+
				"with a database URL; refusing to start with an empty cell set")
	}

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
				"with per-cell outbox relay fan-out (US4 #1963 / #2152); colocated assemblies "+
				"must configure an identical GOCELL_<CELLID>_DATABASE_URL for every postgres cell",
			errcode.WithInternal(errcode.InternalAttr("distinct_dsn_count", len(seen))),
			errcode.WithInternal(errcode.InternalAttr("cell_ids", strings.Join(cellIDs, ","))),
		)
	}

	// Exactly one distinct DSN: use the alphabetically-first cell's full Config
	// (DSN + pool knobs). Colocated deployments must set identical pool knobs across
	// postgres cells since they configure the one shared pool. Apply the caller's
	// RequireRestrictedRole override.
	agreed := cfg.Cells[cellIDs[0]]
	agreed.RequireRestrictedRole = cfg.RequireRestrictedRole
	return agreed, true, nil
}
