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

	// RequireRestrictedRole opts each serving pool into the
	// postgres_app_role_restricted_ready precondition probe (#1676 [F-B11]).
	// Applied to every resolved per-instance Config the composition root opens.
	RequireRestrictedRole bool
}

// Resolution is the per-instance pool plan the composition root opens N pools
// from (#2341). Each distinct DSN (after trim + dedup) is one pool instance keyed
// by a bootstrap.InfraInstanceKey:
//
//   - colocated (exactly 1 distinct DSN): a single instance keyed
//     bootstrap.DefaultInstanceKey(), with every cell mapped to it. The default
//     key preserves the bare relay probe names (outbox_relay_poll, …) — zero
//     observability regression for the only shape that runs today.
//   - split (>1 distinct DSN): one instance per DSN group, keyed
//     bootstrap.NewInfraInstanceKey(rep) where rep is the alphabetically-first
//     cell sharing that DSN. Each cell maps to its group's key. The composition
//     root opens one pool + drives one relay per instance (each relay's per-instance
//     health probe is namespaced by the key id).
type Resolution struct {
	// Instances maps each pool identity to the Config the composition root opens
	// that pool from (DSN + pool knobs, RequireRestrictedRole applied).
	Instances map[bootstrap.InfraInstanceKey]adapterpg.Config
	// CellToInstance maps each postgres cell to the pool instance it uses, so the
	// composition root can build the capability.PGSet (cell → provider).
	CellToInstance map[string]bootstrap.InfraInstanceKey
}

// Resolve is the PURE per-cell DSN gate + dedup + per-instance keying. It decides
// which pools the assembly opens; it performs NO I/O.
//
// The composition root (cmd/corebundle/cap_wiring.go) owns pool construction,
// schema verification, capability.PGProvider/PGSet wrapping, and the per-pool relay
// — deliberately, so the adapter constructors (NewPool / NewTxManager /
// NewJournalingOutboxWriter) and the relay (NewOutboxStore / NewRelay) stay in the
// single sanctioned cmd site (CAPABILITY-PROVIDER-FUNNEL-01,
// PROJECTION-EVENT-JOURNAL-TOPIC-ALLOWLIST-DERIVED-01,
// RELAY-CONSTRUCTION-CELLMODULE-BAN-01). This resolver only supplies the keyed
// per-instance Configs + the two retained fail-closed gates.
//
// Returns:
//   - (zero, false, nil) for non-postgres (memory) topology — no pool needed.
//   - (Resolution, true, nil) for postgres topology with ≥1 distinct DSN.
//   - fail-closed error for: empty Cells, or any empty per-cell DSN.
//
// Unlike before #2341 it NO LONGER fail-closes on >1 distinct DSN: distinct
// per-cell DSNs now fan out to N keyed pools (each driven by its own relay), which
// the keyed bootstrap relay seam (#2152 PR-1) consumes. The AMQP broker side stays
// single (egress-only, #2366) — distinct DB pools all relay to the one shared
// broker; the two >1-distinct gates are intentionally NOT lifted in lockstep.
func Resolve(topo bootstrap.Topology, cfg Config) (Resolution, bool, error) {
	if topo.StorageBackend() != bootstrap.StorageBackendPostgres {
		return Resolution{}, false, nil
	}

	// Sort cell IDs for deterministic error messages + stable per-group rep picks.
	cellIDs := make([]string, 0, len(cfg.Cells))
	for id := range cfg.Cells {
		cellIDs = append(cellIDs, id)
	}
	sort.Strings(cellIDs)

	// Empty-cell-set gate: postgres topology with no postgres cells is a
	// fail-closed misconfiguration, not a silent no-op.
	if len(cellIDs) == 0 {
		return Resolution{}, false, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"percellpg: postgres topology requires at least one postgres-requiring cell "+
				"with a database URL; refusing to start with an empty cell set")
	}

	// Group cells by trimmed DSN (fail-closed on empty DSN). dsnGroups preserves
	// the sorted cell order within each group, so groups[0] is the alphabetically
	// -first cell (the rep). dsnConfig keeps each DSN's pool knobs (first cell wins;
	// cells sharing a DSN must configure identical knobs since they share the pool).
	dsnGroups := make(map[string][]string)
	dsnConfig := make(map[string]adapterpg.Config)
	var dsnOrder []string
	for _, id := range cellIDs {
		cellCfg := cfg.Cells[id]
		trimmed := strings.TrimSpace(cellCfg.DSN)
		if trimmed == "" {
			envVar := "GOCELL_" + strings.ToUpper(id) + "_DATABASE_URL"
			return Resolution{}, false, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
				"percellpg: postgres-requiring cell is missing its per-cell database URL; "+
					"refusing to silently share another cell's pool",
				errcode.WithInternal(errcode.InternalAttr("cell_id", id)),
				errcode.WithInternal(errcode.InternalAttr("env_var", envVar)),
			)
		}
		if _, seen := dsnConfig[trimmed]; !seen {
			dsnConfig[trimmed] = cellCfg
			dsnOrder = append(dsnOrder, trimmed)
		}
		dsnGroups[trimmed] = append(dsnGroups[trimmed], id)
	}

	res := Resolution{
		Instances:      make(map[bootstrap.InfraInstanceKey]adapterpg.Config, len(dsnOrder)),
		CellToInstance: make(map[string]bootstrap.InfraInstanceKey, len(cellIDs)),
	}

	colocated := len(dsnOrder) == 1
	for _, dsn := range dsnOrder {
		group := dsnGroups[dsn]
		// Colocated keeps the default (zero) key so relay probe names stay bare;
		// split keys each pool by its rep cell so per-instance probes namespace cleanly.
		var key bootstrap.InfraInstanceKey
		if colocated {
			key = bootstrap.DefaultInstanceKey()
		} else {
			key = bootstrap.NewInfraInstanceKey(group[0])
		}
		instCfg := dsnConfig[dsn]
		instCfg.RequireRestrictedRole = cfg.RequireRestrictedRole
		res.Instances[key] = instCfg
		for _, id := range group {
			res.CellToInstance[id] = key
		}
	}

	return res, true, nil
}
