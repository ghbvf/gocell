package certlifecycle_test

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/reconcile/reconciletest"
)

// TestReconcileLoopBoundedSweep drives the Reconciler through a real Loop with a
// resync-all pulse and asserts every due candidate is renewed in one sweep.
func TestReconcileLoopBoundedSweep(t *testing.T) {
	t.Parallel()
	now := time.Now()
	nb, na := dueWindow(now)
	repo := newFakeRepo(
		activeCandidate(t, "device-1", nb, na),
		activeCandidate(t, "device-2", nb, na),
		activeCandidate(t, "device-3", nb, na),
	)
	signer := newFakeSigner(t, now, now.Add(testSignTTL))
	authz := &fakeAuthorizer{grant: grantAll(t, testSignTTL)}
	rec := newReconciler(t, repo, signer, authz)

	stop := driveLoop(t, rec, repo)
	defer stop()
	waitUntil(t, "all three certs renewed", func() bool { return len(repo.mutations()) == 3 })

	seen := map[string]bool{}
	for _, m := range repo.mutations() {
		seen[m.DeviceID] = true
		if m.TargetEpoch != 2 {
			t.Errorf("%s TargetEpoch = %d, want 2", m.DeviceID, m.TargetEpoch)
		}
	}
	for _, d := range []string{"device-1", "device-2", "device-3"} {
		if !seen[d] {
			t.Errorf("device %s was not renewed in the sweep", d)
		}
	}
}

// TestReconcileLoopMultiReplicaFencing proves cross-replica safety via the
// monotonic LeaseToken.Epoch: a handoff bumps the epoch, the live leader's
// renewal lands at the higher epoch, and a zombie old-leader write at the stale
// epoch is rejected by the CAS — exactly one renewal effect survives.
func TestReconcileLoopMultiReplicaFencing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	const rid = testReconcilerID // the lease key the test Loop acquires under

	backend := reconciletest.NewFakeLeaseBackend(clock.Real())

	// Old leader acquires (epoch N) then hands off.
	old := backend.Elector("holder-old")
	tokOld, err := old.AcquireLease(ctx, rid)
	if err != nil {
		t.Fatalf("old acquire: %v", err)
	}
	if err := old.ReleaseLease(ctx, tokOld); err != nil {
		t.Fatalf("old release: %v", err)
	}

	now := time.Now()
	nb, na := dueWindow(now)
	repo := newFakeRepo(activeCandidate(t, "device-1", nb, na))
	signer := newFakeSigner(t, now, now.Add(testSignTTL))
	authz := &fakeAuthorizer{grant: grantAll(t, testSignTTL)}
	rec := newReconciler(t, repo, signer, authz)

	// New leader (epoch N+1) drives the Loop; its renewal lands at the higher epoch.
	stop := driveLoopWithLeader(t, rec, repo, backend.Elector("holder-new"))
	defer stop()
	waitUntil(t, "new leader renewed", func() bool { return len(repo.mutations()) == 1 })

	if got := repo.LastEpoch("device-1"); got <= tokOld.Epoch {
		t.Fatalf("live write epoch %d must exceed the old leader's epoch %d (handoff must bump)", got, tokOld.Epoch)
	}

	// Zombie old leader replays an in-flight write at the STALE epoch — rejected.
	accepted, err := repo.ApplyFenced(ctx, "device-1", tokOld.Epoch, "zombie-renewal")
	if err != nil {
		t.Fatalf("zombie write: %v", err)
	}
	if accepted {
		t.Fatal("stale-epoch zombie write must be rejected by the fencing CAS")
	}
	// Exactly one renewal effect survives (the live leader's IssuedMutation).
	if got := len(repo.mutations()); got != 1 {
		t.Fatalf("recorded %d renewal mutations, want exactly 1 (no duplicate from zombie)", got)
	}
}
