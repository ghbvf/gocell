package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
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
