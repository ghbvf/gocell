//go:build integration

package authorizationdecide

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/adapters/postgres/pgtest"
	accesspgrepo "github.com/ghbvf/gocell/corecells/accesscore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// Package-shared PostgreSQL lifecycle: one pre-migrated template container per
// test binary; per-test isolation via TEMPLATE clone (see adapters/postgres/pgtest).
var sharedPG = pgtest.New("gocell_accesscore_authzconf_test_template")

func TestMain(m *testing.M) {
	code := m.Run()
	sharedPG.Shutdown()
	os.Exit(code)
}

// TestAuthorizerConformance_PG enrolls the PG-backed PolicyRepository in the same
// authorizer conformance suite the mem store runs (TestAuthorizerConformance),
// proving the engine decides identically when policies are persisted as JSONB and
// read back through the production path: a real adapterpg.TxManager drives
// scopedtx → SET LOCAL app.tenant_id → PGPolicyRepo.ListByTenant. Each sub-test
// gets a fresh per-test database (sharedPG.NewPerTestPool).
//
// RLS enforcement under a restricted (non-superuser) role is intentionally out of
// scope here — the test pool is superuser, so isolation is exercised via the
// application-level WHERE tenant_id predicate (identical to mem). The FORCE-RLS
// enforcement on the policies table is proven by schema_guard verifyRLS, the
// rls_force negative-drop test, and the PolicyRepository CrossTenant conformance.
func TestAuthorizerConformance_PG(t *testing.T) {
	runAuthorizerConformance(t, func(t *testing.T) (ports.PolicyRepository, persistence.CellTxManager) {
		pool := sharedPG.NewPerTestPool(t)
		txMgr := adapterpg.NewTxManager(pool)
		repo, err := accesspgrepo.NewPGPolicyRepo(pool.DB(), txMgr, clock.Real())
		require.NoError(t, err)
		return repo, persistence.WrapForCell(txMgr)
	})
}
