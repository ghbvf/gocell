package reconciletest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/reconcile"
)

// ElectorFactory builds a LeaderElector for the given holderID, all sharing one
// backend (same redis client / pg pool / FakeLeaseBackend) so two holders
// contend for the same lease. Distinct holderIDs simulate distinct replicas.
type ElectorFactory func(holderID string) reconcile.LeaderElector

// RunLeaderConformance exercises a LeaderElector implementation across the
// deterministic, wall-clock-free parts of the contract: mutual exclusion, graceful
// release handoff, renew keeping ownership + epoch, and monotonic epoch bump on
// holder change. Both adapters (redis miniredis, postgres integration) and the
// fake run it. TTL-expiry takeover is wall-clock-dependent and is covered by
// implementation-specific tests (the fake's Expire helper, miniredis FastForward).
func RunLeaderConformance(t *testing.T, newElector ElectorFactory) {
	t.Helper()

	t.Run("AcquireExclusive", func(t *testing.T) {
		ctx := context.Background()
		const rid = "conf-exclusive"
		a, b := newElector("holder-A"), newElector("holder-B")

		tokA, err := a.AcquireLease(ctx, rid)
		require.NoError(t, err, "first holder must acquire")
		defer func() { _ = a.ReleaseLease(ctx, tokA) }() // release held resources (PG session conn)

		_, err = b.AcquireLease(ctx, rid)
		require.Error(t, err, "second holder must be denied a live lease")
		require.ErrorIs(t, err, reconcile.ErrLeaseHeld, "contention must report ErrLeaseHeld (logged at Debug)")
	})

	t.Run("ReleaseUnblocksFollower", func(t *testing.T) {
		ctx := context.Background()
		const rid = "conf-release"
		a, b := newElector("holder-A"), newElector("holder-B")

		tokA, err := a.AcquireLease(ctx, rid)
		require.NoError(t, err)
		require.NoError(t, a.ReleaseLease(ctx, tokA))

		tokB, err := b.AcquireLease(ctx, rid)
		require.NoError(t, err, "follower must acquire after the leader releases")
		_ = b.ReleaseLease(ctx, tokB)
	})

	t.Run("RenewKeepsLeaseAndEpoch", func(t *testing.T) {
		ctx := context.Background()
		const rid = "conf-renew"
		a, b := newElector("holder-A"), newElector("holder-B")

		tokA, err := a.AcquireLease(ctx, rid)
		require.NoError(t, err)
		defer func() { _ = a.ReleaseLease(ctx, tokA) }()
		require.NoError(t, a.RenewLease(ctx, tokA), "renew must succeed while held")

		_, err = b.AcquireLease(ctx, rid)
		require.ErrorIs(t, err, reconcile.ErrLeaseHeld, "renew must keep the follower locked out")

		// Idempotent same-holder re-acquire of a live lease keeps the epoch.
		tokA2, err := a.AcquireLease(ctx, rid)
		require.NoError(t, err)
		require.Equal(t, tokA.Epoch, tokA2.Epoch, "same-holder re-acquire of a live lease must keep the epoch")
	})

	t.Run("HandoffBumpsEpoch", func(t *testing.T) {
		ctx := context.Background()
		const rid = "conf-handoff"
		a, b := newElector("holder-A"), newElector("holder-B")

		tokA, err := a.AcquireLease(ctx, rid)
		require.NoError(t, err)
		require.NoError(t, a.ReleaseLease(ctx, tokA))

		tokB, err := b.AcquireLease(ctx, rid)
		require.NoError(t, err)
		defer func() { _ = b.ReleaseLease(ctx, tokB) }()
		require.Greater(t, tokB.Epoch, tokA.Epoch, "a holder change must bump the monotonic epoch")
	})

	t.Run("RenewAfterTakeoverIsLost", func(t *testing.T) {
		ctx := context.Background()
		const rid = "conf-renew-lost"
		a, b := newElector("holder-A"), newElector("holder-B")

		tokA, err := a.AcquireLease(ctx, rid)
		require.NoError(t, err)
		require.NoError(t, a.ReleaseLease(ctx, tokA))
		tokB, err := b.AcquireLease(ctx, rid)
		require.NoError(t, err)
		defer func() { _ = b.ReleaseLease(ctx, tokB) }()

		// A's renew must now report the lease lost (B owns it).
		err = a.RenewLease(ctx, tokA)
		require.ErrorIs(t, err, reconcile.ErrReconcileLeaseLost,
			"renew after takeover must report ErrReconcileLeaseLost (drives the Loop's lost-lease ctx cancel)")
	})
}

// RunFencingConformance is the real-failure-injection: a zombie old leader's write
// at epoch N, replayed AFTER a follower took over at epoch N+1, MUST be rejected by
// the monotonic-epoch CAS, and MUST leave no duplicate effect. It pairs the real
// elector (which must produce a strictly higher epoch on handoff) with a
// FakeFencedRepository standing in for a consumer's epoch-aware store (no real
// consumer ships in PR-A6). This proves "leader election ≠ fencing": even though
// both leaders briefly believed they led, only the higher-epoch write lands.
func RunFencingConformance(t *testing.T, newElector ElectorFactory) {
	t.Helper()
	ctx := context.Background()
	const (
		rid    = "fence-rid"
		entity = "device-1"
	)

	a := newElector("holder-A")
	tokA, err := a.AcquireLease(ctx, rid)
	require.NoError(t, err)
	require.NoError(t, a.ReleaseLease(ctx, tokA), "old leader hands off")

	b := newElector("holder-B")
	tokB, err := b.AcquireLease(ctx, rid)
	require.NoError(t, err)
	defer func() { _ = b.ReleaseLease(ctx, tokB) }()
	require.Greater(t, tokB.Epoch, tokA.Epoch, "takeover must bump the fencing epoch")

	repo := NewFakeFencedRepository()

	// New leader (epoch N+1) applies its write — accepted.
	accepted, err := repo.ApplyFenced(ctx, entity, tokB.Epoch, "command-from-new-leader")
	require.NoError(t, err)
	require.True(t, accepted, "the current leader's write must be accepted")

	// Zombie old leader (epoch N) replays its in-flight write AFTER the handoff —
	// the monotonic CAS must reject it.
	accepted, err = repo.ApplyFenced(ctx, entity, tokA.Epoch, "command-from-zombie-leader")
	require.NoError(t, err)
	require.False(t, accepted, "stale-epoch zombie write must be rejected by the fencing CAS")

	// No duplicate command: exactly one effect, from the new leader.
	require.Equal(t, tokB.Epoch, repo.LastEpoch(entity))
	effects := repo.Effects()
	require.Len(t, effects, 1, "the stale replay must not produce a duplicate effect")
	require.Equal(t, "command-from-new-leader", effects[0].Mutation)
}
