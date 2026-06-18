package percellpg

import (
	"fmt"
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

	// Group cells by trimmed DSN, fail-closing on empty DSN and knob mismatches.
	dsnGroups, dsnConfig, dsnOrder, err := groupCellsByDSN(cellIDs, cfg.Cells)
	if err != nil {
		return Resolution{}, false, err
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

// groupCellsByDSN groups sorted cell IDs by their trimmed DSN. It is extracted
// from Resolve to keep that function's cognitive complexity within the ≤15 limit.
//
// Returns:
//   - dsnGroups: DSN → sorted cell IDs.
//   - dsnConfig: DSN → the Config of the alphabetically-first cell in the group
//     (all pool knobs are identical after the knobs consistency check).
//   - dsnOrder: DSNs in first-seen order (stable because cellIDs is pre-sorted).
//   - error: fail-closed on empty DSN or pool-knob mismatch within a DSN group.
func groupCellsByDSN(cellIDs []string, cells map[string]adapterpg.Config) (
	dsnGroups map[string][]string,
	dsnConfig map[string]adapterpg.Config,
	dsnOrder []string,
	err error,
) {
	dsnGroups = make(map[string][]string)
	dsnConfig = make(map[string]adapterpg.Config)

	for _, id := range cellIDs {
		cellCfg := cells[id]
		trimmed := strings.TrimSpace(cellCfg.DSN)
		if trimmed == "" {
			envVar := "GOCELL_" + strings.ToUpper(id) + "_DATABASE_URL"
			return nil, nil, nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
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

	// Knobs consistency gate: within each DSN group every cell must declare the
	// same pool knobs (MaxConns, IdleTimeout, MaxLifetime, ConnectTimeout). Sharing
	// a DSN means sharing a pool — mismatched knobs are a misconfiguration; the
	// previous "first cell wins" behavior was a silent data loss.
	for _, dsn := range dsnOrder {
		if kerr := checkGroupKnobs(dsn, dsnGroups[dsn], cells); kerr != nil {
			return nil, nil, nil, kerr
		}
	}

	return dsnGroups, dsnConfig, dsnOrder, nil
}

// poolKnobs holds only the connection-pool tuning fields of an adapterpg.Config
// that all cells sharing the same DSN must agree on.
type poolKnobs struct {
	MaxConns       int32
	IdleTimeout    interface{}
	MaxLifetime    interface{}
	ConnectTimeout interface{}
}

// checkGroupKnobs verifies that all cells in a DSN group declare identical pool
// knobs (MaxConns / IdleTimeout / MaxLifetime / ConnectTimeout). DSN and
// RequireRestrictedRole are intentionally excluded: RequireRestrictedRole is
// applied uniformly from cfg.RequireRestrictedRole; DSN equality is what defines
// the group in the first place.
//
// Returns a fail-closed errcode error naming the conflicting cell IDs and the
// first differing knob when any mismatch is detected. The error carries
// WithInternal attrs (cell_ids, knob) so the detail stays server-side only.
func checkGroupKnobs(dsn string, group []string, cells map[string]adapterpg.Config) error {
	if len(group) <= 1 {
		return nil
	}
	ref := cells[group[0]]
	refKnobs := poolKnobs{
		MaxConns:       ref.MaxConns,
		IdleTimeout:    ref.IdleTimeout,
		MaxLifetime:    ref.MaxLifetime,
		ConnectTimeout: ref.ConnectTimeout,
	}
	for _, id := range group[1:] {
		c := cells[id]
		cKnobs := poolKnobs{
			MaxConns:       c.MaxConns,
			IdleTimeout:    c.IdleTimeout,
			MaxLifetime:    c.MaxLifetime,
			ConnectTimeout: c.ConnectTimeout,
		}
		if knob := firstDifferingKnob(refKnobs, cKnobs); knob != "" {
			cellIDs := strings.Join(group, ", ")
			return errcode.New(
				errcode.KindInvalid,
				errcode.ErrValidationFailed,
				"percellpg: cells sharing the same DSN must declare identical pool knobs; "+
					"refusing to start with ambiguous pool configuration",
				errcode.WithInternal(errcode.InternalAttr("cell_ids", cellIDs)),
				errcode.WithInternal(errcode.InternalAttr("knob", knob)),
				errcode.WithInternal(errcode.InternalAttr("dsn_prefix", dsnPrefix(dsn))),
			)
		}
	}
	return nil
}

// firstDifferingKnob returns the name of the first knob that differs between a
// and b, or "" if all knobs are identical. The check order is deterministic so
// error messages are stable across runs.
func firstDifferingKnob(a, b poolKnobs) string {
	if a.MaxConns != b.MaxConns {
		return "MaxConns"
	}
	if a.IdleTimeout != b.IdleTimeout {
		return "IdleTimeout"
	}
	if a.MaxLifetime != b.MaxLifetime {
		return "MaxLifetime"
	}
	if a.ConnectTimeout != b.ConnectTimeout {
		return "ConnectTimeout"
	}
	return ""
}

// dsnPrefix returns a non-sensitive prefix of the DSN for diagnostic context,
// truncated after the host to avoid leaking credentials or database names.
func dsnPrefix(dsn string) string {
	const maxLen = 40
	// Strip userinfo (credentials) from the prefix: find "://" and skip to host.
	if i := strings.Index(dsn, "://"); i >= 0 {
		rest := dsn[i+3:]
		// Skip userinfo@: everything up to the last '@' before the first '/'.
		if at := strings.LastIndex(strings.SplitN(rest, "/", 2)[0], "@"); at >= 0 {
			rest = rest[at+1:]
		}
		candidate := fmt.Sprintf("%s://%s", dsn[:i], rest)
		if len(candidate) > maxLen {
			return candidate[:maxLen] + "…"
		}
		return candidate
	}
	if len(dsn) > maxLen {
		return dsn[:maxLen] + "…"
	}
	return dsn
}
