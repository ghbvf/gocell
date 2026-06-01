//go:build integration

package redis

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/kernel/reconcile/reconciletest"
)

// TestIntegration_ReconcileElectorConformance runs the cross-implementation
// LeaderElector + fencing conformance suites against a real Redis (testcontainers),
// validating the SET-NX + epoch-INCR Lua atomicity that the unit-test mock only
// approximates. Each factory call builds a distinct-holder elector sharing one
// client, so the suites' two holders contend for the same lease.
func TestIntegration_ReconcileElectorConformance(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	factory := func(holderID string) reconcile.LeaderElector {
		e, err := NewRedisReconcileElector(client, "reconcile", holderID, 30*time.Second)
		require.NoError(t, err)
		return e
	}

	t.Run("Leader", func(t *testing.T) { reconciletest.RunLeaderConformance(t, factory) })
	t.Run("Fencing", func(t *testing.T) { reconciletest.RunFencingConformance(t, factory) })
}
