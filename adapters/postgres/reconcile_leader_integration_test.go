//go:build integration

package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/kernel/reconcile/reconciletest"
)

// reconcilePGTestLeaseTTL + mustPGLease are declared in the untagged
// reconcile_leader_test.go so this integration-tagged file shares them without a
// duplicate declaration (mirrors the redis adapter's reconcileTestLeaseTTL).

// TestIntegration_ReconcileElectorConformance runs the cross-implementation
// LeaderElector + fencing conformance suites against a real PostgreSQL
// (testcontainers via migratedPool), validating the session-scoped
// pg_try_advisory_lock gate + the ON CONFLICT epoch-bump UPSERT. Each factory
// call builds a distinct-holder elector sharing one pool, so the suites' two
// holders contend for the same reconcile_leases row / advisory lock.
func TestIntegration_ReconcileElectorConformance(t *testing.T) {
	pool := migratedPool(t)

	factory := func(string) reconcile.LeaderElector {
		e, err := NewReconcileElector(pool, mustPGLease(t))
		require.NoError(t, err)
		return e
	}

	t.Run("Leader", func(t *testing.T) { reconciletest.RunLeaderConformance(t, factory) })
	t.Run("Fencing", func(t *testing.T) { reconciletest.RunFencingConformance(t, factory) })
}
