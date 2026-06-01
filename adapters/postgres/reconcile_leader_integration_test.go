//go:build integration

package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/kernel/reconcile/reconciletest"
)

// TestIntegration_ReconcileElectorConformance runs the cross-implementation
// LeaderElector + fencing conformance suites against a real PostgreSQL
// (testcontainers via migratedPool), validating the session-scoped
// pg_try_advisory_lock gate + the ON CONFLICT epoch-bump UPSERT. Each factory
// call builds a distinct-holder elector sharing one pool, so the suites' two
// holders contend for the same reconcile_leases row / advisory lock.
func TestIntegration_ReconcileElectorConformance(t *testing.T) {
	pool := migratedPool(t)

	factory := func(holderID string) reconcile.LeaderElector {
		e, err := NewReconcileElector(pool, holderID, 30*time.Second)
		require.NoError(t, err)
		return e
	}

	t.Run("Leader", func(t *testing.T) { reconciletest.RunLeaderConformance(t, factory) })
	t.Run("Fencing", func(t *testing.T) { reconciletest.RunFencingConformance(t, factory) })
}
