package reconcile_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/kernel/reconcile/reconciletest"
)

// TestLoop_LeaderElectInjectsFencedWriterWithLeaseEpoch verifies that a Loop in
// leader-elect mode acquires the lease, dispatches Reconcile, and injects an
// epoch-bound FencedWriter (carrying the live lease's Epoch) as the reconciler's
// write surface — and that the write lands at that epoch.
func TestLoop_LeaderElectInjectsFencedWriterWithLeaseEpoch(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	backend := reconciletest.NewFakeLeaseBackend(clock.Real())
	repo := reconciletest.NewFakeFencedRepository()
	src := make(chan reconcile.Request, 1)
	got := make(chan uint64, 1)

	rec := reconciletest.FakeReconciler{Fn: func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
		w, ok := reconcile.FencedWriterFrom(ctx)
		if !ok {
			t.Error("leader-elect + FencedRepo must inject a FencedWriter into ctx")
			got <- 0
			return reconcile.Result{}, nil
		}
		_ = w.Write(ctx, req.EntityID, "cmd")
		got <- w.Epoch()
		return reconcile.Result{RequeueAfter: time.Hour}, nil // avoid rapid requeue churn
	}}

	l := &reconcile.Loop{
		ReconcilerID: "leadertest",
		Reconciler:   rec,
		Source:       src,
		Leader:       backend.Elector("A"),
		FencedRepo:   repo,
	}
	ownerCtx, cancel := context.WithCancel(context.Background())
	require.NoError(t, l.Start(ownerCtx))
	src <- reconcile.Request{EntityID: "dev-1"}

	select {
	case epoch := <-got:
		require.Equal(t, uint64(1), epoch, "first lease term binds Epoch 1")
	case <-time.After(2 * time.Second):
		t.Fatal("Reconcile did not run under leadership")
	}
	require.Equal(t, uint64(1), repo.LastEpoch("dev-1"), "fenced write recorded at the lease epoch")

	cancel()
	require.NoError(t, l.Stop(context.Background()))
}

// TestLoop_LeaderElectLostLeaseCancelsInflight verifies the lost-lease interrupt
// (ADR §4.2): when the lease expires and the next renew fails, the Loop cancels
// the lease-scoped ctx, interrupting the in-flight Reconcile.
func TestLoop_LeaderElectLostLeaseCancelsInflight(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	backend := reconciletest.NewFakeLeaseBackend(clock.Real())
	src := make(chan reconcile.Request, 1)
	started := make(chan struct{}, 1)
	ctxErr := make(chan error, 1)

	rec := reconciletest.FakeReconciler{Fn: func(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-ctx.Done() // block until the lease-scoped ctx is canceled
		ctxErr <- ctx.Err()
		return reconcile.Result{}, ctx.Err()
	}}

	l := &reconcile.Loop{
		ReconcilerID:  "leaderlost",
		Reconciler:    rec,
		Source:        src,
		Leader:        backend.Elector("A"),
		RenewInterval: 20 * time.Millisecond, // fast renew so the lost lease is detected quickly
	}
	ownerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, l.Start(ownerCtx))
	src <- reconcile.Request{EntityID: "dev-1"}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("Reconcile never started")
	}

	backend.Expire("leaderlost") // lease expires; next renew (≤20ms) fails → lease ctx canceled

	select {
	case err := <-ctxErr:
		require.Error(t, err, "in-flight Reconcile ctx must be canceled the instant the lease is lost")
	case <-time.After(2 * time.Second):
		t.Fatal("lost lease did not interrupt the in-flight Reconcile")
	}

	cancel()
	require.NoError(t, l.Stop(context.Background()))
}

// TestLoop_LeaderElectFollowerDoesNotDispatch verifies whole-loop leader election:
// only the lease holder dispatches Reconcile; the follower holds idle.
func TestLoop_LeaderElectFollowerDoesNotDispatch(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	backend := reconciletest.NewFakeLeaseBackend(clock.Real())
	var aCount, bCount atomic.Int64
	mkRec := func(c *atomic.Int64) reconciletest.FakeReconciler {
		return reconciletest.FakeReconciler{Fn: func(_ context.Context, _ reconcile.Request) (reconcile.Result, error) {
			c.Add(1)
			return reconcile.Result{RequeueAfter: time.Hour}, nil
		}}
	}
	srcA := make(chan reconcile.Request, 1)
	srcB := make(chan reconcile.Request, 1)
	la := &reconcile.Loop{ReconcilerID: "foll", Reconciler: mkRec(&aCount), Source: srcA, Leader: backend.Elector("A")}
	lb := &reconcile.Loop{ReconcilerID: "foll", Reconciler: mkRec(&bCount), Source: srcB, Leader: backend.Elector("B")}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, la.Start(ctx)) // A contends first → becomes leader
	time.Sleep(50 * time.Millisecond) // let A's manager acquire (in-memory, sub-ms)
	require.NoError(t, lb.Start(ctx)) // B is the follower

	srcA <- reconcile.Request{EntityID: "x"}
	srcB <- reconcile.Request{EntityID: "y"} // buffered but never drained (follower spawns no workers)
	time.Sleep(200 * time.Millisecond)

	require.Equal(t, int64(1), aCount.Load(), "the leader dispatches its request")
	require.Equal(t, int64(0), bCount.Load(), "the follower does not dispatch")

	cancel()
	require.NoError(t, la.Stop(context.Background()))
	require.NoError(t, lb.Stop(context.Background()))
}
