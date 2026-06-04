package reconcile_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/kernel/reconcile/reconciletest"
)

// Site-specific test durations (TEST-TIME-LITERAL-01: literals must live in a
// package-level const initializer, call sites reference the const).
const (
	leaderTestWaitShort   = 2 * time.Second        // generous deadline for a channel signal
	leaderTestWaitMedium  = 6 * time.Second        // takeover headroom past lease expiry
	leaderTestWaitLong    = 10 * time.Second       // I/O-retry backoff headroom (~2×leaderRetryPeriod)
	leaderTestFastRenew   = 20 * time.Millisecond  // fast renew so a lost lease is detected quickly
	leaderTestSettleSleep = 200 * time.Millisecond // CI headroom: goroutine scheduling can exceed 50ms under load
	leaderTestShortTTL    = 100 * time.Millisecond // short lease TTL so takeover is quick
	// leaderTestFollowerQuiet bounds the "follower must not dispatch" quiet period.
	// Kept < leaderRetryPeriod (1s) so the follower stays a follower (no re-acquire
	// attempt) for the whole window.
	leaderTestFollowerQuiet = 400 * time.Millisecond
)

// TestLoop_LeaderElectInjectsFencedWriterWithLeaseEpoch verifies that a Loop in
// leader-elect mode acquires the lease, dispatches Reconcile, and injects an
// epoch-bound FencedWriter (carrying the live lease's Epoch) as the reconciler's
// write surface — and that the write lands at that epoch.
func TestLoop_LeaderElectInjectsFencedWriterWithLeaseEpoch(t *testing.T) {
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

	l, err := reconcile.New(rec).
		WithTrigger(reconciletest.FakeTrigger{In: src}).
		WithReconcilerID("leadertest").
		WithLeader(backend.Elector("A")).
		WithFencedRepo(repo).
		Build()
	require.NoError(t, err)

	ownerCtx, cancel := context.WithCancel(context.Background())
	require.NoError(t, l.Start(ownerCtx))
	src <- reconcile.Request{EntityID: "dev-1"}

	select {
	case epoch := <-got:
		require.Equal(t, uint64(1), epoch, "first lease term binds Epoch 1")
	case <-time.After(leaderTestWaitShort):
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

	l, err := reconcile.New(rec).
		WithTrigger(reconciletest.FakeTrigger{In: src}).
		WithReconcilerID("leaderlost").
		WithLeader(backend.Elector("A")).
		WithRenewInterval(leaderTestFastRenew). // fast renew so the lost lease is detected quickly
		Build()
	require.NoError(t, err)

	ownerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, l.Start(ownerCtx))
	src <- reconcile.Request{EntityID: "dev-1"}

	select {
	case <-started:
	case <-time.After(leaderTestWaitShort):
		t.Fatal("Reconcile never started")
	}

	backend.Expire("leaderlost") // lease expires; next renew (≤20ms) fails → lease ctx canceled

	select {
	case err := <-ctxErr:
		require.Error(t, err, "in-flight Reconcile ctx must be canceled the instant the lease is lost")
	case <-time.After(leaderTestWaitShort):
		t.Fatal("lost lease did not interrupt the in-flight Reconcile")
	}

	cancel()
	require.NoError(t, l.Stop(context.Background()))
}

// TestLoop_LeaderElectFollowerDoesNotDispatch verifies whole-loop leader election:
// only the lease holder dispatches Reconcile; the follower holds idle.
// Channel-driven sync: we signal on leaderDispatched when the leader's reconciler
// runs, then assert follower count == 0 — no brittle sleeps on the assertion path.
func TestLoop_LeaderElectFollowerDoesNotDispatch(t *testing.T) {
	backend := reconciletest.NewFakeLeaseBackend(clock.Real())
	var bCount atomic.Int64
	leaderDispatched := make(chan struct{}, 1)

	leaderRec := reconciletest.FakeReconciler{Fn: func(_ context.Context, _ reconcile.Request) (reconcile.Result, error) {
		select {
		case leaderDispatched <- struct{}{}:
		default:
		}
		return reconcile.Result{RequeueAfter: time.Hour}, nil
	}}
	followerRec := reconciletest.FakeReconciler{Fn: func(_ context.Context, _ reconcile.Request) (reconcile.Result, error) {
		bCount.Add(1)
		return reconcile.Result{RequeueAfter: time.Hour}, nil
	}}

	srcA := make(chan reconcile.Request, 1)
	srcB := make(chan reconcile.Request, 1)

	la, err := reconcile.New(leaderRec).
		WithTrigger(reconciletest.FakeTrigger{In: srcA}).
		WithReconcilerID("foll").
		WithLeader(backend.Elector("A")).
		Build()
	require.NoError(t, err)

	lb, err := reconcile.New(followerRec).
		WithTrigger(reconciletest.FakeTrigger{In: srcB}).
		WithReconcilerID("foll").
		WithLeader(backend.Elector("B")).
		Build()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, la.Start(ctx)) // A contends first → becomes leader
	time.Sleep(leaderTestSettleSleep) //archtest:allow:test-sleep no signal for in-memory lease acquire (sub-ms)
	require.NoError(t, lb.Start(ctx)) // B is the follower

	srcA <- reconcile.Request{EntityID: "x"}
	srcB <- reconcile.Request{EntityID: "y"} // buffered but never drained (follower spawns no workers)

	// Wait for the leader to dispatch (channel-driven), then assert follower idle.
	select {
	case <-leaderDispatched:
	case <-time.After(leaderTestWaitShort):
		t.Fatal("leader did not dispatch within 2s")
	}
	require.Equal(t, int64(0), bCount.Load(), "the follower must not dispatch")

	cancel()
	require.NoError(t, la.Stop(context.Background()))
	require.NoError(t, lb.Stop(context.Background()))
}

// TestLoop_LeaderElectFollowerTakesOverAfterLeaseExpiry is the ADR Independent
// Test for "follower takes over": Loop A leads and dispatches; its lease is then
// forcibly expired; Loop B detects the vacancy and takes over. The assertion that
// B begins dispatching is channel-driven (no sleep on the critical path).
func TestLoop_LeaderElectFollowerTakesOverAfterLeaseExpiry(t *testing.T) {
	backend := reconciletest.NewFakeLeaseBackend(clock.Real())

	aDispatched := make(chan struct{}, 1)
	bDispatched := make(chan struct{}, 1)

	mkRec := func(sig chan<- struct{}) reconciletest.FakeReconciler {
		return reconciletest.FakeReconciler{Fn: func(_ context.Context, _ reconcile.Request) (reconcile.Result, error) {
			select {
			case sig <- struct{}{}:
			default:
			}
			return reconcile.Result{RequeueAfter: time.Hour}, nil
		}}
	}

	srcA := make(chan reconcile.Request, 2)
	srcB := make(chan reconcile.Request, 2)

	// Use a short TTL so B can take over quickly after expiry.
	la, err := reconcile.New(mkRec(aDispatched)).
		WithTrigger(reconciletest.FakeTrigger{In: srcA}).
		WithReconcilerID("takeover").
		WithLeader(backend.ElectorWithTTL("A", leaderTestShortTTL)).
		Build()
	require.NoError(t, err)

	lb, err := reconcile.New(mkRec(bDispatched)).
		WithTrigger(reconciletest.FakeTrigger{In: srcB}).
		WithReconcilerID("takeover").
		WithLeader(backend.ElectorWithTTL("B", leaderTestShortTTL)).
		Build()
	require.NoError(t, err)

	// Use separate contexts: cancelA stops A from re-contending after its lease
	// is expired so B can cleanly take over. cancelAll stops both loops at the end.
	ctxA, cancelA := context.WithCancel(context.Background())
	ctxAll, cancelAll := context.WithCancel(context.Background())
	defer cancelAll()

	require.NoError(t, la.Start(ctxA))
	srcA <- reconcile.Request{EntityID: "e1"}
	// Wait for A to dispatch (channel-driven: ensures A holds the lease).
	select {
	case <-aDispatched:
	case <-time.After(leaderTestWaitShort):
		t.Fatal("leader A did not dispatch within 2s")
	}

	// Start B as a confirmed follower (A holds the lease).
	require.NoError(t, lb.Start(ctxAll))
	srcB <- reconcile.Request{EntityID: "e2"}
	// Give B a moment to attempt its first AcquireLease (gets ErrLeaseHeld)
	// and enter its 2s sleep cycle.
	time.Sleep(leaderTestSettleSleep) //archtest:allow:test-sleep no signal for "B entered leaderRetryPeriod sleep after first ErrLeaseHeld"

	// Stop A (cancel its ctx) so it will not re-contend after lease expiry.
	// Then expire the lease so B sees it as available on its next retry.
	cancelA()
	require.NoError(t, la.Stop(context.Background()))
	backend.Expire("takeover")

	// Wait for B to dispatch (channel-driven). B wakes from its 2s leaderRetryPeriod
	// sleep, acquires the now-vacant lease, and dispatches. Give 6s headroom.
	select {
	case <-bDispatched:
	case <-time.After(leaderTestWaitMedium):
		t.Fatal("follower B did not take over within 6s after lease expiry")
	}

	cancelAll()
	require.NoError(t, lb.Stop(context.Background()))
}

// errAfterN is an in-test fake LeaderElector that fails AcquireLease the first N
// calls with a non-ErrLeaseHeld, non-ctx error (simulating a transient I/O fault),
// then delegates to the real FakeLeaderElector. This exercises the leaderManage
// Warn-path retry loop.
type errAfterN struct {
	mu      sync.Mutex
	callN   int
	failFor int
	inner   reconcile.LeaderElector
}

func (e *errAfterN) AcquireLease(ctx context.Context, rid string) (reconcile.LeaseToken, error) {
	e.mu.Lock()
	n := e.callN
	e.callN++
	e.mu.Unlock()
	if n < e.failFor {
		return reconcile.LeaseToken{}, errors.New("simulated I/O fault on AcquireLease")
	}
	return e.inner.AcquireLease(ctx, rid)
}

func (e *errAfterN) RenewLease(ctx context.Context, tok reconcile.LeaseToken) error {
	return e.inner.RenewLease(ctx, tok)
}

func (e *errAfterN) ReleaseLease(ctx context.Context, tok reconcile.LeaseToken) error {
	return e.inner.ReleaseLease(ctx, tok)
}

// TestLoop_LeaderManageIOErrorRetry verifies the Warn-path retry: when AcquireLease
// returns a transient I/O error (not ErrLeaseHeld), leaderManage retries after
// leaderRetryPeriod and eventually acquires the lease and dispatches. This closes
// the coverage gap on the Warn retry branch.
func TestLoop_LeaderManageIOErrorRetry(t *testing.T) {
	backend := reconciletest.NewFakeLeaseBackend(clock.Real())
	dispatched := make(chan struct{}, 1)

	rec := reconciletest.FakeReconciler{Fn: func(_ context.Context, _ reconcile.Request) (reconcile.Result, error) {
		select {
		case dispatched <- struct{}{}:
		default:
		}
		return reconcile.Result{RequeueAfter: time.Hour}, nil
	}}

	src := make(chan reconcile.Request, 1)
	// Fail the first 2 AcquireLease calls with an I/O error, then succeed on the 3rd.
	elector := &errAfterN{failFor: 2, inner: backend.Elector("A")}

	l, err := reconcile.New(rec).
		WithTrigger(reconciletest.FakeTrigger{In: src}).
		WithReconcilerID("ioretry").
		WithLeader(elector).
		Build()
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, l.Start(ctx))
	src <- reconcile.Request{EntityID: "e1"}

	// After ~2×leaderRetryPeriod (~4s) the loop retries and acquires; wait up to 10s.
	select {
	case <-dispatched:
	case <-time.After(leaderTestWaitLong):
		t.Fatal("Loop did not dispatch after I/O error retries")
	}

	cancel()
	require.NoError(t, l.Stop(context.Background()))
}

// TestLoop_FollowerDoesNotConsumeTrigger pins the leader-gated Trigger contract
// (C1/F2): a leader-elect Loop that is a FOLLOWER (another holder owns the lease)
// must NOT start its Trigger, so it never consumes its external source before
// winning the lease — otherwise followers would steal/buffer events from the
// active leader (controller-runtime: sources do not run before winning election).
func TestLoop_FollowerDoesNotConsumeTrigger(t *testing.T) {
	backend := reconciletest.NewFakeLeaseBackend(clock.Real())
	const rid = "follower_noconsume"

	// holder-A holds the lease for the whole test (acquired directly, never
	// released; the fake's 30s default TTL outlasts the test), so B never wins.
	if _, err := backend.Elector("holder-A").AcquireLease(context.Background(), rid); err != nil {
		t.Fatalf("seed holder acquire: %v", err)
	}

	// B is a follower Loop. srcB is buffered so the submit never blocks (no leaked
	// sender goroutine); the request is only forwarded/dispatched if B's Trigger
	// were (wrongly) started while B is a follower.
	srcB := make(chan reconcile.Request, 1)
	var dispatched atomic.Int64
	recB := reconciletest.FakeReconciler{Fn: func(_ context.Context, _ reconcile.Request) (reconcile.Result, error) {
		dispatched.Add(1)
		return reconcile.Result{RequeueAfter: time.Hour}, nil
	}}
	lb, err := reconcile.New(recB).
		WithReconcilerID(rid).
		WithTrigger(reconciletest.FakeTrigger{In: srcB}).
		WithLeader(backend.Elector("holder-B")).
		WithRenewInterval(leaderTestFastRenew).
		Build()
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, lb.Start(ctx))
	defer func() { _ = lb.Stop(context.Background()) }()

	srcB <- reconcile.Request{EntityID: "e1"} // buffered; consumed only if B starts its Trigger

	// Quiet period (< leaderRetryPeriod so B stays follower). Absence-of-consumption
	// cannot be polled, so this is a genuine sleep.
	time.Sleep(leaderTestFollowerQuiet) //archtest:allow:test-sleep follower must not consume source before lease (absence not pollable)

	// Definitive F2 assertion: the request must STILL be in srcB — a follower that
	// (wrongly) started its Trigger would have drained srcB into the internal queue.
	// (Asserting "not dispatched" alone is insufficient: a follower spawns no
	// feedFromSource, so a wrongly-started Trigger consumes srcB→triggerCh yet still
	// never dispatches — bCount stays 0 either way.)
	select {
	case got := <-srcB:
		require.Equal(t, "e1", got.EntityID, "request still buffered → follower never consumed its source")
	default:
		t.Fatal("follower consumed its trigger source before winning the lease — Trigger must be leader-gated")
	}
	require.Equal(t, int64(0), dispatched.Load(), "follower must not dispatch")
}
