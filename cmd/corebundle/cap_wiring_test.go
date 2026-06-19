package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cellmodules/percellpg"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/capability"
)

// These unit tests cover provisionPostgres's Docker-free branches — the topology
// gate, the per-cell LoadPGConfig loop + error, and the percellpg.Resolve fail-closed
// propagation. The postgres I/O path (NewPool + verifyPGPreconditions + provider
// wrapping) needs a live database and is covered by the real-PG integration tests
// (main_integration_test.go / corebundle_pg_env_integration_test.go).

// TestProvisionPostgres_MemoryTopology: the topology gate runs first, so memory
// mode returns early without reading any per-cell PG env (regression guard for the
// re-review F3 fix) and leaves shared.PG nil.
func TestProvisionPostgres_MemoryTopology(t *testing.T) {
	// A malformed knob must NOT block memory mode — the gate returns before LoadPGConfig.
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_MAX_CONNS", "not-a-number")
	shared, locals := newValidatedSharedDepsAndLocals(t, mkTopo("real", "memory", false))

	require.NoError(t, provisionPostgres(context.Background(), shared, locals),
		"memory topology must not read per-cell PG env")
	require.Nil(t, shared.PG, "memory topology: no pool provisioned")
}

// TestProvisionPostgres_MissingDSN_FailClosed: postgres topology with no per-cell
// DSN configured fails closed (before any pool open) via percellpg.Resolve.
func TestProvisionPostgres_MissingDSN_FailClosed(t *testing.T) {
	t.Setenv("GOCELL_ACCESSCORE_DATABASE_URL", "")
	t.Setenv("GOCELL_AUDITCORE_DATABASE_URL", "")
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_URL", "")
	shared, locals := newValidatedSharedDepsAndLocals(t, mkTopo("real", "postgres", false))

	err := provisionPostgres(context.Background(), shared, locals)
	require.Error(t, err, "postgres topology with no per-cell DSN must fail-closed")
	// errcode.Error() renders the WithInternal attrs (cell_id / env_var) when present.
	require.Contains(t, err.Error(), "GOCELL_ACCESSCORE_DATABASE_URL")
	require.Nil(t, shared.PG, "no provider on fail-closed")
}

// TestProvisionPostgres_BadPoolKnob: an invalid per-cell pool knob surfaces from
// LoadPGConfig before any pool open.
func TestProvisionPostgres_BadPoolKnob(t *testing.T) {
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_URL", "postgres://host/db")
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_MAX_CONNS", "not-a-number")
	shared, locals := newValidatedSharedDepsAndLocals(t, mkTopo("real", "postgres", false))

	err := provisionPostgres(context.Background(), shared, locals)
	require.Error(t, err, "an invalid per-cell pool knob must fail at config load")
	require.Contains(t, err.Error(), "load PG config for cell configcore")
}

// TestProvisionPostgres_PoolOpenError: a malformed (but non-empty) DSN passes the
// percellpg gates and fails fast at NewPool's DSN parse (no connection attempt),
// exercising the pool-open error branch without a live database.
func TestProvisionPostgres_PoolOpenError(t *testing.T) {
	const dsn = "postgres://host/db?sslmode=bogus" // rejected by ParseConfig, no dial
	t.Setenv("GOCELL_ACCESSCORE_DATABASE_URL", dsn)
	t.Setenv("GOCELL_AUDITCORE_DATABASE_URL", dsn)
	t.Setenv("GOCELL_CONFIGCORE_DATABASE_URL", dsn)
	shared, locals := newValidatedSharedDepsAndLocals(t, mkTopo("real", "postgres", false))

	err := provisionPostgres(context.Background(), shared, locals)
	require.Error(t, err, "a malformed DSN must fail-closed at pool open")
	require.Contains(t, err.Error(), "open PG pool for instance")
	require.Nil(t, shared.PG, "no provider on pool-open failure")
}

// TestOrderedPGInstances_SplitFanOut locks the #2341 split fan-out at the composition
// root: distinct DSNs produce N pool instances (each driving exactly one pool option +
// one relay option in provisionPGInstance's 1:1 loop), keyed and ordered deterministically
// by representative cell. The real pool-open/opt-append is Docker-gated; this pins the pure
// resolver→ordering seam that decides the fan-out cardinality + per-cell→instance routing.
func TestOrderedPGInstances_SplitFanOut(t *testing.T) {
	topo := mkTopo("real", "postgres", false)
	const dsnA = "postgres://hostA/db"
	const dsnB = "postgres://hostB/db"
	// accesscore + auditcore share DSN-A (one pool); configcore on DSN-B (its own pool).
	cfg := percellpg.Config{
		Cells: map[string]adapterpg.Config{
			"accesscore": {DSN: dsnA},
			"auditcore":  {DSN: dsnA},
			"configcore": {DSN: dsnB},
		},
		RequireRestrictedRole: true,
	}
	res, ok, err := percellpg.Resolve(topo, cfg)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, res.Instances, 2, "2 distinct DSN → 2 pool instances")

	insts := orderedPGInstances(res)
	require.Len(t, insts, 2, "one pool option + one relay option per distinct DSN (1:1)")

	// Deterministic order by representative cell (alphabetically-first served cell):
	// DSN-A group {accesscore, auditcore} → rep accesscore; DSN-B group {configcore} → rep configcore.
	assert.Equal(t, "accesscore", insts[0].repCell)
	assert.Equal(t, []string{"accesscore", "auditcore"}, insts[0].cells)
	assert.Equal(t, bootstrap.NewInfraInstanceKey("accesscore"), insts[0].key)
	assert.Equal(t, "configcore", insts[1].repCell)
	assert.Equal(t, []string{"configcore"}, insts[1].cells)
	assert.Equal(t, bootstrap.NewInfraInstanceKey("configcore"), insts[1].key)

	// Each cell routes to its group's instance key (the contract shared.PG.ForCell relies on).
	assert.Equal(t, insts[0].key, res.CellToInstance["accesscore"])
	assert.Equal(t, insts[0].key, res.CellToInstance["auditcore"])
	assert.Equal(t, insts[1].key, res.CellToInstance["configcore"])
	assert.NotEqual(t, insts[0].key, insts[1].key, "split instances have distinct keys")
}

// TestPGSet_ForCellRouting_Split verifies the PGSet routing layer (#2341): in a split
// shape each cell resolves to its own DSN group's provider, same-group cells share one
// provider, and an unprovisioned cell fails closed. This is the per-cell routing the
// cell modules depend on (accesscore/auditcore/configcore call shared.PG.ForCell).
func TestPGSet_ForCellRouting_Split(t *testing.T) {
	// pgProvider is a value struct {tx, writer, db}; give each a distinct db sentinel
	// so the two providers are non-equal and routing identity is observable via ==.
	dbA, dbB := new(int), new(int)
	provA := capability.NewPGProvider(projNoopTxRunner{}, nil, dbA)
	provB := capability.NewPGProvider(projNoopTxRunner{}, nil, dbB)
	set, err := capability.NewPGSet([]capability.PGInstance{
		{Provider: provA, Cells: []string{"accesscore", "auditcore"}},
		{Provider: provB, Cells: []string{"configcore"}},
	})
	require.NoError(t, err)

	gotAccess, err := set.ForCell("accesscore")
	require.NoError(t, err)
	gotAudit, err := set.ForCell("auditcore")
	require.NoError(t, err)
	gotConfig, err := set.ForCell("configcore")
	require.NoError(t, err)

	assert.True(t, gotAccess == provA, "accesscore routes to its DSN group's provider")
	assert.True(t, gotAudit == provA, "auditcore (same DSN group) routes to the same provider")
	assert.True(t, gotConfig == provB, "configcore routes to its own group's provider")
	assert.False(t, gotAccess == gotConfig, "cross-group providers are distinct (split isolation)")

	_, err = set.ForCell("nonexistentcell")
	require.Error(t, err, "an unprovisioned cell must fail closed")
}
