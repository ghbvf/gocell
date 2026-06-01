package reconciletest

import (
	"context"
	"errors"
	"testing"

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
//
// Plain testing.T assertions (no testify): this is a kernel test-support package
// and the kernel-isolation depguard bans testify outside *_test.go.
func RunLeaderConformance(t *testing.T, newElector ElectorFactory) {
	t.Helper()
	t.Run("AcquireExclusive", func(t *testing.T) { confAcquireExclusive(t, newElector) })
	t.Run("ReleaseUnblocksFollower", func(t *testing.T) { confReleaseUnblocksFollower(t, newElector) })
	t.Run("RenewKeepsLeaseAndEpoch", func(t *testing.T) { confRenewKeepsLeaseAndEpoch(t, newElector) })
	t.Run("HandoffBumpsEpoch", func(t *testing.T) { confHandoffBumpsEpoch(t, newElector) })
	t.Run("RenewAfterTakeoverIsLost", func(t *testing.T) { confRenewAfterTakeoverIsLost(t, newElector) })
}

func confAcquireExclusive(t *testing.T, newElector ElectorFactory) {
	ctx := context.Background()
	const rid = "conf-exclusive"
	a, b := newElector("holder-A"), newElector("holder-B")

	tokA, err := a.AcquireLease(ctx, rid)
	mustNoErr(t, err, "first holder must acquire")
	defer func() { _ = a.ReleaseLease(ctx, tokA) }() // release held resources (PG session conn)

	if _, err = b.AcquireLease(ctx, rid); !errors.Is(err, reconcile.ErrLeaseHeld) {
		t.Fatalf("contention must report ErrLeaseHeld (logged at Debug); got %v", err)
	}
}

func confReleaseUnblocksFollower(t *testing.T, newElector ElectorFactory) {
	ctx := context.Background()
	const rid = "conf-release"
	a, b := newElector("holder-A"), newElector("holder-B")

	tokA, err := a.AcquireLease(ctx, rid)
	mustNoErr(t, err, "acquire A")
	mustNoErr(t, a.ReleaseLease(ctx, tokA), "release A")

	tokB, err := b.AcquireLease(ctx, rid)
	mustNoErr(t, err, "follower must acquire after the leader releases")
	_ = b.ReleaseLease(ctx, tokB)
}

func confRenewKeepsLeaseAndEpoch(t *testing.T, newElector ElectorFactory) {
	ctx := context.Background()
	const rid = "conf-renew"
	a, b := newElector("holder-A"), newElector("holder-B")

	tokA, err := a.AcquireLease(ctx, rid)
	mustNoErr(t, err, "acquire A")
	defer func() { _ = a.ReleaseLease(ctx, tokA) }()
	mustNoErr(t, a.RenewLease(ctx, tokA), "renew must succeed while held")

	if _, err = b.AcquireLease(ctx, rid); !errors.Is(err, reconcile.ErrLeaseHeld) {
		t.Fatalf("renew must keep the follower locked out; got %v", err)
	}

	// Idempotent same-holder re-acquire of a live lease keeps the epoch.
	tokA2, err := a.AcquireLease(ctx, rid)
	mustNoErr(t, err, "idempotent re-acquire")
	if tokA2.Epoch != tokA.Epoch {
		t.Fatalf("same-holder re-acquire of a live lease must keep the epoch: got %d, want %d", tokA2.Epoch, tokA.Epoch)
	}
}

func confHandoffBumpsEpoch(t *testing.T, newElector ElectorFactory) {
	ctx := context.Background()
	const rid = "conf-handoff"
	a, b := newElector("holder-A"), newElector("holder-B")

	tokA, err := a.AcquireLease(ctx, rid)
	mustNoErr(t, err, "acquire A")
	mustNoErr(t, a.ReleaseLease(ctx, tokA), "release A")

	tokB, err := b.AcquireLease(ctx, rid)
	mustNoErr(t, err, "acquire B")
	defer func() { _ = b.ReleaseLease(ctx, tokB) }()
	if tokB.Epoch <= tokA.Epoch {
		t.Fatalf("a holder change must bump the monotonic epoch: B=%d not > A=%d", tokB.Epoch, tokA.Epoch)
	}
}

func confRenewAfterTakeoverIsLost(t *testing.T, newElector ElectorFactory) {
	ctx := context.Background()
	const rid = "conf-renew-lost"
	a, b := newElector("holder-A"), newElector("holder-B")

	tokA, err := a.AcquireLease(ctx, rid)
	mustNoErr(t, err, "acquire A")
	mustNoErr(t, a.ReleaseLease(ctx, tokA), "release A")
	tokB, err := b.AcquireLease(ctx, rid)
	mustNoErr(t, err, "acquire B")
	defer func() { _ = b.ReleaseLease(ctx, tokB) }()

	// A's renew must now report the lease lost (B owns it).
	if err := a.RenewLease(ctx, tokA); !errors.Is(err, reconcile.ErrReconcileLeaseLost) {
		t.Fatalf("renew after takeover must report ErrReconcileLeaseLost (drives lost-lease ctx cancel); got %v", err)
	}
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
	mustNoErr(t, err, "acquire A")
	mustNoErr(t, a.ReleaseLease(ctx, tokA), "old leader hands off")

	b := newElector("holder-B")
	tokB, err := b.AcquireLease(ctx, rid)
	mustNoErr(t, err, "acquire B")
	defer func() { _ = b.ReleaseLease(ctx, tokB) }()
	if tokB.Epoch <= tokA.Epoch {
		t.Fatalf("takeover must bump the fencing epoch: B=%d not > A=%d", tokB.Epoch, tokA.Epoch)
	}

	repo := NewFakeFencedRepository()

	// New leader (epoch N+1) applies its write — accepted.
	accepted, err := repo.ApplyFenced(ctx, entity, tokB.Epoch, "command-from-new-leader")
	mustNoErr(t, err, "new-leader write")
	if !accepted {
		t.Fatal("the current leader's write must be accepted")
	}

	// Zombie old leader (epoch N) replays its in-flight write AFTER the handoff —
	// the monotonic CAS must reject it.
	accepted, err = repo.ApplyFenced(ctx, entity, tokA.Epoch, "command-from-zombie-leader")
	mustNoErr(t, err, "zombie-leader write")
	if accepted {
		t.Fatal("stale-epoch zombie write must be rejected by the fencing CAS")
	}

	// No duplicate command: exactly one effect, from the new leader.
	if got := repo.LastEpoch(entity); got != tokB.Epoch {
		t.Fatalf("last accepted epoch = %d, want %d (the new leader's)", got, tokB.Epoch)
	}
	effects := repo.Effects()
	if len(effects) != 1 {
		t.Fatalf("the stale replay must not produce a duplicate effect: got %d effects", len(effects))
	}
	if effects[0].Mutation != "command-from-new-leader" {
		t.Fatalf("the surviving effect must be the new leader's: got %v", effects[0].Mutation)
	}
}

// mustNoErr fails the test fatally if err is non-nil (testify-free helper).
func mustNoErr(t *testing.T, err error, msg string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", msg, err)
	}
}
