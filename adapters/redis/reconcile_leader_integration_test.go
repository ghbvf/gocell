//go:build integration

package redis

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
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

	factory := func(string) reconcile.LeaderElector {
		e, err := NewRedisReconcileElector(client, "reconcile", 30*time.Second, clock.Real())
		require.NoError(t, err)
		return e
	}

	t.Run("Leader", func(t *testing.T) { reconciletest.RunLeaderConformance(t, factory) })
	t.Run("Fencing", func(t *testing.T) { reconciletest.RunFencingConformance(t, factory) })
}

// TestIntegration_ReconcileElector_EpochKeyMissing_FailClosed validates against a
// REAL Redis that deleting the epoch key while the holder still owns the lease makes
// BOTH live-holder paths — same-holder re-acquire AND renew — return a fail-closed
// error (the Lua error_reply the in-memory mock can only approximate) rather than a
// silently rolled-back epoch 0 / a falsely-successful renew. Round-3 fencing-integrity
// fix; the free-holder rebuild-from-1 residual is an inherent Redis limitation (see
// reconcileAcquireScript godoc + ADR 202605291600-661 §threat-matrix).
func TestIntegration_ReconcileElector_EpochKeyMissing_FailClosed(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()
	ctx := context.Background()

	newElector := func() *RedisReconcileElector {
		e, err := NewRedisReconcileElector(client, "reconcile", 30*time.Second, clock.Real())
		require.NoError(t, err)
		return e
	}

	t.Run("same_holder_reacquire", func(t *testing.T) {
		e := newElector()
		_, err := e.AcquireLease(ctx, "rid-acq")
		require.NoError(t, err)
		require.NoError(t, e.rdb.Del(ctx, e.epochKey("rid-acq")).Err(), "delete epoch key (simulate eviction)")

		_, err = e.AcquireLease(ctx, "rid-acq")
		require.Error(t, err, "same-holder re-acquire with missing epoch key must fail-closed")
		require.NotErrorIs(t, err, reconcile.ErrLeaseHeld, "fencing-integrity fault, not contention")
	})

	t.Run("renew", func(t *testing.T) {
		e := newElector()
		tok, err := e.AcquireLease(ctx, "rid-renew")
		require.NoError(t, err)
		require.NoError(t, e.rdb.Del(ctx, e.epochKey("rid-renew")).Err(), "delete epoch key (simulate eviction)")

		err = e.RenewLease(ctx, tok)
		require.Error(t, err, "renew with missing epoch key must fail-closed")
		require.NotErrorIs(t, err, reconcile.ErrReconcileLeaseLost, "fencing-integrity fault, not a normal handoff")
	})
}
