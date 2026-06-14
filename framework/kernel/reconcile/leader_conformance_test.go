package reconcile_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/reconcile"
	"github.com/ghbvf/gocell/framework/kernel/reconcile/reconciletest"
)

// TestFakeLeaderElector_Conformance runs the cross-implementation suites against
// the in-memory fake: the same suites adapters/redis and adapters/postgres run.
func TestFakeLeaderElector_Conformance(t *testing.T) {
	t.Parallel()
	t.Run("Leader", func(t *testing.T) {
		t.Parallel()
		backend := reconciletest.NewFakeLeaseBackend(clock.Real())
		reconciletest.RunLeaderConformance(t, func(h string) reconcile.LeaderElector { return backend.Elector(h) })
	})
	t.Run("Fencing", func(t *testing.T) {
		t.Parallel()
		backend := reconciletest.NewFakeLeaseBackend(clock.Real())
		reconciletest.RunFencingConformance(t, func(h string) reconcile.LeaderElector { return backend.Elector(h) })
	})
}

// TestLeaderElector_AcquireExclusive (T21): two holders, only one wins.
func TestLeaderElector_AcquireExclusive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := reconciletest.NewFakeLeaseBackend(clock.Real())
	a, b := backend.Elector("A"), backend.Elector("B")

	_, err := a.AcquireLease(ctx, "rid")
	require.NoError(t, err)
	_, err = b.AcquireLease(ctx, "rid")
	require.ErrorIs(t, err, reconcile.ErrLeaseHeld)
}

// TestLeaderElector_ReleaseUnblocksFollower (T21).
func TestLeaderElector_ReleaseUnblocksFollower(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := reconciletest.NewFakeLeaseBackend(clock.Real())
	a, b := backend.Elector("A"), backend.Elector("B")

	tok, err := a.AcquireLease(ctx, "rid")
	require.NoError(t, err)
	require.NoError(t, a.ReleaseLease(ctx, tok))
	_, err = b.AcquireLease(ctx, "rid")
	require.NoError(t, err)
}

// TestLeaderElector_RenewKeepsLease (T21): renew keeps ownership + epoch.
func TestLeaderElector_RenewKeepsLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := reconciletest.NewFakeLeaseBackend(clock.Real())
	a, b := backend.Elector("A"), backend.Elector("B")

	tok, err := a.AcquireLease(ctx, "rid")
	require.NoError(t, err)
	require.NoError(t, a.RenewLease(ctx, tok))
	_, err = b.AcquireLease(ctx, "rid")
	require.ErrorIs(t, err, reconcile.ErrLeaseHeld, "renew must keep the follower locked out")

	tok2, err := a.AcquireLease(ctx, "rid")
	require.NoError(t, err)
	require.Equal(t, tok.Epoch, tok2.Epoch, "live re-acquire keeps epoch")
}

// TestLeaderElector_StaleFollowerAcquiresOnExpiry (T21): leader stops renewing →
// follower takes over once the lease expires (deterministic via Expire helper).
func TestLeaderElector_StaleFollowerAcquiresOnExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	backend := reconciletest.NewFakeLeaseBackend(clock.Real())
	a, b := backend.Elector("A"), backend.Elector("B")

	tokA, err := a.AcquireLease(ctx, "rid")
	require.NoError(t, err)

	// Before expiry the follower is locked out.
	_, err = b.AcquireLease(ctx, "rid")
	require.ErrorIs(t, err, reconcile.ErrLeaseHeld)

	// Lease expires (leader paused / crashed); follower takes over with a bumped epoch.
	backend.Expire("rid")
	tokB, err := b.AcquireLease(ctx, "rid")
	require.NoError(t, err)
	require.Greater(t, tokB.Epoch, tokA.Epoch, "expiry takeover must bump the fencing epoch")

	// The stale old leader's renew now fails (drives the Loop's lost-lease cancel).
	require.ErrorIs(t, a.RenewLease(ctx, tokA), reconcile.ErrReconcileLeaseLost)
}
