//go:build integration

package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	accesspg "github.com/ghbvf/gocell/cells/accesscore/postgres"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// buildAccessCorePGOptions returns the PG-backed repository options for accesscore:
// user/role/session/refresh stores all backed by the supplied pool + txMgr.
// Mirrors the wiring in access_module.go (Provide durable path).
func buildAccessCorePGOptions(tb testing.TB, pool *adapterpg.Pool, txMgr *adapterpg.TxManager) []accesscore.Option {
	tb.Helper()
	pgDeps, err := accesspg.NewDeps(pool.DB(), txMgr, clock.Real())
	require.NoError(tb, err, "buildAccessCorePGOptions: accesspg.NewDeps")
	pgUserRepo, err := accesspg.NewUserRepository(pgDeps)
	require.NoError(tb, err, "buildAccessCorePGOptions: NewUserRepository")
	pgRoleRepo, err := accesspg.NewRoleRepository(pgDeps)
	require.NoError(tb, err, "buildAccessCorePGOptions: NewRoleRepository")
	pgSetupLock, err := accesspg.NewSetupLock(pgDeps)
	require.NoError(tb, err, "buildAccessCorePGOptions: NewSetupLock")

	sessionProto, err := session.NewProtocol(
		session.WithFingerprint(session.FingerprintJTIRef{}),
		session.WithOrdering(session.OrderingAuthzEpoch{}),
		session.WithRevokeOnAll(),
	)
	require.NoError(tb, err, "buildAccessCorePGOptions: session.NewProtocol")
	pgSessionStore, err := adapterpg.NewSessionStore(pool.DB(), txMgr, sessionProto, clock.Real())
	require.NoError(tb, err, "buildAccessCorePGOptions: NewSessionStore")
	pgRefreshStore, err := adapterpg.NewRefreshStore(pool.DB(), txMgr, accesscore.DefaultRefreshPolicy(), clock.Real(), nil)
	require.NoError(tb, err, "buildAccessCorePGOptions: NewRefreshStore")

	return []accesscore.Option{
		accesscore.WithUserRepository(pgUserRepo),
		accesscore.WithRoleRepository(pgRoleRepo),
		accesscore.WithSessionStore(pgSessionStore),
		accesscore.WithRefreshStore(pgRefreshStore),
		accesscore.WithSetupLock(pgSetupLock),
	}
}
